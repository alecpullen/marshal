package bridge

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The working-draft store is deliberately independent of the factory store:
// its own directory, its own journal and its own ownership lock, so a corrupt
// or unavailable factory journal cannot make incomplete drafts unreadable and
// a draft save can never touch factory state.
const (
	workingDraftJournalName   = "journal.log"
	workingDraftLockName      = "owner.lock"
	workingDraftJournalSchema = 1
	// workingDraftLockPIDPrefix starts the ownership lock's first line, which
	// names the process that took it. A complete lock is that line, then a
	// hex-encoded 32-byte token: workingDraftLockTokenLength is its length, so a
	// half-written file is never mistaken for a stale one.
	workingDraftLockPIDPrefix   = "pid:"
	workingDraftLockTokenLength = 64
)

// Journal operation kinds. The kind is part of every operation's digest, so a
// request ID reused for a different operation conflicts instead of replaying.
const (
	workingDraftOpCreate       = "create"
	workingDraftOpSave         = "save"
	workingDraftOpDelete       = "delete"
	workingDraftOpConvertBegin = "convert-begin"
	workingDraftOpConvertDone  = "convert-complete"
	// workingDraftOpConvertFail is a reserved conversion the factory definitely
	// refused: the draft returns to editing carrying the reason.
	workingDraftOpConvertFail = "convert-fail"
)

// errWorkingDraftCorrupt is a journal that cannot be trusted: a bad checksum,
// schema version or record transition. It is always reported wrapped in
// ErrWorkingDraftUnavailable, and the journal is never rebuilt or replaced.
var errWorkingDraftCorrupt = errors.New("working draft journal corrupt")

// errWorkingDraftOwned is a second store opening a directory another live
// store owns.
var errWorkingDraftOwned = errors.New("working draft store already owned; confirm the previous bridge stopped")

// workingDraftRequestRecord is the replay identity of one accepted mutation:
// the canonical digest of (operation, draft, request ID, payload) plus the
// result the caller originally received.
type workingDraftRequestRecord struct {
	DraftID string
	Digest  string
	Draft   WorkingDraft
}

// workingDraftConversionRecord is a conversion's frozen input. The exact
// FactoryCreate is retained only here; WorkingDraft never exposes it.
type workingDraftConversionRecord struct {
	// RequestID is the conversion request that reserved the record, so a
	// reconciliation knows the journal identity of the association it makes.
	// It is always set by this version; deriving from an empty one still yields
	// a stable identity, which is what keeps a replayed reservation safe.
	RequestID       string
	Request         json.RawMessage
	SettingsVersion string
	// Sequence is the journal sequence of the reservation, which orders
	// reconciliation's work newest first.
	Sequence uint64
}

// workingDraftPayload is one journal record's content. Records are versioned by
// the enclosing workingDraftRecord.Schema and carry everything needed to
// rebuild the store exactly, including replay identities.
type workingDraftPayload struct {
	Sequence        uint64          `json:"sequence"`
	Operation       string          `json:"operation"`
	RequestID       string          `json:"requestId"`
	Digest          string          `json:"digest"`
	Draft           WorkingDraft    `json:"draft"`
	FactoryRequest  json.RawMessage `json:"factoryRequest,omitempty"`
	SettingsVersion string          `json:"settingsVersion,omitempty"`
	// ConversionRequestID records which conversion request a reservation
	// belongs to, so reconciliation can derive the same journal identities a
	// live conversion would. It is part of the reservation's meaning, not a
	// schema change.
	ConversionRequestID string `json:"conversionRequestId,omitempty"`
}

type workingDraftRecord struct {
	Schema   uint64          `json:"schema"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

// WorkingDraftStore owns single-writer storage of working drafts. Every
// mutation is serialized under one mutex and appended to the journal with a
// synced write before it is observable in memory.
type WorkingDraftStore struct {
	mu          sync.Mutex
	file        *os.File
	lock        *os.File
	lockPath    string
	drafts      map[string]WorkingDraft
	requests    map[string]workingDraftRequestRecord
	conversions map[string]workingDraftConversionRecord
	seq         uint64
	now         func() time.Time
	unavailable bool
	closed      bool
}

// workingDraftDigest is the canonical operation identity: a length-prefixed
// digest of the operation kind, draft ID, request ID and exact payload bytes.
// The expected version is not part of it on purpose: an identical retry must
// replay its original result even after later versions exist.
func workingDraftDigest(operation, id, requestID string, payload []byte) string {
	var b bytes.Buffer
	for _, s := range []string{operation, id, requestID} {
		_ = binary.Write(&b, binary.BigEndian, uint64(len(s)))
		b.WriteString(s)
	}
	_ = binary.Write(&b, binary.BigEndian, uint64(len(payload)))
	b.Write(payload)
	return factoryDigest(b.Bytes())
}

// workingDraftContentBytes validates one editor input and returns its canonical
// encoding. Only the store's own limits apply here; the complete factory
// configuration is validated by conversion, never by saving.
func workingDraftContentBytes(c WorkingDraftContent) ([]byte, error) {
	if len(c.Plan)+len(c.Spec) > factoryImportLimit {
		return nil, fmt.Errorf("working draft plan and spec exceed 1 MiB")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	canonical, err := factoryCanonical(raw)
	if err != nil {
		return nil, fmt.Errorf("working draft content: %w", err)
	}
	return canonical, nil
}

func workingDraftConversionBytes(request json.RawMessage, settingsVersion string) []byte {
	b, _ := json.Marshal(struct {
		Request         json.RawMessage `json:"request"`
		SettingsVersion string          `json:"settingsVersion"`
	}{request, settingsVersion})
	return b
}

// OpenWorkingDraftStore opens or creates the store in dir. A missing directory
// is created private to the user; a missing journal is an empty store. A
// journal that fails its checksum or schema version is reported as unavailable
// rather than replaced.
func OpenWorkingDraftStore(dir string, now func() time.Time) (*WorkingDraftStore, error) {
	if now == nil {
		now = time.Now
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkingDraftUnavailable, e)
	}
	lockPath := filepath.Join(dir, workingDraftLockName)
	l, e := acquireWorkingDraftLock(lockPath)
	if e != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkingDraftUnavailable, e)
	}
	s := &WorkingDraftStore{lock: l, lockPath: lockPath, now: now, drafts: map[string]WorkingDraft{}, requests: map[string]workingDraftRequestRecord{}, conversions: map[string]workingDraftConversionRecord{}}
	fail := func(e error) (*WorkingDraftStore, error) {
		_ = s.Close()
		return nil, fmt.Errorf("%w: %v", ErrWorkingDraftUnavailable, e)
	}
	token := make([]byte, 32)
	if _, e = rand.Read(token); e != nil {
		return fail(e)
	}
	if _, e = io.WriteString(l, workingDraftLockContent(os.Getpid(), token)); e != nil {
		return fail(e)
	}
	if e = l.Sync(); e != nil {
		return fail(e)
	}
	s.file, e = os.OpenFile(filepath.Join(dir, workingDraftJournalName), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return fail(e)
	}
	if e = s.replay(); e != nil {
		return fail(e)
	}
	if _, e = s.file.Seek(0, io.SeekEnd); e != nil {
		return fail(e)
	}
	// Persist the journal directory entry before acknowledging any mutation.
	d, err := os.Open(dir)
	if err != nil {
		return fail(err)
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	return s, nil
}

// replay rebuilds memory from the journal. Records are read in bounded
// chunks, so a corrupt or hostile journal cannot allocate without limit.
func (s *WorkingDraftStore) replay() error {
	var line []byte
	buf := make([]byte, 32<<10)
	var offset int64
	for {
		n, e := s.file.Read(buf)
		for _, c := range buf[:n] {
			line = append(line, c)
			if len(line) > factoryRecordLimit {
				return errWorkingDraftCorrupt
			}
			if c != '\n' {
				continue
			}
			if e := s.replayRecord(line[:len(line)-1]); e != nil {
				return e
			}
			offset += int64(len(line))
			line = nil
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if len(line) > 0 {
		// An interrupted append: it was never acknowledged, so drop the fragment
		// rather than treating the whole journal as corrupt.
		if e := s.file.Truncate(offset); e != nil {
			return e
		}
		return s.file.Sync()
	}
	return nil
}

func (s *WorkingDraftStore) replayRecord(line []byte) error {
	var r workingDraftRecord
	var p workingDraftPayload
	if json.Unmarshal(line, &r) != nil || factoryDigest(r.Payload) != r.Checksum || json.Unmarshal(r.Payload, &p) != nil {
		return errWorkingDraftCorrupt
	}
	// Require the exact canonical encoding this store writes, so a record means
	// one thing only.
	canonical, e := json.Marshal(p)
	if e != nil || !bytes.Equal(canonical, r.Payload) || r.Schema != workingDraftJournalSchema || p.Sequence != s.seq+1 || p.RequestID == "" || p.Digest == "" {
		return errWorkingDraftCorrupt
	}
	if _, ok := s.requests[p.RequestID]; ok {
		return errWorkingDraftCorrupt
	}
	if e := s.apply(p); e != nil {
		return errWorkingDraftCorrupt
	}
	s.seq = p.Sequence
	return nil
}

// apply validates one record's transition and installs it, both on replay and
// after a durable append.
func (s *WorkingDraftStore) apply(p workingDraftPayload) error {
	d := p.Draft
	if d.ID == "" || d.ProjectRoot == "" {
		return errWorkingDraftCorrupt
	}
	if _, e := workingDraftContentBytes(d.Content); e != nil {
		return errWorkingDraftCorrupt
	}
	old, exists := s.drafts[d.ID]
	next := func(state string) bool {
		return exists && old.Version+1 == d.Version && d.State == state && old.ProjectRoot == d.ProjectRoot
	}
	switch p.Operation {
	case workingDraftOpCreate:
		if exists || d.Version != 1 || d.State != workingDraftEditing {
			return errWorkingDraftCorrupt
		}
	case workingDraftOpSave:
		if !next(workingDraftEditing) || old.State != workingDraftEditing {
			return errWorkingDraftCorrupt
		}
	case workingDraftOpDelete:
		if !next(workingDraftDeleted) || old.State == workingDraftDeleted || old.State == workingDraftConverting {
			return errWorkingDraftCorrupt
		}
	case workingDraftOpConvertBegin:
		if !next(workingDraftConverting) || old.State != workingDraftEditing || len(p.FactoryRequest) == 0 {
			return errWorkingDraftCorrupt
		}
	case workingDraftOpConvertDone:
		if !next(workingDraftConverted) || old.State != workingDraftConverting || d.FactoryItemID == "" {
			return errWorkingDraftCorrupt
		}
	case workingDraftOpConvertFail:
		if !next(workingDraftEditing) || old.State != workingDraftConverting || strings.TrimSpace(d.Error) == "" {
			return errWorkingDraftCorrupt
		}
	default:
		return errWorkingDraftCorrupt
	}
	s.drafts[d.ID] = d
	s.requests[p.RequestID] = workingDraftRequestRecord{DraftID: d.ID, Digest: p.Digest, Draft: d}
	if p.Operation == workingDraftOpConvertBegin {
		s.conversions[d.ID] = workingDraftConversionRecord{RequestID: p.ConversionRequestID, Request: p.FactoryRequest, SettingsVersion: p.SettingsVersion, Sequence: p.Sequence}
	} else if p.Operation == workingDraftOpConvertDone || p.Operation == workingDraftOpConvertFail {
		// A conversion record lives only as long as the draft is reserved.
		// Once the draft reached an item, or was returned to editing, nothing
		// may replay it, and keeping every frozen request alive would grow the
		// store without bound in a long-lived bridge.
		delete(s.conversions, d.ID)
	}
	return nil
}

// Close is idempotent. It releases the journal and removes the ownership lock
// only if it is still the one this store created.
func (s *WorkingDraftStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var e error
	if s.file != nil {
		e = s.file.Close()
	}
	if s.lock != nil {
		a, ae := s.lock.Stat()
		b, be := os.Stat(s.lockPath)
		if ae == nil && be == nil && os.SameFile(a, b) {
			if x := os.Remove(s.lockPath); e == nil {
				e = x
			}
		}
		if x := s.lock.Close(); e == nil {
			e = x
		}
	}
	return e
}

func (s *WorkingDraftStore) writable() error {
	if s.closed || s.unavailable {
		return ErrWorkingDraftUnavailable
	}
	return nil
}

// readable still serves memory after a failed write, so a caller can show the
// last durably stored state; a closed store serves nothing.
func (s *WorkingDraftStore) readable() error {
	if s.closed {
		return ErrWorkingDraftUnavailable
	}
	return nil
}

// replayRequest resolves an operation identity before any version comparison.
// An identical retry returns the original result; a reused request ID with a
// different digest conflicts.
func (s *WorkingDraftStore) replayRequest(requestID, id, digest string) (WorkingDraft, bool, error) {
	p, ok := s.requests[requestID]
	if !ok {
		return WorkingDraft{}, false, nil
	}
	if p.Digest != digest || (id != "" && p.DraftID != id) {
		return WorkingDraft{}, true, fmt.Errorf("%w: request id reused with a different payload", ErrWorkingDraftConflict)
	}
	return p.Draft, true, nil
}

// Get returns the draft including a deleted tombstone, so a device can compare
// its own copy with the recorded deletion. Project authorization happens at the
// service boundary.
func (s *WorkingDraftStore) Get(id string) (WorkingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.readable(); e != nil {
		return WorkingDraft{}, e
	}
	d, ok := s.drafts[id]
	if !ok {
		return WorkingDraft{}, ErrWorkingDraftNotFound
	}
	return d, nil
}

// List returns the non-deleted drafts of exactly one project root.
func (s *WorkingDraftStore) List(projectRoot string) ([]WorkingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.readable(); e != nil {
		return nil, e
	}
	out := make([]WorkingDraft, 0, len(s.drafts))
	for _, d := range s.drafts {
		if d.State == workingDraftDeleted || d.ProjectRoot != projectRoot {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].UpdatedAt.Equal(out[b].UpdatedAt) {
			return out[a].UpdatedAt.After(out[b].UpdatedAt)
		}
		return out[a].ID < out[b].ID
	})
	return out, nil
}

// acquireWorkingDraftLock takes the store's ownership lock.
//
// A lock left behind by a process that no longer exists is reclaimed. Recovering
// what a crash interrupted is this store's reason to exist, and leaving that
// recovery to somebody remembering to delete a file in the state directory
// would silently defeat it. A lock whose owner cannot be shown dead is refused,
// never taken.
func acquireWorkingDraftLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		return f, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}
	if !workingDraftLockStale(path) {
		return nil, errWorkingDraftOwned
	}
	if err := os.Remove(path); err != nil {
		return nil, errWorkingDraftOwned
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		// Another opener won the reclaim: it owns the store now.
		if os.IsExist(err) {
			return nil, errWorkingDraftOwned
		}
		return nil, err
	}
	return f, nil
}

// workingDraftLockStale reports whether a lock file names an owner that is
// provably gone. Only a complete lock this version wrote is considered: an
// opaque token left by an older bridge, a partially written file, or anything
// unreadable stays owned, because a live store's lock must never be taken by
// mistake.
func workingDraftLockStale(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.HasSuffix(raw, []byte("\n")) {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], workingDraftLockPIDPrefix) || len(lines[1]) != workingDraftLockTokenLength {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(lines[0], workingDraftLockPIDPrefix))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		// Our own process holding the lock means this store is open here.
		return false
	}
	return !workingDraftOwnerAlive(pid)
}

// workingDraftLockContent is what the ownership lock records: the writing
// process, so a later opener can tell an abandoned lock from a live one, and a
// random token, so the file is unique.
func workingDraftLockContent(pid int, token []byte) string {
	return fmt.Sprintf("%s%d\n%s\n", workingDraftLockPIDPrefix, pid, hex.EncodeToString(token))
}

// Create stores a new editing draft for one project. Project root is opaque
// here: registering and authorizing it is the service boundary's job.
func (s *WorkingDraftStore) Create(projectRoot, requestID string, content WorkingDraftContent) (WorkingDraft, error) {
	d, _, err := s.create(projectRoot, requestID, content)
	return d, err
}

// create reports whether the request ID was already accepted, so an HTTP caller
// can tell a fresh draft (201) from a replay (200) without peeking at the store
// first: the answer is decided under the same lock as the append.
func (s *WorkingDraftStore) create(projectRoot, requestID string, content WorkingDraftContent) (WorkingDraft, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return WorkingDraft{}, false, e
	}
	if strings.TrimSpace(requestID) == "" {
		return WorkingDraft{}, false, fmt.Errorf("requestId required")
	}
	if strings.TrimSpace(projectRoot) == "" {
		return WorkingDraft{}, false, fmt.Errorf("project root required")
	}
	if _, e := workingDraftContentBytes(content); e != nil {
		return WorkingDraft{}, false, e
	}
	// The project is part of the create payload: the same request ID cannot
	// replay a draft into a different project.
	identity, e := json.Marshal(struct {
		ProjectRoot string              `json:"projectRoot"`
		Content     WorkingDraftContent `json:"content"`
	}{projectRoot, content})
	if e != nil {
		return WorkingDraft{}, false, e
	}
	digest := workingDraftDigest(workingDraftOpCreate, "", requestID, identity)
	if d, ok, e := s.replayRequest(requestID, "", digest); ok {
		return d, true, e
	}
	idBytes := make([]byte, 16)
	if _, e = rand.Read(idBytes); e != nil {
		return WorkingDraft{}, false, e
	}
	d := WorkingDraft{ID: hex.EncodeToString(idBytes), ProjectRoot: projectRoot, Version: 1, State: workingDraftEditing, Content: content, UpdatedAt: s.now().UTC()}
	stored, e := s.append(workingDraftPayload{Operation: workingDraftOpCreate, RequestID: requestID, Digest: digest, Draft: d})
	return stored, false, e
}

// Save conditionally replaces the content of one editing draft.
func (s *WorkingDraftStore) Save(id, requestID string, expectedVersion uint64, content WorkingDraftContent) (WorkingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return WorkingDraft{}, e
	}
	if strings.TrimSpace(requestID) == "" {
		return WorkingDraft{}, fmt.Errorf("requestId required")
	}
	raw, e := workingDraftContentBytes(content)
	if e != nil {
		return WorkingDraft{}, e
	}
	digest := workingDraftDigest(workingDraftOpSave, id, requestID, raw)
	if d, ok, e := s.replayRequest(requestID, id, digest); ok {
		return d, e
	}
	old, ok := s.drafts[id]
	if !ok {
		return WorkingDraft{}, ErrWorkingDraftNotFound
	}
	if old.State != workingDraftEditing {
		return WorkingDraft{}, fmt.Errorf("%w: draft is %s", ErrWorkingDraftConflict, old.State)
	}
	if old.Version != expectedVersion {
		return WorkingDraft{}, fmt.Errorf("%w: expected version %d, have %d", ErrWorkingDraftConflict, expectedVersion, old.Version)
	}
	d := old
	d.Version = old.Version + 1
	d.Content = content
	// A stale conversion error described content that is now superseded.
	d.Error = ""
	d.UpdatedAt = s.now().UTC()
	return s.append(workingDraftPayload{Operation: workingDraftOpSave, RequestID: requestID, Digest: digest, Draft: d})
}

// Delete records a tombstone. It never deletes the journal history, and a
// deleted draft is never resurrected.
func (s *WorkingDraftStore) Delete(id, requestID string, expectedVersion uint64) (WorkingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return WorkingDraft{}, e
	}
	if strings.TrimSpace(requestID) == "" {
		return WorkingDraft{}, fmt.Errorf("requestId required")
	}
	digest := workingDraftDigest(workingDraftOpDelete, id, requestID, nil)
	if d, ok, e := s.replayRequest(requestID, id, digest); ok {
		return d, e
	}
	old, ok := s.drafts[id]
	if !ok {
		return WorkingDraft{}, ErrWorkingDraftNotFound
	}
	if old.State == workingDraftDeleted {
		return WorkingDraft{}, fmt.Errorf("%w: draft is already deleted", ErrWorkingDraftConflict)
	}
	if old.State == workingDraftConverting {
		return WorkingDraft{}, fmt.Errorf("%w: draft conversion is in flight", ErrWorkingDraftConflict)
	}
	if old.Version != expectedVersion {
		return WorkingDraft{}, fmt.Errorf("%w: expected version %d, have %d", ErrWorkingDraftConflict, expectedVersion, old.Version)
	}
	d := old
	d.Version = old.Version + 1
	d.State = workingDraftDeleted
	d.UpdatedAt = s.now().UTC()
	return s.append(workingDraftPayload{Operation: workingDraftOpDelete, RequestID: requestID, Digest: digest, Draft: d})
}

// BeginConversion durably reserves exactly one frozen factory request for the
// draft's current version before any factory call is made. The request is kept
// in the journal's internal record only; WorkingDraft never exposes it.
func (s *WorkingDraftStore) BeginConversion(id, requestID string, expectedVersion uint64, request FactoryCreate, settingsVersion string) (WorkingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return WorkingDraft{}, e
	}
	if strings.TrimSpace(requestID) == "" {
		return WorkingDraft{}, fmt.Errorf("requestId required")
	}
	raw, e := json.Marshal(request)
	if e != nil {
		return WorkingDraft{}, e
	}
	frozen, e := factoryCanonical(raw)
	if e != nil {
		return WorkingDraft{}, fmt.Errorf("frozen factory request: %w", e)
	}
	digest := workingDraftDigest(workingDraftOpConvertBegin, id, requestID, workingDraftConversionBytes(frozen, settingsVersion))
	if d, ok, e := s.replayRequest(requestID, id, digest); ok {
		return d, e
	}
	old, ok := s.drafts[id]
	if !ok {
		return WorkingDraft{}, ErrWorkingDraftNotFound
	}
	if old.State != workingDraftEditing {
		return WorkingDraft{}, fmt.Errorf("%w: draft is %s", ErrWorkingDraftConflict, old.State)
	}
	if old.Version != expectedVersion {
		return WorkingDraft{}, fmt.Errorf("%w: expected version %d, have %d", ErrWorkingDraftConflict, expectedVersion, old.Version)
	}
	d := old
	d.Version = old.Version + 1
	d.State = workingDraftConverting
	d.Error = ""
	d.UpdatedAt = s.now().UTC()
	return s.append(workingDraftPayload{Operation: workingDraftOpConvertBegin, RequestID: requestID, Digest: digest, Draft: d, FactoryRequest: frozen, SettingsVersion: settingsVersion, ConversionRequestID: requestID})
}

// CompleteConversion durably associates the factory item a reserved conversion
// produced. Only this makes a draft converted.
func (s *WorkingDraftStore) CompleteConversion(id, requestID, itemID string) (WorkingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return WorkingDraft{}, e
	}
	if strings.TrimSpace(requestID) == "" {
		return WorkingDraft{}, fmt.Errorf("requestId required")
	}
	if strings.TrimSpace(itemID) == "" {
		return WorkingDraft{}, fmt.Errorf("factory item required")
	}
	digest := workingDraftDigest(workingDraftOpConvertDone, id, requestID, []byte(itemID))
	if d, ok, e := s.replayRequest(requestID, id, digest); ok {
		return d, e
	}
	old, ok := s.drafts[id]
	if !ok {
		return WorkingDraft{}, ErrWorkingDraftNotFound
	}
	if old.State != workingDraftConverting || old.FactoryItemID != "" {
		return WorkingDraft{}, fmt.Errorf("%w: draft is %s", ErrWorkingDraftConflict, old.State)
	}
	d := old
	d.Version = old.Version + 1
	d.State = workingDraftConverted
	d.FactoryItemID = itemID
	d.UpdatedAt = s.now().UTC()
	return s.append(workingDraftPayload{Operation: workingDraftOpConvertDone, RequestID: requestID, Digest: digest, Draft: d})
}

// FailConversion returns a reserved conversion to editing with the reason the
// factory definitely refused it. It is only reachable from converting: a
// draft that is not mid-conversion has nothing to fail, and a draft this call
// already failed replays its original result rather than failing twice.
func (s *WorkingDraftStore) FailConversion(id, requestID, message string) (WorkingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.writable(); e != nil {
		return WorkingDraft{}, e
	}
	if strings.TrimSpace(requestID) == "" {
		return WorkingDraft{}, fmt.Errorf("requestId required")
	}
	if strings.TrimSpace(message) == "" {
		// The record must carry a reason: an empty one would be
		// indistinguishable from a draft that never left editing.
		message = "conversion failed"
	}
	digest := workingDraftDigest(workingDraftOpConvertFail, id, requestID, []byte(message))
	if d, ok, e := s.replayRequest(requestID, id, digest); ok {
		return d, e
	}
	old, ok := s.drafts[id]
	if !ok {
		return WorkingDraft{}, ErrWorkingDraftNotFound
	}
	if old.State != workingDraftConverting {
		return WorkingDraft{}, fmt.Errorf("%w: draft is %s", ErrWorkingDraftConflict, old.State)
	}
	d := old
	d.Version = old.Version + 1
	d.State = workingDraftEditing
	d.Error = message
	d.UpdatedAt = s.now().UTC()
	return s.append(workingDraftPayload{Operation: workingDraftOpConvertFail, RequestID: requestID, Digest: digest, Draft: d})
}

// workingDraftConvertingDraft is one draft a crash left mid-conversion,
// together with the frozen request and settings version its reservation
// recorded. The frozen bytes stay package-private: WorkingDraft never exposes
// them.
type workingDraftConvertingDraft struct {
	ID              string
	RequestID       string
	Request         json.RawMessage
	SettingsVersion string
	Sequence        uint64
}

// convertingDrafts returns every draft whose conversion is reserved but not
// associated, newest reservation first, so startup reconciliation can finish
// them in the order they were started.
func (s *WorkingDraftStore) convertingDrafts() ([]workingDraftConvertingDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.readable(); e != nil {
		return nil, e
	}
	var out []workingDraftConvertingDraft
	for id, d := range s.drafts {
		if d.State != workingDraftConverting {
			continue
		}
		r, ok := s.conversions[id]
		if !ok || len(r.Request) == 0 {
			// A converting draft always has a reservation; without one there is
			// nothing to replay, so it is left for a later reconciliation
			// rather than guessed at.
			continue
		}
		out = append(out, workingDraftConvertingDraft{ID: id, RequestID: r.RequestID, Request: r.Request, SettingsVersion: r.SettingsVersion, Sequence: r.Sequence})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Sequence > out[b].Sequence })
	return out, nil
}

// conversionRecord returns the conversion the store froze for a draft, which is
// what its reservation recorded or replayed.
func (s *WorkingDraftStore) conversionRecord(id string) (workingDraftConversionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readable() != nil {
		return workingDraftConversionRecord{}, false
	}
	r, ok := s.conversions[id]
	return r, ok
}

// append durably writes one record and only then makes it observable. A
// failed write or sync leaves the previous state in memory and marks the store
// unavailable, so a caller is never told a mutation was saved when it was not.
func (s *WorkingDraftStore) append(p workingDraftPayload) (WorkingDraft, error) {
	p.Sequence = s.seq + 1
	payload, e := json.Marshal(p)
	if e != nil {
		return WorkingDraft{}, e
	}
	line, e := json.Marshal(workingDraftRecord{workingDraftJournalSchema, payload, factoryDigest(payload)})
	if e != nil {
		return WorkingDraft{}, e
	}
	line = append(line, '\n')
	if len(line) > factoryRecordLimit {
		return WorkingDraft{}, fmt.Errorf("working draft journal record exceeds 16 MiB")
	}
	n, e := s.file.Write(line)
	if e == nil && n != len(line) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = s.file.Sync()
	}
	if e != nil {
		s.unavailable = true
		return WorkingDraft{}, fmt.Errorf("%w: %v", ErrWorkingDraftUnavailable, e)
	}
	if e = s.apply(p); e != nil {
		s.unavailable = true
		return WorkingDraft{}, fmt.Errorf("%w: %v", ErrWorkingDraftUnavailable, e)
	}
	s.seq = p.Sequence
	return p.Draft, nil
}
