package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PlannerService is the sole owner of planner backends and of the durable
// state that describes them. A browser never holds a process, a filesystem
// path or an adapter session: it holds a planner ID this service minted, and
// every route resolves that ID here.
//
// The lifecycle rules the service enforces are the ones that keep a lost HTTP
// connection from destroying work and a restarted bridge from lying about it:
//
//   - A turn is recorded as dispatched before the backend is called, and never
//     resent, so a dead turn is reported unknown rather than repeated.
//   - A turn runs on a context this service owns, so closing the browser tab
//     does not cancel it. Only an explicit cancel or end does.
//   - Ownership of a workspace is claimed by a record, never inferred from the
//     absence of one: an unreadable or stale owner is unknown, not free.
const (
	// plannerAdapterEnv names the environment variable holding the absolute
	// path of the ACP adapter a planner drives. There is no fallback: a bridge
	// that was not configured with an adapter refuses to create a planner
	// rather than quietly running a different agent.
	plannerAdapterEnv = "MARSHAL_CLAUDE_ACP_ADAPTER"
	// plannerMaxPromptText bounds one prompt, matching the ACP inbound frame
	// cap.
	plannerMaxPromptText = 1 << 20
)

// Planner errors, each mapped to one status by writePlannerError.
var (
	errPlannerInvalid         = errors.New("invalid planner request")
	errPlannerNotFound        = errors.New("unknown planner")
	errPlannerNotLive         = errors.New("planner has no live session")
	errPlannerOccupancy       = errors.New("planner workspace is occupied")
	errPlannerBusy            = errors.New("a prompt is already in flight")
	errPlannerModeUnconfirmed = errors.New("the planner's mode is not confirmed")
	errPlannerUnknownTurn     = errors.New("the turn's outcome is unknown")
	errPlannerRequestUnknown  = errors.New("unknown planner request")
	errPlannerRequestSettled  = errors.New("planner request already settled")
	errPlannerUnavailable     = errors.New("planner unavailable")
)

// plannerOccupancyError is the conflict a create request gets when the
// workspace it named is in use or of unknown ownership. It carries the
// occupancy and the live owners so the browser can show who holds it; the
// caller proceeds by setting proceedShared.
type plannerOccupancyError struct {
	WorkspaceID string
	Occupancy   PlannerOccupancy
	Owners      []string
}

func (e *plannerOccupancyError) Error() string {
	return fmt.Sprintf("workspace %s is %s; set proceedShared to use it anyway", e.WorkspaceID, e.Occupancy)
}

func (e *plannerOccupancyError) Is(target error) bool { return target == errPlannerOccupancy }

// PlannerService owns the planner backends. No package-level state: one
// service per server, all of its seams instance fields, so two bridges in one
// process (a restart in a test, a second state directory) never share one.
type PlannerService struct {
	fleet    *Fleet
	stateDir string
	store    *plannerStore

	// backendFor builds the backend a planner drives. newPlannerService
	// installs the production constructor; tests replace it with a fake so no
	// process is spawned.
	backendFor func(spec WorkerSessionSpec, events workerEventSink, requests workerRequestSink) (WorkerBackend, error)
	// provisionWorkspace creates a fresh checkout for a new planner. The
	// production implementation uses the bridge's git mirror machinery; tests
	// replace it so no git runs.
	provisionWorkspace func(ctx context.Context, workspaceID string, repo Repo) error
	// now is the clock, so a test can age a heartbeat without sleeping.
	now func() time.Time

	mu       sync.Mutex
	planners map[string]*plannerInstance
}

// plannerInstance is one running planner: its record, its backend session and
// the requests waiting for an answer. The zero of every field is "not yet
// started", so a partially built instance is never mistaken for a live one.
type plannerInstance struct {
	svc *PlannerService
	id  string

	mu      sync.Mutex
	rec     PlannerRecord
	seq     uint64
	backend WorkerBackend
	session string
	// turnRunning is true from the durable dispatch until the backend returns.
	// A second prompt is refused for as long as it holds.
	turnRunning bool
	turnCancel  context.CancelFunc
	pending     map[string]*plannerPending
	ended       bool
}

// plannerPending is one agent request waiting for the browser's answer. The
// channel is buffered and never closed, so a settlement that races a
// cancellation drops its answer instead of panicking.
type plannerPending struct {
	requestID string
	method    string
	settled   bool
	answer    chan plannerAnswer
}

type plannerAnswer struct {
	response json.RawMessage
	err      error
}

// newPlannerService builds the planner service for one bridge. It rewrites any
// turn a previous bridge left in flight to unknown — the turn may already have
// changed the workspace, so it is never resent — and touches no other state.
func newPlannerService(fleet *Fleet, stateDir string) *PlannerService {
	svc := &PlannerService{
		fleet:              fleet,
		stateDir:           stateDir,
		store:              newPlannerStore(stateDir),
		now:                time.Now,
		planners:           map[string]*plannerInstance{},
		provisionWorkspace: plannerGitWorkspace(fleet, stateDir),
	}
	svc.backendFor = newPlannerACPBackend
	svc.recover()
	return svc
}

func (s *PlannerService) nowUTC() time.Time { return s.now().UTC() }

// newPlannerACPBackend is the production backend constructor: an ACP adapter
// configured by absolute path in the environment, run in the planner's own
// workspace. The command is a single configured executable with no arguments
// built from paths or user input, and there is no fallback binary or download.
func newPlannerACPBackend(spec WorkerSessionSpec, events workerEventSink, requests workerRequestSink) (WorkerBackend, error) {
	adapter := os.Getenv(plannerAdapterEnv)
	if adapter == "" || !filepath.IsAbs(adapter) {
		return nil, fmt.Errorf("%w: %s must name the absolute path of the ACP adapter a planner drives", errPlannerUnavailable, plannerAdapterEnv)
	}
	transport := newProcessTransport(adapter, spec.CWD, nil, nil)
	return newACPWorkerBackend(transport, events, requests), nil
}

// plannerGitWorkspace returns the production provisioning function: a separate
// checkout at the registered project base, through the bridge's existing mirror
// and working-tree machinery. The checkout's directory is derived from the ID
// the bridge minted, never from a caller's string.
func plannerGitWorkspace(fleet *Fleet, stateDir string) func(context.Context, string, Repo) error {
	return func(ctx context.Context, workspaceID string, repo Repo) error {
		if fleet == nil || fleet.git == nil {
			return fmt.Errorf("%w: git is not available in this bridge", errPlannerUnavailable)
		}
		if fleet.creds == nil {
			return fmt.Errorf("%w: no credential store is configured", errPlannerUnavailable)
		}
		cred, err := fleet.creds.Resolve(ctx, DefaultOwnerID, repo.CredRef)
		if err != nil {
			return fmt.Errorf("%w: resolve credential for %s: %v", errPlannerUnavailable, repo.ID, err)
		}
		mirror, err := fleet.git.EnsureMirror(stateDir, repo.URL, cred)
		if err != nil {
			return fmt.Errorf("%w: %v", errPlannerUnavailable, err)
		}
		if _, err := fleet.git.PrepareTree(stateDir, workspaceID, mirror, repo.URL, repo.Branch); err != nil {
			return fmt.Errorf("%w: %v", errPlannerUnavailable, err)
		}
		return nil
	}
}

// recover rewrites the durable state a previous bridge could not finish. A
// turn recorded as dispatched or running is unknown: it may have been applied
// to the workspace, so it is not resent and not reported as done. A request
// left open is cancelled once, because its backend is gone with the process
// that held it.
func (s *PlannerService) recover() {
	records, err := s.store.list()
	if err != nil {
		return
	}
	now := s.nowUTC()
	for _, rec := range records {
		changed := false
		for i := range rec.Turns {
			switch rec.Turns[i].State {
			case plannerTurnDispatched, plannerTurnRunning:
				rec.Turns[i].State = plannerTurnUnknown
				rec.Turns[i].Reason = "the bridge restarted while this turn was in flight; it was not resent"
				rec.Turns[i].At = now
				changed = true
			}
		}
		for i := range rec.Requests {
			if rec.Requests[i].State == plannerDecisionPending {
				rec.Requests[i].State = plannerDecisionCancelled
				rec.Requests[i].Reason = "the bridge restarted before this request was answered"
				rec.Requests[i].At = now
				changed = true
			}
		}
		if !changed {
			continue
		}
		rec.UpdatedAt = now
		_ = s.store.write(rec)
	}
}

// Create starts a planner: a fresh separate checkout by default, or a
// workspace the bridge returned earlier. Project trust and workspace ownership
// are resolved here, and nothing a caller sent is used as a path.
func (s *PlannerService) Create(ctx context.Context, req PlannerCreateRequest) (PlannerView, error) {
	root, repo, err := s.project(req.ProjectRoot, req.RepoID)
	if err != nil {
		return PlannerView{}, err
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = plannerModePermissive
	}
	if mode != plannerModePermissive && mode != plannerModeRestricted {
		return PlannerView{}, fmt.Errorf("%w: mode %q is not %q or %q", errPlannerInvalid, mode, plannerModePermissive, plannerModeRestricted)
	}

	workspaceID := strings.TrimSpace(req.WorkspaceID)
	fresh := workspaceID == ""
	if fresh {
		// A new planner always gets its own checkout: it never adopts another
		// planner's workspace just because that workspace looks idle.
		if workspaceID, err = newPlannerID(); err != nil {
			return PlannerView{}, fmt.Errorf("%w: %v", errPlannerUnavailable, err)
		}
	} else {
		if !validPlannerID(workspaceID) {
			return PlannerView{}, fmt.Errorf("%w: workspace %q is not an ID this bridge returned", errPlannerInvalid, workspaceID)
		}
		if !s.workspaceKnown(workspaceID) {
			return PlannerView{}, fmt.Errorf("%w: workspace %q is not known to this bridge", errPlannerInvalid, workspaceID)
		}
		occupancy, owners := s.occupancy(workspaceID)
		if occupancy != plannerOccupancyFree && !req.ProceedShared {
			return PlannerView{}, &plannerOccupancyError{WorkspaceID: workspaceID, Occupancy: occupancy, Owners: owners}
		}
	}
	id, err := newPlannerID()
	if err != nil {
		return PlannerView{}, fmt.Errorf("%w: %v", errPlannerUnavailable, err)
	}
	backendID, err := newPlannerID()
	if err != nil {
		return PlannerView{}, fmt.Errorf("%w: %v", errPlannerUnavailable, err)
	}
	inst := &plannerInstance{svc: s, id: id, pending: map[string]*plannerPending{}}
	spec := WorkerSessionSpec{
		WorkerID:    backendID,
		ProjectRoot: root,
		WorkspaceID: workspaceID,
		CWD:         workspaceDirFor(s.stateDir, workspaceID),
		Mode:        mode,
	}
	// The backend is configured before the checkout is made: a bridge that has
	// no adapter refuses here, rather than provisioning a workspace for a
	// planner it cannot run.
	backend, err := s.backendFor(spec, inst.onEvent, inst.onRequest)
	if err != nil {
		return PlannerView{}, err
	}
	if fresh {
		if err := s.provisionWorkspace(ctx, workspaceID, repo); err != nil {
			return PlannerView{}, fmt.Errorf("%w: provision workspace: %v", errPlannerUnavailable, err)
		}
	}

	now := s.nowUTC()
	rec := PlannerRecord{
		ID:            id,
		ProjectRoot:   root,
		RepoID:        repo.ID,
		BackendID:     backendID,
		WorkspaceID:   workspaceID,
		RequestedMode: mode,
		Trust:         projectTrust(root),
		State:         plannerStateActive,
		PID:           os.Getpid(),
		Heartbeat:     now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	// The record is durable before the backend session exists: a planner that
	// is starting already owns its workspace, and a crash here is a stale
	// record (unknown ownership), not a missing one (free).
	if err := s.store.write(rec); err != nil {
		return PlannerView{}, err
	}
	inst.rec = rec

	sessionID, caps, err := backend.Start(ctx, spec)
	if err != nil {
		s.abandon(inst, "the backend refused to start a session: "+err.Error())
		return PlannerView{}, fmt.Errorf("%w: start worker session: %v", errPlannerUnavailable, err)
	}

	inst.mu.Lock()
	inst.backend = backend
	inst.session = sessionID
	inst.rec.SessionID = sessionID
	inst.rec.Capabilities = caps
	// The mode counts as confirmed only when the session reported it among the
	// modes it actually offers; anything else leaves it empty, and a prompt is
	// then refused rather than sent under an unverified access mode.
	if containsMode(caps.Modes, mode) {
		inst.rec.ConfirmedMode = mode
	}
	inst.touchLocked()
	persistErr := inst.persistLocked()
	rec = inst.rec.clone()
	inst.mu.Unlock()
	if persistErr != nil {
		_ = backend.Close(context.Background(), sessionID)
		s.abandon(inst, "the planner record could not be written: "+persistErr.Error())
		return PlannerView{}, persistErr
	}
	s.mu.Lock()
	s.planners[id] = inst
	s.mu.Unlock()
	inst.appendEvent("planner.started", map[string]any{
		"workspaceId": workspaceID,
		"mode":        mode,
		"sessionId":   sessionID,
	})
	// The view is built after the instance is registered and without its lock:
	// occupancy reads the planner set, which now includes this planner.
	return s.view(rec, true), nil
}

// Get reports one planner: its durable record, what this bridge knows about
// its workspace, and what its session can do.
func (s *PlannerService) Get(id string) (PlannerView, error) {
	if inst, ok := s.live(id); ok {
		return s.view(inst.snapshot(), true), nil
	}
	rec, err := s.store.read(id)
	if err != nil {
		return PlannerView{}, err
	}
	return s.view(rec, s.recordLive(rec, s.nowUTC())), nil
}

// Prompt dispatches one turn, or replays the outcome already recorded for the
// request ID. It takes no request context on purpose: a browser that goes away
// must not cancel the turn it just started.
func (s *PlannerService) Prompt(id string, req PlannerPromptRequest) (PlannerTurnReply, error) {
	inst, err := s.livePlanner(id)
	if err != nil {
		return PlannerTurnReply{}, err
	}
	return inst.prompt(req)
}

// Cancel interrupts the in-flight turn and nothing else.
func (s *PlannerService) Cancel(id string) error {
	inst, err := s.livePlanner(id)
	if err != nil {
		return err
	}
	return inst.cancelTurn()
}

// Answer settles one agent request exactly once with the browser's choice.
func (s *PlannerService) Answer(id, requestID string, response json.RawMessage) error {
	inst, err := s.livePlanner(id)
	if err != nil {
		return err
	}
	return inst.answer(requestID, response)
}

// Events replays the planner's journal from just after sequence after. It
// answers the journal as stored, so it never streams and never blocks.
func (s *PlannerService) Events(id string, after uint64) ([]PlannerEvent, error) {
	if _, ok := s.live(id); !ok {
		if _, err := s.store.read(id); err != nil {
			return nil, err
		}
	}
	return s.store.events(id, after)
}

// End is the explicit end of a planner: its session is closed, its open
// requests settle as cancelled and its workspace stops being claimed. The
// journal is kept, because an ended planner's history is still worth reading.
func (s *PlannerService) End(id string) error {
	if inst, ok := s.live(id); ok {
		if err := inst.end(); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.planners, id)
		s.mu.Unlock()
		return nil
	}
	rec, err := s.store.read(id)
	if err != nil {
		return err
	}
	if rec.State == plannerStateEnded {
		return nil
	}
	now := s.nowUTC()
	rec.State = plannerStateEnded
	rec.EndedAt = &now
	rec.UpdatedAt = now
	rec.Heartbeat = now
	rec.Reason = "ended by the client; this bridge generation did not own the session"
	for i := range rec.Requests {
		if rec.Requests[i].State == plannerDecisionPending {
			rec.Requests[i].State = plannerDecisionCancelled
			rec.Requests[i].Reason = "the planner ended before this request was answered"
			rec.Requests[i].At = now
		}
	}
	return s.store.write(rec)
}

// project validates the caller's project and repo: the root must be the
// registered one the repo belongs to, and both must be known here. A mismatch
// is the caller's mistake, not a lookup to guess at.
func (s *PlannerService) project(root, repoID string) (string, Repo, error) {
	if s.fleet == nil || s.fleet.ws == nil {
		return "", Repo{}, fmt.Errorf("%w: this bridge has no workspace", errPlannerUnavailable)
	}
	if strings.TrimSpace(repoID) == "" {
		return "", Repo{}, fmt.Errorf("%w: repoId is required", errPlannerInvalid)
	}
	repo, ok := s.fleet.ws.Repo(repoID)
	if !ok {
		return "", Repo{}, fmt.Errorf("%w: repo %q is not registered", errPlannerInvalid, repoID)
	}
	if strings.TrimSpace(root) == "" {
		return "", Repo{}, fmt.Errorf("%w: projectRoot is required", errPlannerInvalid)
	}
	if err := s.fleet.registeredProject(root); err != nil {
		return "", Repo{}, fmt.Errorf("%w: %v", errPlannerInvalid, err)
	}
	if registered := s.fleet.ws.ProjectRootByRepo(repo.ID); registered == "" || registered != root {
		return "", Repo{}, fmt.Errorf("%w: project %q is not the registered project for repo %q", errPlannerInvalid, root, repo.ID)
	}
	return root, repo, nil
}

// workspaceKnown reports whether this bridge has a checkout for workspaceID.
// A workspace is known because the bridge made it, never because a caller
// named a directory.
func (s *PlannerService) workspaceKnown(workspaceID string) bool {
	if !validPlannerID(workspaceID) {
		return false
	}
	info, err := os.Stat(workspaceDirFor(s.stateDir, workspaceID))
	return err == nil && info.IsDir()
}

// recordLive reports whether this service holds the live owner of rec. The
// record's own claim (its PID and heartbeat) is necessary but not sufficient:
// a bridge that restarted in the same process over the same state directory
// finds records whose heartbeat still looks fresh, and none of them is live
// because no instance is driving it.
func (s *PlannerService) recordLive(rec PlannerRecord, now time.Time) bool {
	if !plannerRecordLive(rec, now) {
		return false
	}
	_, owned := s.live(rec.ID)
	return owned
}

// occupancy reports what a workspace looks like to a new planner, with the IDs
// of the planners that are live on it. A record that exists but is not live is
// unknown: the bridge will not hand out a workspace whose owner it cannot
// account for, and it will not claim a free one without a record.
func (s *PlannerService) occupancy(workspaceID string) (PlannerOccupancy, []string) {
	now := s.nowUTC()
	owned, live := false, []string{}
	for _, rec := range s.records() {
		if rec.WorkspaceID != workspaceID || rec.State != plannerStateActive {
			continue
		}
		owned = true
		if s.recordLive(rec, now) {
			live = append(live, rec.ID)
		}
	}
	if len(live) > 0 {
		sort.Strings(live)
		return plannerOccupancyInUse, live
	}
	if owned {
		return plannerOccupancyUnknown, nil
	}
	return plannerOccupancyFree, nil
}

// records is every planner record this bridge knows: the ones on disk, with a
// live instance's fresher copy winning. The instances are snapshotted outside
// the service lock so no instance lock is ever held while the service's is.
func (s *PlannerService) records() []PlannerRecord {
	onDisk, _ := s.store.list()
	s.mu.Lock()
	live := make([]*plannerInstance, 0, len(s.planners))
	for _, inst := range s.planners {
		live = append(live, inst)
	}
	s.mu.Unlock()

	fresh := make(map[string]PlannerRecord, len(live))
	for _, inst := range live {
		fresh[inst.id] = inst.snapshot()
	}
	out := make([]PlannerRecord, 0, len(onDisk)+len(fresh))
	seen := make(map[string]bool, len(onDisk))
	for _, rec := range onDisk {
		if current, ok := fresh[rec.ID]; ok {
			rec = current
		}
		out = append(out, rec)
		seen[rec.ID] = true
	}
	for id, rec := range fresh {
		if !seen[id] {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// view projects a record for the browser. A record with no live owner reports
// state "unknown" rather than claiming to be active, and a session resume is
// offered only when it is actually available.
func (s *PlannerService) view(rec PlannerRecord, live bool) PlannerView {
	occupancy, owners := s.occupancy(rec.WorkspaceID)
	resume, reason := plannerResume(rec)
	state := rec.State
	if state == plannerStateActive && !live {
		state = plannerStateUnknown
	}
	return PlannerView{
		ID:              rec.ID,
		ProjectRoot:     rec.ProjectRoot,
		RepoID:          rec.RepoID,
		BackendID:       rec.BackendID,
		WorkspaceID:     rec.WorkspaceID,
		Occupancy:       occupancy,
		Owners:          owners,
		SessionID:       rec.SessionID,
		RequestedMode:   rec.RequestedMode,
		ConfirmedMode:   rec.ConfirmedMode,
		ModeConfirmed:   rec.ConfirmedMode != "" && rec.ConfirmedMode == rec.RequestedMode,
		State:           state,
		Live:            live,
		EventSequence:   rec.EventSequence,
		Capabilities:    rec.Capabilities,
		SessionLoad:     rec.Capabilities.SessionLoad,
		ResumeAvailable: resume,
		ResumeReason:    reason,
		Trust:           rec.Trust,
		Turns:           rec.Turns,
		Requests:        rec.Requests,
		Artifacts:       rec.Artifacts,
		Reason:          rec.Reason,
		CreatedAt:       rec.CreatedAt,
		UpdatedAt:       rec.UpdatedAt,
		EndedAt:         rec.EndedAt,
	}
}

// plannerResume reports whether a planner's session can be resumed, and why
// not when it cannot. Session load is the backend's capability; even with it,
// a resume is only offered after a replay this bridge actually performed, and
// nothing here performs one. The browser is told to start a new planner.
func plannerResume(rec PlannerRecord) (bool, string) {
	if !rec.Capabilities.SessionLoad {
		return false, "session resume is unavailable: the backend has no session load support; start a new planner"
	}
	return false, "session resume is unavailable: a load has not been verified for this session; start a new planner"
}

// live returns the running instance for id, if this bridge owns it.
func (s *PlannerService) live(id string) (*plannerInstance, bool) {
	if !validPlannerID(id) {
		return nil, false
	}
	s.mu.Lock()
	inst, ok := s.planners[id]
	s.mu.Unlock()
	return inst, ok
}

// livePlanner resolves id to a running instance, answering "no live session"
// for a record that exists but is not owned by this bridge: a prompt is never
// replayed into a backend that is gone.
func (s *PlannerService) livePlanner(id string) (*plannerInstance, error) {
	if inst, ok := s.live(id); ok {
		return inst, nil
	}
	if _, err := s.store.read(id); err != nil {
		if errors.Is(err, errPlannerInvalid) {
			return nil, fmt.Errorf("%w: %s", errPlannerNotFound, id)
		}
		return nil, err
	}
	return nil, fmt.Errorf("%w: %s has no live session; start a new planner", errPlannerNotLive, id)
}

// abandon ends the durable claim of a planner that never started, so a failed
// creation never leaves a workspace occupied by nobody.
func (s *PlannerService) abandon(inst *plannerInstance, reason string) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	now := s.nowUTC()
	inst.rec.State = plannerStateEnded
	inst.rec.EndedAt = &now
	inst.rec.UpdatedAt = now
	inst.rec.Heartbeat = now
	inst.rec.Reason = reason
	inst.ended = true
	_ = s.store.write(inst.rec)
}

// snapshot copies the record under the instance lock, so a reader never shares
// a slice with the writer that is appending to it.
func (inst *plannerInstance) snapshot() PlannerRecord {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.rec.clone()
}

// clone deep-copies the slices a caller may read while the instance keeps
// writing.
func (rec PlannerRecord) clone() PlannerRecord {
	out := rec
	out.Turns = append([]PlannerTurn(nil), rec.Turns...)
	out.Requests = make([]PlannerDecision, len(rec.Requests))
	for i, d := range rec.Requests {
		d.Response = append(json.RawMessage(nil), d.Response...)
		out.Requests[i] = d
	}
	out.Artifacts = append([]PlannerArtifact(nil), rec.Artifacts...)
	out.Capabilities.Modes = append([]string(nil), rec.Capabilities.Modes...)
	if rec.EndedAt != nil {
		ended := *rec.EndedAt
		out.EndedAt = &ended
	}
	return out
}

// persistLocked writes the record. The caller holds the instance lock, so one
// writer at a time and a reader sees a whole transition.
func (inst *plannerInstance) persistLocked() error {
	return inst.svc.store.write(inst.rec)
}

// touchLocked refreshes the ownership claim: the heartbeat is what makes a
// record live rather than unknown.
func (inst *plannerInstance) touchLocked() {
	inst.rec.Heartbeat = inst.svc.nowUTC()
	inst.rec.UpdatedAt = inst.rec.Heartbeat
}

// appendEvent journals one event under the instance lock, so the planner's
// sequence is strictly increasing in the journal and never interleaved.
func (inst *plannerInstance) appendEvent(kind string, data any) {
	var raw json.RawMessage
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return
		}
		raw = encoded
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.ended && kind != "planner.ended" {
		return
	}
	inst.seq++
	inst.rec.EventSequence = inst.seq
	inst.rec.Heartbeat = inst.svc.nowUTC()
	// The backend's own sequence is deliberately dropped: it counts one
	// backend's notifications, and the browser replays this journal.
	_ = inst.svc.store.appendEvent(inst.id, PlannerEvent{Sequence: inst.seq, Kind: kind, Data: raw})
}

// onEvent is the backend's event sink. It never fails the backend: a journal
// that cannot be written loses an event, not a turn.
func (inst *plannerInstance) onEvent(_ string, _ uint64, kind string, data json.RawMessage) {
	inst.appendEvent(kind, data)
}

// onRequest is the backend's request sink: it registers the request durably,
// then waits for its one settlement. The wait is on the backend's context, so
// a backend that gives up removes the request rather than stranding it.
func (inst *plannerInstance) onRequest(ctx context.Context, _ string, requestID, method string, params json.RawMessage) (json.RawMessage, error) {
	inst.mu.Lock()
	if inst.ended {
		inst.mu.Unlock()
		return nil, fmt.Errorf("%w: the planner has ended", errPlannerNotLive)
	}
	if _, dup := inst.pending[requestID]; dup {
		inst.mu.Unlock()
		return nil, fmt.Errorf("%w: request %q is already open", errPlannerRequestSettled, requestID)
	}
	pending := &plannerPending{requestID: requestID, method: method, answer: make(chan plannerAnswer, 1)}
	inst.pending[requestID] = pending
	now := inst.svc.nowUTC()
	inst.rec.Requests = append(inst.rec.Requests, PlannerDecision{
		RequestID: requestID,
		Method:    method,
		State:     plannerDecisionPending,
		At:        now,
	})
	inst.touchLocked()
	_ = inst.persistLocked()
	inst.mu.Unlock()

	select {
	case answer := <-pending.answer:
		return answer.response, answer.err
	case <-ctx.Done():
		inst.cancelRequest(requestID, "the backend abandoned this request before it was answered")
		return nil, fmt.Errorf("%w: request %q was not answered", errPlannerRequestSettled, requestID)
	}
}

// answer settles one request. Only the first settlement counts: a second
// answer is refused, not applied twice.
func (inst *plannerInstance) answer(requestID string, response json.RawMessage) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("%w: requestId is required", errPlannerInvalid)
	}
	inst.mu.Lock()
	pending := inst.pending[requestID]
	if pending == nil {
		// A request that was already settled is gone from the pending set, but
		// its decision is still on the record: a repeat answer is a conflict,
		// not an unknown request.
		recorded := false
		for _, decision := range inst.rec.Requests {
			if decision.RequestID == requestID {
				recorded = true
			}
		}
		inst.mu.Unlock()
		if recorded {
			return fmt.Errorf("%w: request %q was already settled", errPlannerRequestSettled, requestID)
		}
		return fmt.Errorf("%w: request %q is not open on planner %s", errPlannerRequestUnknown, requestID, inst.id)
	}
	if pending.settled {
		inst.mu.Unlock()
		return fmt.Errorf("%w: request %q was already settled", errPlannerRequestSettled, requestID)
	}
	pending.settled = true
	delete(inst.pending, requestID)
	now := inst.svc.nowUTC()
	for i := range inst.rec.Requests {
		if inst.rec.Requests[i].RequestID == requestID {
			inst.rec.Requests[i].State = plannerDecisionAnswered
			inst.rec.Requests[i].Response = append(json.RawMessage(nil), response...)
			inst.rec.Requests[i].At = now
		}
	}
	inst.touchLocked()
	_ = inst.persistLocked()
	inst.mu.Unlock()

	// Buffered, so the settlement never blocks on a backend that has already
	// walked away.
	pending.answer <- plannerAnswer{response: response}
	return nil
}

// cancelRequest records one request as cancelled exactly once. It is called
// from the request sink's own cancellation and from closing the planner.
func (inst *plannerInstance) cancelRequest(requestID, reason string) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	pending := inst.pending[requestID]
	if pending == nil || pending.settled {
		return
	}
	pending.settled = true
	delete(inst.pending, requestID)
	now := inst.svc.nowUTC()
	for i := range inst.rec.Requests {
		if inst.rec.Requests[i].RequestID == requestID && inst.rec.Requests[i].State == plannerDecisionPending {
			inst.rec.Requests[i].State = plannerDecisionCancelled
			inst.rec.Requests[i].Reason = reason
			inst.rec.Requests[i].At = now
		}
	}
	inst.touchLocked()
	_ = inst.persistLocked()
}

// prompt dispatches a turn or replays the recorded outcome of one.
func (inst *plannerInstance) prompt(req PlannerPromptRequest) (PlannerTurnReply, error) {
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return PlannerTurnReply{}, fmt.Errorf("%w: requestId is required", errPlannerInvalid)
	}
	if strings.TrimSpace(req.Text) == "" {
		return PlannerTurnReply{}, fmt.Errorf("%w: text is required", errPlannerInvalid)
	}
	if len(req.Text) > plannerMaxPromptText {
		return PlannerTurnReply{}, fmt.Errorf("%w: text exceeds %d bytes", errPlannerInvalid, plannerMaxPromptText)
	}
	textHash := plannerTextHash(req.Text)

	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.ended {
		return PlannerTurnReply{}, fmt.Errorf("%w: planner %s has ended", errPlannerNotLive, inst.id)
	}
	// A request ID is dispatched once. Its recorded outcome is replayed instead
	// of the turn being sent again, and an unknown outcome stays unknown.
	for _, turn := range inst.rec.Turns {
		if turn.RequestID != requestID {
			continue
		}
		if turn.TextHash != textHash {
			return PlannerTurnReply{}, fmt.Errorf("%w: requestId %q was already used for different text", errPlannerInvalid, requestID)
		}
		switch turn.State {
		case plannerTurnDispatched, plannerTurnRunning:
			return PlannerTurnReply{RequestID: requestID, State: turn.State}, nil
		case plannerTurnUnknown:
			return PlannerTurnReply{}, fmt.Errorf("%w: request %q was not resent; its outcome is unknown", errPlannerUnknownTurn, requestID)
		case plannerTurnFailed:
			return PlannerTurnReply{}, fmt.Errorf("%w: request %q failed: %s", errPlannerUnknownTurn, requestID, turn.Reason)
		default:
			return PlannerTurnReply{RequestID: requestID, State: turn.State, StopReason: turn.StopReason}, nil
		}
	}
	if inst.session == "" || inst.backend == nil {
		return PlannerTurnReply{}, fmt.Errorf("%w: planner %s has no live session", errPlannerNotLive, inst.id)
	}
	if inst.rec.ConfirmedMode == "" || inst.rec.ConfirmedMode != inst.rec.RequestedMode {
		return PlannerTurnReply{}, fmt.Errorf("%w: the mode %q was never confirmed by the backend, so no prompt is accepted", errPlannerModeUnconfirmed, inst.rec.RequestedMode)
	}
	if inst.turnRunning {
		return PlannerTurnReply{}, fmt.Errorf("%w: a prompt is already in flight on planner %s", errPlannerBusy, inst.id)
	}

	// The dispatch intent is durable before the backend is called: a bridge
	// that dies from here leaves "dispatched", which recovery rewrites to
	// unknown instead of resending it.
	now := inst.svc.nowUTC()
	inst.rec.Turns = append(inst.rec.Turns, PlannerTurn{RequestID: requestID, TextHash: textHash, State: plannerTurnDispatched, At: now})
	inst.touchLocked()
	if err := inst.persistLocked(); err != nil {
		inst.rec.Turns = inst.rec.Turns[:len(inst.rec.Turns)-1]
		return PlannerTurnReply{}, err
	}
	inst.turnRunning = true
	// The turn owns its context: closing the HTTP connection cancels nothing.
	ctx, cancel := context.WithCancel(context.Background())
	inst.turnCancel = cancel
	backend, session, text := inst.backend, inst.session, req.Text
	go inst.runTurn(ctx, backend, session, requestID, text)
	return PlannerTurnReply{RequestID: requestID, State: plannerTurnDispatched}, nil
}

// runTurn drives one dispatched turn and records what actually happened. It
// never retries: a turn whose outcome the backend could not observe is
// unknown, because it may already have changed the workspace.
func (inst *plannerInstance) runTurn(ctx context.Context, backend WorkerBackend, session, requestID, text string) {
	inst.setTurnState(requestID, plannerTurnRunning, "", "")
	result, err := backend.Prompt(ctx, session, text)
	switch {
	case err == nil:
		inst.setTurnState(requestID, plannerTurnDone, result.StopReason, "")
	case errors.Is(err, ErrWorkerUncertain):
		inst.setTurnState(requestID, plannerTurnUnknown, "", "the backend could not observe the turn's outcome; it was not resent")
	case errors.Is(err, context.Canceled):
		inst.setTurnState(requestID, plannerTurnCancelled, "", "the turn was cancelled")
	default:
		inst.setTurnState(requestID, plannerTurnFailed, "", err.Error())
	}
	inst.mu.Lock()
	inst.turnRunning = false
	inst.turnCancel = nil
	inst.touchLocked()
	inst.mu.Unlock()
}

// setTurnState records one turn's state transition durably.
func (inst *plannerInstance) setTurnState(requestID, state, stopReason, reason string) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	now := inst.svc.nowUTC()
	for i := range inst.rec.Turns {
		if inst.rec.Turns[i].RequestID != requestID {
			continue
		}
		inst.rec.Turns[i].State = state
		inst.rec.Turns[i].StopReason = stopReason
		inst.rec.Turns[i].Reason = reason
		inst.rec.Turns[i].At = now
	}
	inst.touchLocked()
	_ = inst.persistLocked()
}

// cancelTurn interrupts the in-flight turn. It does not cancel this service's
// own turn context: the backend answers a cancel with a cancelled turn, and
// aborting the context locally would turn that answer into an unknown one.
func (inst *plannerInstance) cancelTurn() error {
	inst.mu.Lock()
	backend, session, running := inst.backend, inst.session, inst.turnRunning
	inst.mu.Unlock()
	if !running || backend == nil || session == "" {
		// Nothing in flight: a cancel is a no-op, not an error.
		return nil
	}
	if err := backend.Cancel(context.Background(), session); err != nil {
		if errors.Is(err, ErrWorkerUnsupported) {
			return fmt.Errorf("%w: this backend cannot cancel", errPlannerNotLive)
		}
		return fmt.Errorf("planner cancel: %w", err)
	}
	return nil
}

// end closes the planner: the backend session, the open requests and the
// workspace claim. The journal is left in place.
func (inst *plannerInstance) end() error {
	inst.mu.Lock()
	if inst.ended {
		inst.mu.Unlock()
		return nil
	}
	inst.ended = true
	backend, session, cancel := inst.backend, inst.session, inst.turnCancel
	now := inst.svc.nowUTC()
	open := make([]*plannerPending, 0, len(inst.pending))
	for _, pending := range inst.pending {
		open = append(open, pending)
	}
	inst.pending = map[string]*plannerPending{}
	for _, pending := range open {
		pending.settled = true
	}
	for i := range inst.rec.Requests {
		if inst.rec.Requests[i].State == plannerDecisionPending {
			inst.rec.Requests[i].State = plannerDecisionCancelled
			inst.rec.Requests[i].Reason = "the planner ended before this request was answered"
			inst.rec.Requests[i].At = now
		}
	}
	for i := range inst.rec.Turns {
		if inst.rec.Turns[i].State == plannerTurnDispatched || inst.rec.Turns[i].State == plannerTurnRunning {
			inst.rec.Turns[i].State = plannerTurnCancelled
			inst.rec.Turns[i].Reason = "the planner ended while this turn was in flight"
			inst.rec.Turns[i].At = now
		}
	}
	inst.rec.State = plannerStateEnded
	inst.rec.EndedAt = &now
	inst.rec.Reason = "ended by the client"
	inst.touchLocked()
	persistErr := inst.persistLocked()
	inst.mu.Unlock()

	// Each open request settles once, as cancelled, so its backend stops
	// waiting instead of holding a session that no longer exists.
	for _, pending := range open {
		pending.answer <- plannerAnswer{err: fmt.Errorf("%w: the planner ended", errPlannerNotLive)}
	}
	if cancel != nil {
		cancel()
	}
	if backend != nil && session != "" {
		_ = backend.Close(context.Background(), session)
	}
	inst.appendEvent("planner.ended", nil)
	return persistErr
}

// plannerRoutes registers the planner API and creates the service behind it.
// It is called from NewServer when the server has a fleet; a server without
// one keeps answering 404 for these paths, as it does for any route it does
// not know.
func (s *Server) plannerRoutes() {
	svc := newPlannerService(s.fleet, s.fleet.stateDir)
	svc.routes(s.mux)
}

// routes registers the planner endpoints on mux. It is on the service rather
// than the server so a test drives exactly the routes a running bridge serves.
func (svc *PlannerService) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/planners", svc.handleCreate)
	mux.HandleFunc("GET /api/planners/{id}", svc.handleGet)
	mux.HandleFunc("POST /api/planners/{id}/prompt", svc.handlePrompt)
	mux.HandleFunc("POST /api/planners/{id}/cancel", svc.handleCancel)
	mux.HandleFunc("POST /api/planners/{id}/requests/{requestId}", svc.handleAnswer)
	mux.HandleFunc("GET /api/planners/{id}/events", svc.handleEvents)
	mux.HandleFunc("DELETE /api/planners/{id}", svc.handleEnd)
}

func (svc *PlannerService) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body PlannerCreateRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := svc.Create(r.Context(), body)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (svc *PlannerService) handleGet(w http.ResponseWriter, r *http.Request) {
	view, err := svc.Get(r.PathValue("id"))
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (svc *PlannerService) handlePrompt(w http.ResponseWriter, r *http.Request) {
	var body PlannerPromptRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	// The request context is deliberately not passed on: a browser that closes
	// its connection does not cancel the turn it started.
	reply, err := svc.Prompt(r.PathValue("id"), body)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	status := http.StatusOK
	if reply.State == plannerTurnDispatched || reply.State == plannerTurnRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, reply)
}

func (svc *PlannerService) handleCancel(w http.ResponseWriter, r *http.Request) {
	if err := svc.Cancel(r.PathValue("id")); err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (svc *PlannerService) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var body PlannerAnswerRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(strings.TrimSpace(string(body.Response))) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "response is required"})
		return
	}
	if err := svc.Answer(r.PathValue("id"), r.PathValue("requestId"), body.Response); err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "settled"})
}

func (svc *PlannerService) handleEvents(w http.ResponseWriter, r *http.Request) {
	var after uint64
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "after must be a non-negative integer"})
			return
		}
		after = parsed
	}
	events, err := svc.Events(r.PathValue("id"), after)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	if events == nil {
		events = []PlannerEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

func (svc *PlannerService) handleEnd(w http.ResponseWriter, r *http.Request) {
	if err := svc.End(r.PathValue("id")); err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ended"})
}

// writePlannerError maps a planner error onto the statuses the browser
// branches on: the caller's mistake (400), a planner that does not exist
// (404), a state conflict it must resolve explicitly (409), and a bridge that
// cannot serve planners here (500).
func writePlannerError(w http.ResponseWriter, err error) {
	var occupancy *plannerOccupancyError
	switch {
	case errors.As(err, &occupancy):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":       occupancy.Error(),
			"workspaceId": occupancy.WorkspaceID,
			"occupancy":   occupancy.Occupancy,
			"owners":      occupancy.Owners,
		})
		return
	case errors.Is(err, errPlannerNotFound), errors.Is(err, errPlannerRequestUnknown):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errPlannerInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, errPlannerOccupancy), errors.Is(err, errPlannerBusy),
		errors.Is(err, errPlannerModeUnconfirmed), errors.Is(err, errPlannerUnknownTurn),
		errors.Is(err, errPlannerRequestSettled), errors.Is(err, errPlannerNotLive):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, errPlannerUnavailable):
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}
