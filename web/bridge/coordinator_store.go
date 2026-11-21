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
	// newer build is refused rather than guessed at. Version 2 added
	// assignments and their dispatch identity; version 3 added the delivery a
	// task posts back to its source thread. Each addition is additive: an older
	// document loads with none of what it predates.
	coordinatorVersion = 3
	// coordinatorDocumentLimit bounds the document this build will read, so a
	// corrupt or hostile file cannot allocate without limit. It is also the
	// bound a mutation is refused at (see persistLocked): a document this build
	// could not read back is never written, so a full store keeps serving what
	// it has instead of turning every route into a 503 on the next start.
	coordinatorDocumentLimit = 8 << 20
	// Per-field bounds on what a caller can store. Every mutation rewrites the
	// whole document, so one unbounded title or instruction would make each
	// write cost whatever the caller chose to send.
	coordinatorTitleLimit       = 200
	coordinatorInstructionLimit = 16 << 10
	coordinatorOutcomeLimit     = 4 << 10
	coordinatorFindingsLimit    = 32 << 10
	// coordinatorRequestIDLimit bounds a create's idempotency key, as
	// validSpawnRequestID bounds the spawn path's: it is stored, compared and
	// echoed in a refusal, so it is short, printable and space-free, and a
	// hostile caller cannot grow the store with the key alone.
	coordinatorRequestIDLimit = 128
	coordinatorFileMode       = 0o600
	coordinatorDirMode        = 0o700
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
	// ErrCoordinatorTooLarge is a mutation refused because the document it
	// would write has outgrown what this build reads back. Nothing is written,
	// so the store keeps answering with what it already holds.
	ErrCoordinatorTooLarge = errors.New("bridge: coordinator store is full")
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

// coordinatorAssignmentRecord is one assignment as the store holds it.
//
// The assignment's own ID is its dispatch identity: it is minted and written
// here before the dispatch has any side effect, and the pending submission, the
// spawn claim and the worker's agent record all carry it. That is what makes a
// retry, a confirmation, a reconnect and a daemon restart resolve to one
// outcome instead of a second worker.
type coordinatorAssignmentRecord struct {
	Assignment contract.CoordinatorAssignment `json:"assignment"`
	// RequestID and Digest are the dispatch request that minted this assignment.
	// A request id replayed with the same digest is the same dispatch; with a
	// different one it is a conflict, because the id would then mean two things.
	RequestID string `json:"requestId,omitempty"`
	Digest    string `json:"digest,omitempty"`
	// Prompted records that the assignment's instruction was handed to its
	// target. It is written before the prompt is dispatched, so a resumed
	// dispatch never sends the same instruction twice.
	Prompted bool `json:"prompted,omitempty"`
	// Steer records that the dispatch asked for a linked thread to take on the
	// instruction, which only a person's verified direction admits. It is
	// persisted so a resumed dispatch completes the delivery the person already
	// directed instead of losing it.
	Steer bool `json:"steer,omitempty"`
	// IntakeExpiresAt is when a pending submission lapses. A submission that
	// disappears before then was withdrawn or denied, which is a different
	// outcome from one that ran out of time.
	IntakeExpiresAt time.Time `json:"intakeExpiresAt,omitempty"`
	// ObservedDigest is the shape of the newest observed result. ResultRevision
	// advances only when this changes.
	ObservedDigest string `json:"observedDigest,omitempty"`
}

// coordinatorDispatchRecord is one dispatch request's idempotency record: its
// content digest and the assignments it minted, in request order.
type coordinatorDispatchRecord struct {
	TaskID        string   `json:"taskId"`
	RequestID     string   `json:"requestId"`
	Digest        string   `json:"digest"`
	AssignmentIDs []string `json:"assignmentIds"`
}

// coordinatorDocument is the store's file.
type coordinatorDocument struct {
	Version     int                           `json:"version"`
	Tasks       []coordinatorRecord           `json:"tasks,omitempty"`
	Assignments []coordinatorAssignmentRecord `json:"assignments,omitempty"`
	Dispatches  []coordinatorDispatchRecord   `json:"dispatches,omitempty"`
	// Deliveries holds one record per task: the newest informational update it
	// posted to its source thread. The event ID on it is stable for the task, so
	// a retry or a reconnect updates this record instead of appending a second
	// summary.
	Deliveries []contract.CoordinatorDelivery `json:"deliveries,omitempty"`
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
	// assignments holds every assignment by the ID that is also its dispatch
	// identity, and dispatches holds one record per task-scoped dispatch request
	// so a retried dispatch never mints its assignments twice.
	assignments map[string]coordinatorAssignmentRecord
	dispatches  map[string]coordinatorDispatchRecord
	// deliveries holds one record per task: the newest informational update the
	// task posted to its source thread.
	deliveries map[string]contract.CoordinatorDelivery
	closed     bool
}

// openCoordinatorStore opens or creates the store in dir. A missing directory
// and a missing document are an empty store; a document this build cannot read
// is unavailable at every later call, never silently replaced.
func openCoordinatorStore(dir string, now func() time.Time) (*coordinatorStore, error) {
	if now == nil {
		now = time.Now
	}
	s := &coordinatorStore{
		dir:         dir,
		path:        filepath.Join(dir, coordinatorTasksName),
		now:         now,
		tasks:       map[string]coordinatorRecord{},
		requests:    map[string]string{},
		assignments: map[string]coordinatorAssignmentRecord{},
		dispatches:  map[string]coordinatorDispatchRecord{},
		deliveries:  map[string]contract.CoordinatorDelivery{},
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
	// Assignments and their dispatch records are additive: a version 1 document
	// holds none, which is an empty assignment list rather than an error.
	for _, rec := range doc.Assignments {
		if rec.Assignment.ID == "" || rec.Assignment.TaskID == "" {
			return nil, fmt.Errorf("%w: coordinator assignment with no identity", ErrCoordinatorUnavailable)
		}
		if _, dup := s.assignments[rec.Assignment.ID]; dup {
			return nil, fmt.Errorf("%w: coordinator assignment %s appears twice", ErrCoordinatorUnavailable, rec.Assignment.ID)
		}
		s.assignments[rec.Assignment.ID] = rec
	}
	for _, rec := range doc.Dispatches {
		key := coordinatorDispatchKey(rec.TaskID, rec.RequestID)
		if rec.TaskID == "" || rec.RequestID == "" {
			return nil, fmt.Errorf("%w: coordinator dispatch with no identity", ErrCoordinatorUnavailable)
		}
		if _, dup := s.dispatches[key]; dup {
			return nil, fmt.Errorf("%w: coordinator dispatch %s appears twice", ErrCoordinatorUnavailable, rec.RequestID)
		}
		s.dispatches[key] = rec
	}
	// Deliveries are additive too: a version 2 document holds none, which is a
	// task that has posted no result yet. One record per task: a document with
	// two for one task is corrupt, and keeping the last one silently would
	// answer a question about a record nobody wrote.
	for _, rec := range doc.Deliveries {
		if rec.TaskID == "" || rec.EventID == "" {
			return nil, fmt.Errorf("%w: coordinator delivery with no identity", ErrCoordinatorUnavailable)
		}
		if _, dup := s.deliveries[rec.TaskID]; dup {
			return nil, fmt.Errorf("%w: coordinator delivery for task %s appears twice", ErrCoordinatorUnavailable, rec.TaskID)
		}
		s.deliveries[rec.TaskID] = rec
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

// coordinatorDispatchKey is one task-scoped dispatch request's key. A dispatch
// request id is unique within its task: the same id in another task is another
// request.
func coordinatorDispatchKey(taskID, requestID string) string {
	return taskID + "\x00" + requestID
}

// coordinatorAssignmentID is one assignment's stable identity.
//
// It is derived from the task, the dispatch request and the target project, so
// the same dispatch always resolves to the same assignment however many times it
// is sent, and two targets are two assignments. The ID is also the assignment's
// dispatch identity: it is persisted before any side effect and carried by the
// pending submission, the spawn claim and the worker's agent record, so every
// retry, confirmation and restart resolves one outcome.
func coordinatorAssignmentID(taskID, requestID, projectID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{taskID, requestID, projectID}, "\x00")))
	return coordinatorAssignmentPrefix + hex.EncodeToString(sum[:12])
}

// coordinatorAssignmentPrefix marks an assignment ID. The ID also names the
// assignment in the intake queue (a pending submission's id), so the prefix keeps
// it apart from the ids the queue mints for itself.
const coordinatorAssignmentPrefix = "asg-"

// coordinatorDispatchDigest is the canonical identity of one dispatch's content.
// The request id is not part of it: that is the key, and this is what it must
// have been used for. Targets are digested in canonical project order so two
// requests naming the same targets in another order are the same dispatch.
func coordinatorDispatchDigest(targets []contract.CoordinatorAssignTarget) string {
	canonical := append([]contract.CoordinatorAssignTarget(nil), targets...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ProjectID < canonical[j].ProjectID })
	payload, _ := json.Marshal(struct {
		Targets []contract.CoordinatorAssignTarget `json:"targets"`
	}{canonical})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// canonicalCoordinatorTargets checks a dispatch's targets and returns them in
// request order with their project IDs canonicalised.
//
// Distinct targets are distinct canonical project IDs: a display name or a path
// names no project, a repeated project is one target, and a target that names
// both an instruction and a thread to link is ambiguous. Everything here is
// refused before anything is written.
func canonicalCoordinatorTargets(targets []contract.CoordinatorAssignTarget) ([]contract.CoordinatorAssignTarget, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: a dispatch names at least one target", ErrCoordinatorInput)
	}
	seen := make(map[string]bool, len(targets))
	out := make([]contract.CoordinatorAssignTarget, 0, len(targets))
	for _, target := range targets {
		projectID := strings.TrimSpace(target.ProjectID)
		if projectID == "" {
			return nil, fmt.Errorf("%w: a target names a project id", ErrCoordinatorInput)
		}
		if seen[projectID] {
			continue
		}
		seen[projectID] = true
		threadID := strings.TrimSpace(target.ThreadID)
		instruction := strings.TrimSpace(target.Instruction)
		switch {
		case threadID != "" && instruction == "":
			return nil, fmt.Errorf("%w: linking a thread needs the instruction it should be observed for", ErrCoordinatorInput)
		case threadID == "" && instruction == "":
			return nil, fmt.Errorf("%w: a target names an instruction", ErrCoordinatorInput)
		case len(instruction) > coordinatorInstructionLimit:
			return nil, fmt.Errorf("%w: a target's instruction is longer than %d characters", ErrCoordinatorInput, coordinatorInstructionLimit)
		}
		out = append(out, contract.CoordinatorAssignTarget{
			ProjectID:   projectID,
			Instruction: instruction,
			ThreadID:    threadID,
			Steer:       target.Steer && threadID != "",
		})
	}
	return out, nil
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

// validCoordinatorRequestID reports whether id is usable as a create's
// idempotency key: non-empty, printable, space-free and short. It is stored,
// compared and echoed in a conflict, so a hostile caller must not be able to
// grow the store with the key alone or put control characters into it.
func validCoordinatorRequestID(id string) bool {
	if id == "" || len(id) > coordinatorRequestIDLimit {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f || r > 0x7e {
			return false
		}
	}
	return true
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
	if !validCoordinatorRequestID(requestID) {
		return contract.CoordinatorTask{}, false, fmt.Errorf("%w: requestId is required, printable and at most %d characters", ErrCoordinatorInput, coordinatorRequestIDLimit)
	}
	if err := validCoordinatorSource(req.Source); err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	projects, err := canonicalCoordinatorProjects(req.Projects)
	if err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	title := strings.TrimSpace(req.Title)
	if len(title) > coordinatorTitleLimit {
		return contract.CoordinatorTask{}, false, fmt.Errorf("%w: title is longer than %d characters", ErrCoordinatorInput, coordinatorTitleLimit)
	}
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

// BeginDispatch records one dispatch request's assignment intents and returns
// them, reporting whether this call minted them.
//
// The intents are durable before the caller attempts any side effect, and the
// assignment IDs they carry are the dispatch identities every later step
// resolves: the pending submission, the spawn claim and the worker's record. A
// replayed request id answers the assignments the original request minted —
// never a second set — and the same id with different content is a conflict.
// Two racing identical dispatches cannot both mint, because both are decided
// under this one lock.
func (s *coordinatorStore) BeginDispatch(taskID, requestID, digest string, targets []contract.CoordinatorAssignTarget) ([]coordinatorAssignmentRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return nil, false, err
	}
	if _, ok := s.tasks[taskID]; !ok {
		return nil, false, ErrCoordinatorNotFound
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, false, fmt.Errorf("%w: requestId is required", ErrCoordinatorInput)
	}
	canonical, err := canonicalCoordinatorTargets(targets)
	if err != nil {
		return nil, false, err
	}
	if digest == "" {
		digest = coordinatorDispatchDigest(canonical)
	}
	key := coordinatorDispatchKey(taskID, requestID)
	if existing, ok := s.dispatches[key]; ok {
		if existing.Digest != digest {
			return nil, false, ErrCoordinatorConflict
		}
		out := make([]coordinatorAssignmentRecord, 0, len(existing.AssignmentIDs))
		for _, id := range existing.AssignmentIDs {
			rec, held := s.assignments[id]
			if !held {
				return nil, false, fmt.Errorf("%w: dispatch %s names no stored assignment", ErrCoordinatorUnavailable, requestID)
			}
			out = append(out, rec)
		}
		return out, false, nil
	}

	now := s.now().UTC()
	records := make([]coordinatorAssignmentRecord, 0, len(canonical))
	ids := make([]string, 0, len(canonical))
	for _, target := range canonical {
		relationship := contract.CoordinatorAssignmentWorker
		if target.ThreadID != "" {
			relationship = contract.CoordinatorAssignmentThread
		}
		rec := coordinatorAssignmentRecord{
			Assignment: contract.CoordinatorAssignment{
				ID:           coordinatorAssignmentID(taskID, requestID, target.ProjectID),
				TaskID:       taskID,
				ProjectID:    target.ProjectID,
				Instruction:  target.Instruction,
				Relationship: relationship,
				State:        contract.CoordinatorAssignmentDispatching,
				CreatedAt:    now,
				UpdatedAt:    now,
			},
			RequestID: requestID,
			Digest:    digest,
		}
		if target.ThreadID != "" {
			rec.Assignment.Thread = &contract.CoordinatorThreadRef{
				ProjectID: target.ProjectID, Kind: "agent", ID: target.ThreadID,
			}
			rec.Steer = target.Steer
		}
		records = append(records, rec)
		ids = append(ids, rec.Assignment.ID)
	}
	dispatch := coordinatorDispatchRecord{TaskID: taskID, RequestID: requestID, Digest: digest, AssignmentIDs: ids}
	// Write first, then publish: an intent that could not be made durable is
	// never reported as one that was.
	if err := s.persistLocked(func(doc *coordinatorDocument) bool {
		doc.Assignments = append(doc.Assignments, records...)
		doc.Dispatches = append(doc.Dispatches, dispatch)
		return true
	}); err != nil {
		return nil, false, err
	}
	for _, rec := range records {
		s.assignments[rec.Assignment.ID] = rec
	}
	s.dispatches[key] = dispatch
	return records, true, nil
}

// Assignments returns one task's assignments, oldest first. An empty list is a
// valid answer: the task has dispatched nothing.
func (s *coordinatorStore) Assignments(taskID string) ([]coordinatorAssignmentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return nil, ErrCoordinatorUnavailable
	}
	out := make([]coordinatorAssignmentRecord, 0)
	for _, rec := range s.assignments {
		if rec.Assignment.TaskID == taskID {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Assignment, out[j].Assignment
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return out, nil
}

// Assignment returns one assignment, by the ID that is also its dispatch
// identity.
func (s *coordinatorStore) Assignment(taskID, assignmentID string) (coordinatorAssignmentRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return coordinatorAssignmentRecord{}, false, ErrCoordinatorUnavailable
	}
	rec, ok := s.assignments[assignmentID]
	if !ok || rec.Assignment.TaskID != taskID {
		return coordinatorAssignmentRecord{}, false, nil
	}
	return rec, true, nil
}

// DispatchRequest returns the dispatch one task-scoped request id already made.
//
// It is how a dispatch tells a retry from a fresh request before it records
// anything: a replay resumes what the original recorded, and a caller that gets
// no record here is the one that mints the assignments.
func (s *coordinatorStore) DispatchRequest(taskID, requestID string) (coordinatorDispatchRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return coordinatorDispatchRecord{}, false, ErrCoordinatorUnavailable
	}
	rec, ok := s.dispatches[coordinatorDispatchKey(taskID, strings.TrimSpace(requestID))]
	if !ok {
		return coordinatorDispatchRecord{}, false, nil
	}
	return rec, true, nil
}

// SourceTask returns the open task whose source is the given thread.
//
// It is how a handoff from a thread finds the task that thread already handed
// work to: the source is linked once, so a second handoff from the same thread
// joins the existing task instead of opening another. A stopped task is not
// returned — its identity is spent, and resuming it is the stop scope's
// decision (task 8).
func (s *coordinatorStore) SourceTask(source contract.CoordinatorThreadRef) (contract.CoordinatorTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return contract.CoordinatorTask{}, false
	}
	for _, rec := range s.tasks {
		if rec.Task.Source == nil {
			continue
		}
		if *rec.Task.Source != source {
			continue
		}
		if rec.Task.Status != contract.CoordinatorTaskOpen {
			continue
		}
		return rec.Task, true
	}
	return contract.CoordinatorTask{}, false
}

// WidenScope adds projects to one task's read scope.
//
// A handoff that joins an existing source task may name a project the task was
// not opened over. Widening the scope is what lets it dispatch there under the
// same task identity, and it never narrows: a project the task already covered
// stays covered.
func (s *coordinatorStore) WidenScope(taskID string, projects []string) (contract.CoordinatorTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return contract.CoordinatorTask{}, err
	}
	rec, ok := s.tasks[taskID]
	if !ok {
		return contract.CoordinatorTask{}, ErrCoordinatorNotFound
	}
	merged, err := canonicalCoordinatorProjects(append(append([]string(nil), rec.Task.Projects...), projects...))
	if err != nil {
		return contract.CoordinatorTask{}, err
	}
	if len(merged) == len(rec.Task.Projects) {
		return rec.Task, nil
	}
	rec.Task.Projects = merged
	rec.Task.Revision++
	rec.Task.UpdatedAt = s.now().UTC()
	if err := s.persistLocked(func(doc *coordinatorDocument) bool {
		for n := range doc.Tasks {
			if doc.Tasks[n].Task.ID == taskID {
				doc.Tasks[n] = rec
				return true
			}
		}
		return false
	}); err != nil {
		return contract.CoordinatorTask{}, err
	}
	s.tasks[taskID] = rec
	return rec.Task, nil
}

// PutDelivery records the informational update one task posts to its source
// thread, and reports whether it changed anything.
//
// The event ID is the task's, and stable across attempts, so a retry, a
// reconnect or a second observation replaces this one record instead of
// posting a second summary. It reports changed=false when the record already
// holds the same text, which is what keeps a delivery that learned nothing new
// from being emitted again.
func (s *coordinatorStore) PutDelivery(delivery contract.CoordinatorDelivery) (contract.CoordinatorDelivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return contract.CoordinatorDelivery{}, false, err
	}
	if delivery.TaskID == "" || delivery.EventID == "" {
		return contract.CoordinatorDelivery{}, false, fmt.Errorf("%w: a delivery needs a task and an event id", ErrCoordinatorInput)
	}
	previous, held := s.deliveries[delivery.TaskID]
	if held && previous.EventID == delivery.EventID && previous.Text == delivery.Text {
		return previous, false, nil
	}
	delivery.At = s.now().UTC()
	if err := s.persistLocked(func(doc *coordinatorDocument) bool {
		for n := range doc.Deliveries {
			if doc.Deliveries[n].TaskID == delivery.TaskID {
				doc.Deliveries[n] = delivery
				return true
			}
		}
		doc.Deliveries = append(doc.Deliveries, delivery)
		return true
	}); err != nil {
		return previous, false, err
	}
	s.deliveries[delivery.TaskID] = delivery
	return delivery, true, nil
}

// Delivery returns the newest informational update one task posted to its
// source, if it posted one.
func (s *coordinatorStore) Delivery(taskID string) (contract.CoordinatorDelivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.closed {
		return contract.CoordinatorDelivery{}, false, ErrCoordinatorUnavailable
	}
	rec, ok := s.deliveries[taskID]
	return rec, ok, nil
}

// SetTaskStatus writes one task's status and reports whether it changed.
//
// It is the durable half of a task stop (agent-first Studio, task 8): the
// stopping record is written before anything is cancelled, so a bridge that dies
// mid-stop resumes from the record rather than from a call that may never have
// been sent. It reports changed=false when the status already stands, which is
// what keeps a repeat after a reconnect — or a second press — from looking like
// progress.
func (s *coordinatorStore) SetTaskStatus(taskID string, status contract.CoordinatorTaskStatus) (contract.CoordinatorTask, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	rec, ok := s.tasks[taskID]
	if !ok {
		return contract.CoordinatorTask{}, false, ErrCoordinatorNotFound
	}
	if rec.Task.Status == status {
		return rec.Task, false, nil
	}
	rec.Task.Status = status
	rec.Task.Revision++
	rec.Task.UpdatedAt = s.now().UTC()
	if err := s.persistLocked(func(doc *coordinatorDocument) bool {
		for n := range doc.Tasks {
			if doc.Tasks[n].Task.ID == taskID {
				doc.Tasks[n] = rec
				return true
			}
		}
		return false
	}); err != nil {
		return contract.CoordinatorTask{}, false, err
	}
	s.tasks[taskID] = rec
	return rec.Task, true, nil
}

// UpdateAssignment applies mutate to one assignment and writes it atomically.
//
// mutate reports whether it changed anything; an unchanged assignment is not
// rewritten, so a read that observes nothing new costs no disk write. The
// returned record is what the store now holds.
func (s *coordinatorStore) UpdateAssignment(taskID, assignmentID string, mutate func(*coordinatorAssignmentRecord) bool) (coordinatorAssignmentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writable(); err != nil {
		return coordinatorAssignmentRecord{}, err
	}
	rec, ok := s.assignments[assignmentID]
	if !ok || rec.Assignment.TaskID != taskID {
		return coordinatorAssignmentRecord{}, ErrCoordinatorNotFound
	}
	previous := rec
	if mutate != nil && !mutate(&rec) {
		return previous, nil
	}
	rec.Assignment.UpdatedAt = s.now().UTC()
	if err := s.persistLocked(func(doc *coordinatorDocument) bool {
		for n := range doc.Assignments {
			if doc.Assignments[n].Assignment.ID == assignmentID {
				doc.Assignments[n] = rec
				return true
			}
		}
		doc.Assignments = append(doc.Assignments, rec)
		return true
	}); err != nil {
		return previous, err
	}
	s.assignments[assignmentID] = rec
	return rec, nil
}

// persistLocked writes the document with mutate applied, atomically.
func (s *coordinatorStore) persistLocked(mutate func(doc *coordinatorDocument) bool) error {
	doc := coordinatorDocument{
		Version:     coordinatorVersion,
		Tasks:       make([]coordinatorRecord, 0, len(s.tasks)+1),
		Assignments: make([]coordinatorAssignmentRecord, 0, len(s.assignments)+1),
		Dispatches:  make([]coordinatorDispatchRecord, 0, len(s.dispatches)+1),
		Deliveries:  make([]contract.CoordinatorDelivery, 0, len(s.deliveries)+1),
	}
	for _, rec := range s.tasks {
		doc.Tasks = append(doc.Tasks, rec)
	}
	sort.Slice(doc.Tasks, func(i, j int) bool { return doc.Tasks[i].Task.ID < doc.Tasks[j].Task.ID })
	for _, rec := range s.assignments {
		doc.Assignments = append(doc.Assignments, rec)
	}
	sort.Slice(doc.Assignments, func(i, j int) bool {
		return doc.Assignments[i].Assignment.ID < doc.Assignments[j].Assignment.ID
	})
	for _, rec := range s.dispatches {
		doc.Dispatches = append(doc.Dispatches, rec)
	}
	sort.Slice(doc.Dispatches, func(i, j int) bool {
		return coordinatorDispatchKey(doc.Dispatches[i].TaskID, doc.Dispatches[i].RequestID) <
			coordinatorDispatchKey(doc.Dispatches[j].TaskID, doc.Dispatches[j].RequestID)
	})
	for _, rec := range s.deliveries {
		doc.Deliveries = append(doc.Deliveries, rec)
	}
	sort.Slice(doc.Deliveries, func(i, j int) bool { return doc.Deliveries[i].TaskID < doc.Deliveries[j].TaskID })
	if !mutate(&doc) {
		return nil
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%w: encode coordinator tasks: %v", ErrCoordinatorUnavailable, err)
	}
	if len(data) > coordinatorDocumentLimit {
		// Refuse the mutation rather than write a document this build could no
		// longer read: the store keeps serving what it has, where writing it
		// would turn every route — and every restart — into a 503.
		return ErrCoordinatorTooLarge
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
