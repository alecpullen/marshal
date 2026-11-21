package bridge

import (
	"errors"
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
	svc.dispatchMu.Lock()
	lock, ok := svc.dispatchLocks[assignmentID]
	if !ok {
		lock = &sync.Mutex{}
		svc.dispatchLocks[assignmentID] = lock
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
	case errors.Is(err, errCoordinatorUnknownProject):
		status, code = http.StatusBadRequest, "unknown_project"
	}
	writeJSON(w, status, map[string]string{"error": code})
}
