package bridge

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// The coordinator store is the durable half of Marshal's own conversation: one
// record per coordinated task, holding the identity the daemon minted for it
// (task ID, coordinator session, working directory), the thread it was opened
// from and the projects it may read.
//
// It follows the planner store's shape — a private directory under the daemon's
// state directory, one JSON document, written atomically (a fsynced temp file
// renamed into place) so a bridge that dies mid-write leaves the previous
// document intact rather than a truncated one.
//
// A store written before coordinated tasks existed, or a missing file, loads as
// an empty store. A store this build cannot read is unavailable — never empty —
// because an empty store would answer "nothing needs attention" for a task that
// still may.
const (
	// coordinatorStateSubpath is where the store lives, under the daemon's
	// state directory.
	coordinatorStateSubpath = "coordinator/v1"
	// coordinatorWorkSubpath holds one daemon-managed working directory per
	// task, under the store's own directory.
	coordinatorWorkSubpath = "work"
	// coordinatorTasksName is the store's one document.
	coordinatorTasksName = "tasks.json"
	// coordinatorVersion is the document's format. A document written by a
	// newer build is refused rather than guessed at.
	coordinatorVersion = 1
	// coordinatorDocumentLimit bounds the document this build will read, so a
	// corrupt or hostile file cannot allocate without limit.
	coordinatorDocumentLimit = 8 << 20
	coordinatorFileMode      = 0o600
	coordinatorDirMode       = 0o700
)

// Coordinator store errors. Each maps onto one HTTP status in
// coordinator.go, and none of them is ever reported as "no tasks".
var (
	// ErrCoordinatorUnavailable is a store this bridge cannot read or write.
	ErrCoordinatorUnavailable = errors.New("bridge: coordinator store unavailable")
	// ErrCoordinatorNotFound is a task id the store does not hold.
	ErrCoordinatorNotFound = errors.New("bridge: unknown coordinator task")
	// ErrCoordinatorConflict is a request id reused for different content.
	ErrCoordinatorConflict = errors.New("bridge: coordinator request id reused with different content")
	// ErrCoordinatorInput is a request this store refuses before it writes:
	// a missing request id, or a scope that is not a canonical project list.
	ErrCoordinatorInput = errors.New("bridge: invalid coordinator request")
)

// coordinatorRecord is one task as the store holds it.
type coordinatorRecord struct {
	Task contract.CoordinatorTask `json:"task"`
	// WorkDir is the daemon-managed working directory the coordinator
	// conversation runs in. It is a host path, so it stays server-only: it is
	// written to this store's private file and never to the wire.
	WorkDir string `json:"workDir,omitempty"`
	// RequestID and Digest are the idempotency identity of the create that
	// minted this task. The digest is of the content, so a request id reused
	// for different content is a conflict rather than a replay.
	RequestID string `json:"requestId,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

// coordinatorDocument is the store's file.
type coordinatorDocument struct {
	Version int                 `json:"version"`
	Tasks   []coordinatorRecord `json:"tasks,omitempty"`
}

// coordinatorStore owns coordinated tasks on disk. Every mutation is serialized
// under one mutex and is durable before it is observable in memory.
type coordinatorStore struct {
	dir  string
	path string
	now  func() time.Time

	mu       sync.Mutex
	tasks    map[string]coordinatorRecord
	requests map[string]string // request id -> task id
	closed   bool
}

// openCoordinatorStore opens or creates the store in dir. A missing directory
// and a missing document are an empty store; a document this build cannot read
// is unavailable at every later call, never silently replaced.
func openCoordinatorStore(dir string, now func() time.Time) (*coordinatorStore, error) {
	if now == nil {
		now = time.Now
	}
	s := &coordinatorStore{
		dir:      dir,
		path:     filepath.Join(dir, coordinatorTasksName),
		now:      now,
		tasks:    map[string]coordinatorRecord{},
		requests: map[string]string{},
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read coordinator tasks: %v", ErrCoordinatorUnavailable, err)
	}
	if len(data) > coordinatorDocumentLimit {
		return nil, fmt.Errorf("%w: coordinator document exceeds %d bytes", ErrCoordinatorUnavailable, coordinatorDocumentLimit)
	}
	var doc coordinatorDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: decode coordinator tasks: %v", ErrCoordinatorUnavailable, err)
	}
	// A document with no version is one written before coordinated tasks
	// carried one: it holds nothing this build can act on, so it loads as an
	// empty store. A newer version is refused rather than guessed at.
	if doc.Version > coordinatorVersion {
		return nil, fmt.Errorf("%w: coordinator document version %d is newer than %d", ErrCoordinatorUnavailable, doc.Version, coordinatorVersion)
	}
	if doc.Version < 1 {
		return s, nil
	}
	for _, rec := range doc.Tasks {
		if rec.Task.ID == "" {
			return nil, fmt.Errorf("%w: coordinator record with no task id", ErrCoordinatorUnavailable)
		}
		if _, dup := s.tasks[rec.Task.ID]; dup {
			return nil, fmt.Errorf("%w: coordinator task %s appears twice", ErrCoordinatorUnavailable, rec.Task.ID)
		}
		if rec.RequestID != "" {
			if _, dup := s.requests[rec.RequestID]; dup {
				return nil, fmt.Errorf("%w: coordinator request %s appears twice", ErrCoordinatorUnavailable, rec.RequestID)
			}
			s.requests[rec.RequestID] = rec.Task.ID
		}
		s.tasks[rec.Task.ID] = rec
	}
	return s, nil
}

// Close is idempotent. The store holds no open file, so this only marks it
// unreadable; an already-durable task stays on disk.
func (s *coordinatorStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *coordinatorStore) writable() error {
	if s == nil || s.closed {
		return ErrCoordinatorUnavailable
	}
	return nil
}

// coordinatorDigest is the canonical identity of one create's content. The
// request id is not part of it: that is the key, and this is what it must have
// been used for.
func coordinatorDigest(title string, source *contract.CoordinatorThreadRef, projects []string) string {
	payload, _ := json.Marshal(struct {
		Title    string                         `json:"title"`
		Source   *contract.CoordinatorThreadRef `json:"source,omitempty"`
		Projects []string                       `json:"projects"`
	}{title, source, projects})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// canonicalCoordinatorProjects checks a scope and returns it in one canonical
// order, so the same scope always digests the same way. An empty scope is valid:
// it is the host-local one.
func canonicalCoordinatorProjects(projects []string) ([]string, error) {
	seen := make(map[string]bool, len(projects))
	out := make([]string, 0, len(projects))
	for _, id := range projects {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("%w: an empty project id", ErrCoordinatorInput)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// validCoordinatorSource checks an optional source reference.
func validCoordinatorSource(ref *contract.CoordinatorThreadRef) error {
	if ref == nil {
		return nil
	}
	if strings.TrimSpace(ref.ProjectID) == "" || strings.TrimSpace(ref.Kind) == "" || strings.TrimSpace(ref.ID) == "" {
		return fmt.Errorf("%w: a source thread names a project, a kind and an id", ErrCoordinatorInput)
	}
	return nil
}

// List returns every task, newest first. An empty list is a valid answer: no
// coordinator destination has been opened on this host yet.
func (s *coordinatorStore) List() ([]contract.CoordinatorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return nil, ErrCoordinatorUnavailable
	}
	out := make([]contract.CoordinatorTask, 0, len(s.tasks))
	for _, rec := range s.tasks {
		out = append(out, rec.Task)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get returns one task.
func (s *coordinatorStore) Get(id string) (contract.CoordinatorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return contract.CoordinatorTask{}, ErrCoordinatorUnavailable
	}
	rec, ok := s.tasks[id]
	if !ok {
		return contract.CoordinatorTask{}, ErrCoordinatorNotFound
	}
	return rec.Task, nil
}

// WorkDir returns the daemon-managed working directory of one task.
func (s *coordinatorStore) WorkDir(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return "", ErrCoordinatorUnavailable
	}
	rec, ok := s.tasks[id]
	if !ok {
		return "", ErrCoordinatorNotFound
	}
	return rec.WorkDir, nil
}

// BySession returns the task whose coordinator conversation is sessionID.
//
// It is how a tool call that presents only the session the daemon issued finds
// the task that session belongs to: the binding names a session, and this store
// is the only record of which task minted it. A session no task holds resolves
// to nothing rather than to a default task.
func (s *coordinatorStore) BySession(sessionID string) (contract.CoordinatorTask, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || sessionID == "" {
		return contract.CoordinatorTask{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return contract.CoordinatorTask{}, false
	}
	for _, rec := range s.tasks {
		if rec.Task.SessionID == sessionID {
			return rec.Task, true
		}
	}
	return contract.CoordinatorTask{}, false
}

// Create stores a new task, or replays the one an earlier identical request
// minted.
//
// The request id is the first-send idempotency key: the same id with the same
// content answers the original task and reports created=false, and the same id
// with different content is a conflict. Both are decided under the same lock as
// the write, so two racing submissions cannot both mint a task.
func (s *coordinatorStore) Create(req contract.CoordinatorCreateRequest) (contract.CoordinatorTask, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return contract.CoordinatorTask{}, false, fmt.Errorf("%w: requestId is required", ErrCoordinatorInput)
	}
	if err := validCoordinatorSource(req.Source); err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	projects, err := canonicalCoordinatorProjects(req.Projects)
	if err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	title := strings.TrimSpace(req.Title)
	digest := coordinatorDigest(title, req.Source, projects)
	if existing, ok := s.requests[requestID]; ok {
		rec, held := s.tasks[existing]
		if !held {
			return contract.CoordinatorTask{}, false, fmt.Errorf("%w: request %s names no stored task", ErrCoordinatorUnavailable, requestID)
		}
		if rec.Digest != digest {
			return contract.CoordinatorTask{}, false, ErrCoordinatorConflict
		}
		return rec.Task, false, nil
	}

	id, err := coordinatorRandomID()
	if err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	sessionID, err := coordinatorRandomID()
	if err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	workDir := filepath.Join(s.dir, coordinatorWorkSubpath, id)
	if err := os.MkdirAll(workDir, coordinatorDirMode); err != nil {
		return contract.CoordinatorTask{}, false, fmt.Errorf("%w: create coordinator working directory: %v", ErrCoordinatorUnavailable, err)
	}
	now := s.now().UTC()
	rec := coordinatorRecord{
		Task: contract.CoordinatorTask{
			ID:        id,
			SessionID: "coord-" + sessionID,
			Title:     title,
			Status:    contract.CoordinatorTaskOpen,
			Revision:  1,
			Source:    req.Source,
			Projects:  projects,
			CreatedAt: now,
			UpdatedAt: now,
		},
		WorkDir:   workDir,
		RequestID: requestID,
		Digest:    digest,
	}
	// Write first, then publish: a create that could not be made durable is
	// never reported as one that was.
	if err := s.persistLocked(func(doc *coordinatorDocument) bool {
		doc.Tasks = append(doc.Tasks, rec)
		return true
	}); err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	s.tasks[rec.Task.ID] = rec
	s.requests[requestID] = rec.Task.ID
	return rec.Task, true, nil
}

// persistLocked writes the document with mutate applied, atomically.
func (s *coordinatorStore) persistLocked(mutate func(doc *coordinatorDocument) bool) error {
	doc := coordinatorDocument{Version: coordinatorVersion, Tasks: make([]coordinatorRecord, 0, len(s.tasks)+1)}
	for _, rec := range s.tasks {
		doc.Tasks = append(doc.Tasks, rec)
	}
	sort.Slice(doc.Tasks, func(i, j int) bool { return doc.Tasks[i].Task.ID < doc.Tasks[j].Task.ID })
	if !mutate(&doc) {
		return nil
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%w: encode coordinator tasks: %v", ErrCoordinatorUnavailable, err)
	}
	if err := os.MkdirAll(s.dir, coordinatorDirMode); err != nil {
		return fmt.Errorf("%w: create coordinator directory: %v", ErrCoordinatorUnavailable, err)
	}
	return coordinatorWriteAtomic(s.path, data)
}

// coordinatorWriteAtomic writes data to path through a fsynced temp file in the
// same directory, then renames it and fsyncs the directory. A failure removes
// the temp file and leaves whatever was there before untouched.
func coordinatorWriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".coordinator-*")
	if err != nil {
		return fmt.Errorf("%w: create temp file: %v", ErrCoordinatorUnavailable, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", ErrCoordinatorUnavailable, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(coordinatorFileMode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: %v", ErrCoordinatorUnavailable, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: replace %s: %v", ErrCoordinatorUnavailable, filepath.Base(path), err)
	}
	// The rename is the visible step; syncing the directory is what makes it
	// survive a power loss.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// coordinatorRandomID mints 16 random bytes in lowercase hex: a task id, or the
// body of a coordinator session id. It is never derived from a caller's input.
func coordinatorRandomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("%w: generate id: %v", ErrCoordinatorUnavailable, err)
	}
	return hex.EncodeToString(b[:]), nil
}
