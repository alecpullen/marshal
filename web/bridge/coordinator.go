package bridge

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// The coordinator API is Marshal's own destination: one host-local conversation
// per coordinated task, its durable identity, and the read-only attention
// summary that conversation may ask for.
//
// A task belongs to exactly one daemon. The store lives under that daemon's
// state directory, the host it reports is the daemon's own identity, and the
// projects it may read are the opaque IDs the daemon minted. Nothing here trusts
// a caller with an identity: a task id is resolved in this host's store, a
// project id only resolves if this daemon registered it, and the read grant is
// resolved again on every attention read.
//
// The routes sit on the same /api mux as the rest of the Studio, so the
// operator's proxy is what authenticates the caller; a store that cannot be
// opened answers 503 rather than vanishing, because "no tasks" is an answer a
// client would believe.

// coordinatorFreshWindow is how old a source's newest evidence may be before its
// contribution to an attention read counts as stale. A stale source still
// reports its entries — an old ask is still an ask — but it can never produce an
// all-clear, because "nothing needs you" would then be a claim the evidence does
// not support.
const coordinatorFreshWindow = 5 * time.Minute

// coordinatorService is the bridge's coordinator API: the durable store and the
// reads over it. A service whose store could not be opened keeps its routes and
// answers unavailable.
type coordinatorService struct {
	fleet *Fleet
	store *coordinatorStore
	// openErr is why the store could not be opened, reported by every route so
	// a client sees an unavailable store rather than an empty one.
	openErr error
	now     func() time.Time
	// dispatchMu guards dispatchLocks. One lock per assignment serializes the
	// attempts to dispatch it, so a retry racing the original — or two identical
	// requests — cannot both run its side effects. Entries are kept for the
	// process lifetime: an assignment is a bounded, durable thing, and a lock
	// that was dropped while a waiter still held it would let a third attempt
	// run beside the second.
	dispatchMu    sync.Mutex
	dispatchLocks map[string]*sync.Mutex
}

// lockAssignment serializes one assignment's dispatch attempts and returns the
// release.
func (svc *coordinatorService) lockAssignment(assignmentID string) func() {
	return svc.lock("assignment\x00" + assignmentID)
}

// lockTask serializes one task's stop attempts and returns the release.
//
// A stop is one operation per task however many times it is asked for: a repeat
// after a reconnect — or a second press — waits for the one in flight and then
// reads the state it settled, rather than cancelling the same workers beside it.
// It is a distinct key space from the assignment locks, so a stop waiting on an
// assignment's dispatch and a dispatch waiting on its own lock can never be the
// same waiter.
func (svc *coordinatorService) lockTask(taskID string) func() {
	return svc.lock("task\x00" + taskID)
}

// lock returns the release of the mutex named by a key, creating it on first
// ask. Entries are kept for the process lifetime: a lock dropped while a waiter
// still held it would let a third attempt run beside the second.
func (svc *coordinatorService) lock(key string) func() {
	svc.dispatchMu.Lock()
	lock, ok := svc.dispatchLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		svc.dispatchLocks[key] = lock
	}
	svc.dispatchMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// newCoordinatorService opens the coordinator store under stateDir.
func newCoordinatorService(fleet *Fleet, stateDir string, now func() time.Time) *coordinatorService {
	svc := &coordinatorService{
		fleet:         fleet,
		now:           now,
		dispatchLocks: map[string]*sync.Mutex{},
	}
	if svc.now == nil {
		svc.now = time.Now
	}
	dir := filepath.Join(stateDir, coordinatorStateSubpath)
	store, err := openCoordinatorStore(dir, svc.now)
	if err != nil {
		svc.openErr = err
		return svc
	}
	svc.store = store
	return svc
}

// coordinatorSetup opens the store beside the daemon's other state and registers
// the routes. Only a daemon that holds a fleet and a state directory can host a
// coordinator conversation; anything else keeps its routes exactly as they were.
func (s *Server) coordinatorSetup(fleet *Fleet) {
	if fleet == nil || fleet.stateDir == "" {
		return
	}
	s.coordinator = newCoordinatorService(fleet, fleet.stateDir, fleet.now)
	s.coordinatorRoutes()
}

// coordinatorRoutes registers the coordinator endpoints on the /api mux.
func (s *Server) coordinatorRoutes() {
	s.mux.HandleFunc("GET /api/coordinator/tasks", s.coordinatorListTasks)
	s.mux.HandleFunc("POST /api/coordinator/tasks", s.coordinatorCreateTask)
	s.mux.HandleFunc("GET /api/coordinator/tasks/{id}", s.coordinatorGetTask)
	s.mux.HandleFunc("GET /api/coordinator/tasks/{id}/attention", s.coordinatorTaskAttention)
	// The assignment snapshot, the source thread's handoff, and the one retry a
	// failed assignment offers (agent-first Studio, task 7). They are reads plus
	// one task-scoped action: the live updates arrive as fleet events, so a client
	// that reconnects reads these instead of replaying.
	s.mux.HandleFunc("GET /api/coordinator/tasks/{id}/assignments", s.coordinatorTaskAssignmentList)
	s.mux.HandleFunc("GET /api/coordinator/sources/{project}/{thread}", s.coordinatorSourceView)
	s.mux.HandleFunc("POST /api/coordinator/tasks/{id}/assignments/{assignment}/retry", s.coordinatorRetry)
	// The scoped stop (agent-first Studio, task 8): one task's own work, with the
	// intent made durable before a child is cancelled. It is one request and one
	// answer, so a screen shows the stop as pending until this returns.
	s.mux.HandleFunc("POST /api/coordinator/tasks/{id}/stop", s.coordinatorStop)
}

// available reports the service and its store, or why it cannot answer.
func (s *Server) coordinatorAvailable() (*coordinatorService, error) {
	if s == nil || s.coordinator == nil {
		return nil, ErrCoordinatorUnavailable
	}
	if s.coordinator.openErr != nil {
		return nil, ErrCoordinatorUnavailable
	}
	if s.coordinator.store == nil {
		return nil, ErrCoordinatorUnavailable
	}
	return s.coordinator, nil
}

// host is the serving daemon's identity, stamped on every task and answer. It
// comes from the daemon, never from a request: the same task id on another host
// names a different task.
func (s *Server) coordinatorHost() string {
	if instanceID, _, ok := s.marshalInstance(); ok {
		return instanceID
	}
	return ""
}

// coordinatorListTasks answers GET /api/coordinator/tasks: the destinations a
// person can reopen on this host, newest first.
func (s *Server) coordinatorListTasks(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	tasks, err := svc.store.List()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	host := s.coordinatorHost()
	out := contract.CoordinatorTaskList{Tasks: make([]contract.CoordinatorTask, 0, len(tasks)), Host: host}
	for _, task := range tasks {
		task.Host = host
		out.Tasks = append(out.Tasks, task)
	}
	writeJSON(w, http.StatusOK, out)
}

// coordinatorCreateTask answers POST /api/coordinator/tasks: the first send of a
// coordinator destination. A request id that already minted a task answers that
// task (200); a fresh one creates it (201).
func (s *Server) coordinatorCreateTask(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	var req contract.CoordinatorCreateRequest
	if !decodeStrictJSON(w, r, &req) {
		return
	}
	// A project this daemon did not register names no scope: a task may only be
	// created over projects the host can resolve.
	for _, id := range req.Projects {
		if _, ok := svc.fleet.ws.ProjectRootByID(strings.TrimSpace(id)); !ok {
			writeCoordinatorError(w, errCoordinatorUnknownProject)
			return
		}
	}
	task, created, err := svc.store.Create(req)
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task.Host = s.coordinatorHost()
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, contract.CoordinatorCreateResult{Task: task, Created: created})
}

// coordinatorGetTask answers GET /api/coordinator/tasks/{id}.
func (s *Server) coordinatorGetTask(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task, err := svc.store.Get(r.PathValue("id"))
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task.Host = s.coordinatorHost()
	writeJSON(w, http.StatusOK, task)
}

// coordinatorTaskAttention answers GET /api/coordinator/tasks/{id}/attention:
// the read-only summary of what needs a person across the task's projects. The
// read itself lives with the tool surface that asks it (marshal_studio_tools.go)
// so the browser and the coordinator's own conversation cannot disagree about
// what it means.
func (s *Server) coordinatorTaskAttention(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task, err := svc.store.Get(r.PathValue("id"))
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task.Host = s.coordinatorHost()
	writeJSON(w, http.StatusOK, s.coordinatorAttention(task))
}

// coordinatorTaskAssignmentList answers GET
// /api/coordinator/tasks/{id}/assignments: the task's assignments, reconciled
// against what the daemon now holds, and the newest result it delivered to its
// source thread.
//
// It is the snapshot a Studio reads on reconnect. The live updates arrive as
// fleet events (DeltaCoordinator), so a client that missed them reads this and
// cannot disagree with what the stream would have said.
func (s *Server) coordinatorTaskAssignmentList(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task, err := svc.store.Get(r.PathValue("id"))
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task.Host = s.coordinatorHost()
	assignments, err := s.coordinatorTaskAssignments(r.Context(), task)
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	out := contract.CoordinatorAssignmentList{TaskID: task.ID, Host: task.Host, Assignments: assignments}
	if delivery, held, derr := svc.store.Delivery(task.ID); derr == nil && held {
		out.Delivery = &delivery
	}
	writeJSON(w, http.StatusOK, out)
}

// coordinatorSourceView answers GET /api/coordinator/sources/{project}/{thread}:
// the handoff one project thread is the source of, as the thread screen reads it.
//
// It is the read that keeps a delivered result available to a thread nobody was
// watching: the task, its assignments and the newest delivery are all durable, so
// opening the thread later draws the result rather than depending on a stream
// that was not attached when it was ready. A thread nothing was handed off from
// answers an empty view — that is a valid answer, not an error.
func (s *Server) coordinatorSourceView(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	projectID := r.PathValue("project")
	threadID := r.PathValue("thread")
	if _, ok := svc.fleet.ws.ProjectRootByID(projectID); !ok {
		writeCoordinatorError(w, errCoordinatorUnknownProject)
		return
	}
	out := contract.CoordinatorSourceView{Host: s.coordinatorHost(), Assignments: []contract.CoordinatorAssignment{}}
	source := contract.CoordinatorThreadRef{ProjectID: projectID, Kind: "agent", ID: threadID}
	var task contract.CoordinatorTask
	found := false
	// The newest task the thread handed work to is the one its screen shows; a
	// stopped one is still shown, because its result is what the thread carries.
	tasks, err := svc.store.List()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	for _, candidate := range tasks {
		if candidate.Source == nil || *candidate.Source != source {
			continue
		}
		task, found = candidate, true
		break
	}
	if !found {
		writeJSON(w, http.StatusOK, out)
		return
	}
	task.Host = s.coordinatorHost()
	assignments, err := s.coordinatorTaskAssignments(r.Context(), task)
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	out.Task = &task
	out.Assignments = assignments
	if delivery, held, derr := svc.store.Delivery(task.ID); derr == nil && held {
		out.Delivery = &delivery
	}
	writeJSON(w, http.StatusOK, out)
}

// coordinatorRetry answers POST
// /api/coordinator/tasks/{id}/assignments/{assignment}/retry: one failed or
// expired assignment attempted again under its existing identity.
//
// It is the scoped retry a person's failure row offers, so it retries exactly
// that assignment: its siblings are untouched, and a steering target owes the
// person's direction again, which travels in the header the trusted transport
// sets rather than in the body a model writes.
func (s *Server) coordinatorRetry(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task, err := svc.store.Get(r.PathValue("id"))
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task.Host = s.coordinatorHost()
	if _, err := s.coordinatorRetryAssignment(r.Context(), task, r.PathValue("assignment"), marshalUserDirectionReceiptID(r)); err != nil {
		var challenge *marshalUserDirectionChallenge
		if errors.As(err, &challenge) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":            factoryCodeUserDirectionRequired,
				"receipt":          challenge.Receipt,
				"receiptExpiresAt": challenge.ExpiresAt.UTC().Format(time.RFC3339),
			})
			return
		}
		writeCoordinatorError(w, err)
		return
	}
	assignments, err := s.coordinatorTaskAssignments(r.Context(), task)
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	out := contract.CoordinatorAssignmentList{TaskID: task.ID, Host: task.Host, Assignments: assignments}
	if delivery, held, derr := svc.store.Delivery(task.ID); derr == nil && held {
		out.Delivery = &delivery
	}
	writeJSON(w, http.StatusOK, out)
}

// coordinatorStop answers POST /api/coordinator/tasks/{id}/stop: one task's own
// work stopped at the scope the person asked for.
//
// The intent is durable before a worker is cancelled, so a request that fails
// part-way leaves a task that reads stopping — pending until the daemon confirms —
// rather than a stopped task whose children may still be running. A repeat is
// answered with the state the first stop left.
func (s *Server) coordinatorStop(w http.ResponseWriter, r *http.Request) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task, err := svc.store.Get(r.PathValue("id"))
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	task.Host = s.coordinatorHost()
	out, err := s.coordinatorStopTask(r.Context(), task)
	if err != nil {
		writeCoordinatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// coordinatorEmitUpdate announces one task's assignments, and its newest source
// delivery, on the fleet stream.
//
// It is a fleet delta rather than a second subscription: a Studio already
// following the fleet receives assignment progress without asking, and the
// snapshot reads (CoordinatorAssignmentList, CoordinatorSourceView) are what a
// client that reconnects reads instead of replaying. SessionID is the source
// thread's agent id, so a screen watching that thread can tell the delta is
// about it.
func (s *Server) coordinatorEmitUpdate(task contract.CoordinatorTask, assignments []contract.CoordinatorAssignment, delivery *contract.CoordinatorDelivery) {
	if s == nil || s.fleet == nil {
		return
	}
	if assignments == nil {
		assignments = []contract.CoordinatorAssignment{}
	}
	delta := contract.CoordinatorDelta{
		Kind:        contract.DeltaCoordinator,
		TaskID:      task.ID,
		Host:        firstNonEmpty(task.Host, s.coordinatorHost()),
		Source:      task.Source,
		Assignments: assignments,
	}
	if task.Source != nil {
		delta.SessionID = task.Source.ID
	}
	if delivery != nil {
		delta.Delivery = delivery
	}
	s.fleet.emit(delta)
}

// coordinatorAssignmentsDigest is the shape of one assignment list, so a read can
// tell "nothing changed" from "this observation learned something".
func coordinatorAssignmentsDigest(assignments []contract.CoordinatorAssignment) string {
	payload := make([]string, 0, len(assignments))
	for _, a := range assignments {
		payload = append(payload, strings.Join([]string{
			a.ID, a.ProjectID, string(a.State), a.Failure, a.Reason,
			fmt.Sprint(a.ResultRevision), a.IntakeID,
		}, "\x00"))
	}
	return strings.Join(payload, "\x01")
}

// errCoordinatorUnknownProject is a create naming a project this daemon did not
// register. It is the caller's input problem, not an unavailable store.
var errCoordinatorUnknownProject = errors.New("bridge: coordinator task names an unknown project")

// writeCoordinatorError maps a coordinator error onto the statuses a client
// distinguishes. A store that cannot be read is never reported as an empty one.
func writeCoordinatorError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadRequest, "invalid_input"
	switch {
	case errors.Is(err, ErrCoordinatorNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrCoordinatorConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, ErrCoordinatorUnavailable):
		status, code = http.StatusServiceUnavailable, "unavailable"
	case errors.Is(err, ErrCoordinatorTooLarge):
		status, code = http.StatusInsufficientStorage, "store_full"
	case errors.Is(err, errCoordinatorUnknownProject):
		status, code = http.StatusBadRequest, "unknown_project"
	}
	writeJSON(w, status, map[string]string{"error": code})
}
