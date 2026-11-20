package subagentwork

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Store is the durable, owner-scoped record store. (owner, task_key) is the
// primary key: one task owns one workspace. (owner, invocation_id) is unique
// too, so one logical dispatch cannot allocate a second child.
//
// Every returned record is a copy the caller owns; mutating it never changes
// stored state.
type Store interface {
	// Create inserts the record if no record exists for its owner and task
	// key, and reports whether it created one. A record that already exists
	// for the owner and invocation is returned unchanged with created ==
	// false, so a retried dispatch observes the first attempt.
	Create(context.Context, Record) (Record, bool, error)
	// Get returns the owner's record for a task key.
	Get(context.Context, string, string) (Record, error)
	// ByInvocation returns the owner's record for a logical invocation.
	ByInvocation(context.Context, string, string) (Record, error)
	// CompareAndSwap stores rec only when the stored revision equals
	// expected, and returns the record with its incremented revision. A
	// mismatch is ErrStaleRecord.
	CompareAndSwap(context.Context, Record, uint64) (Record, error)
	// List returns every record the owner holds, ordered by task key.
	List(context.Context, string) ([]Record, error)
}

// NewMemoryStore returns an explicitly non-persistent Store for fixtures and
// tests. Production sessions use the database adapter: a workspace record that
// does not survive a restart cannot settle a retained task.
func NewMemoryStore() Store {
	return &memoryStore{
		records:     map[string]Record{},
		invocations: map[string]string{},
	}
}

type memoryStore struct {
	mu sync.Mutex
	// records is keyed by owner+"\x00"+task key.
	records map[string]Record
	// invocations maps owner+"\x00"+invocation id to the record key that owns
	// it, which makes the unique (owner, invocation) rule explicit in memory
	// the same way the table's unique index does.
	invocations map[string]string
}

func ownerKey(owner, key string) string { return owner + "\x00" + key }

// normalizeVersion accepts an unset version as the current one and rejects
// anything else, so a caller can never store a record this package cannot
// read back.
func normalizeVersion(rec Record) (Record, error) {
	switch rec.Version {
	case 0:
		rec.Version = RecordVersion
	case RecordVersion:
	default:
		return Record{}, fmt.Errorf("subagentwork: unsupported workspace record version %d", rec.Version)
	}
	return rec, nil
}

func (s *memoryStore) Create(ctx context.Context, rec Record) (Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}
	if rec.OwnerSessionID == "" || rec.TaskKey == "" || rec.InvocationID == "" {
		return Record{}, false, fmt.Errorf("subagentwork: a workspace record needs an owner, a task key and an invocation id")
	}
	normalized, err := normalizeVersion(rec)
	if err != nil {
		return Record{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := ownerKey(normalized.OwnerSessionID, normalized.TaskKey)
	if existing, ok := s.records[key]; ok {
		return existing.clone(), false, nil
	}
	// A duplicate logical dispatch keeps the record it already owns, even
	// when it was spelled with a different task key.
	if found, ok := s.invocations[ownerKey(normalized.OwnerSessionID, normalized.InvocationID)]; ok {
		if existing, ok := s.records[found]; ok {
			return existing.clone(), false, nil
		}
	}
	normalized.Revision = 1
	s.records[key] = normalized.clone()
	s.invocations[ownerKey(normalized.OwnerSessionID, normalized.InvocationID)] = key
	return normalized.clone(), true, nil
}

func (s *memoryStore) Get(ctx context.Context, owner, taskKey string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[ownerKey(owner, taskKey)]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	return rec.clone(), nil
}

func (s *memoryStore) ByInvocation(ctx context.Context, owner, invocationID string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.invocations[ownerKey(owner, invocationID)]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	rec, ok := s.records[key]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	return rec.clone(), nil
}

func (s *memoryStore) CompareAndSwap(ctx context.Context, rec Record, expected uint64) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	normalized, err := normalizeVersion(rec)
	if err != nil {
		return Record{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	key := ownerKey(normalized.OwnerSessionID, normalized.TaskKey)
	stored, ok := s.records[key]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	if stored.Revision != expected {
		return Record{}, ErrStaleRecord
	}
	normalized.Revision = expected + 1
	s.records[key] = normalized.clone()
	if stored.InvocationID != normalized.InvocationID {
		delete(s.invocations, ownerKey(stored.OwnerSessionID, stored.InvocationID))
		s.invocations[ownerKey(normalized.OwnerSessionID, normalized.InvocationID)] = key
	}
	return normalized.clone(), nil
}

func (s *memoryStore) List(ctx context.Context, owner string) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, rec := range s.records {
		if rec.OwnerSessionID == owner {
			out = append(out, rec.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskKey < out[j].TaskKey })
	return out, nil
}
