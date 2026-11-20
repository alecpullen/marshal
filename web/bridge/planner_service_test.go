package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The planner tests drive the real routes and the real service over a fake
// backend and a fake provisioning seam: no adapter process, no git, no network
// and no package-level state. Every planner, workspace and journal lives under
// a t.TempDir().

// fakePlannerSinks is the sink pair one planner hands its backend. The fake
// keeps them per session, so two planners sharing one fake never journal into
// each other's planner.
type fakePlannerSinks struct {
	events   workerEventSink
	requests workerRequestSink
}

type fakePlannerSession struct {
	spec  WorkerSessionSpec
	caps  WorkerCapabilities
	sinks fakePlannerSinks
	seq   uint64
}

// fakePlannerBackend is the in-process WorkerBackend the planner tests use.
// Everything a test steers — a held turn, a mode the session does not confirm,
// a request that must be answered — is a field on the instance.
type fakePlannerBackend struct {
	mu       sync.Mutex
	pending  []fakePlannerSinks
	sessions map[string]*fakePlannerSession
	nextID   int
	started  []WorkerSessionSpec
	prompts  []string
	cancels  []string
	closed   []string
	answers  map[string]json.RawMessage
	emitted  int

	// omitMode keeps the requested mode out of the session's mode list, so the
	// planner cannot confirm it.
	omitMode bool
	// sessionLoad is the session-load capability the fake reports.
	sessionLoad bool
	// holdPrompts makes Prompt wait on hold before it returns.
	holdPrompts bool
	hold        chan struct{}
	// promptEntered receives (without blocking) once per Prompt call, so a
	// test can wait for a turn to be in flight without sleeping.
	promptEntered chan struct{}
	// promptDone counts completed Prompt calls.
	promptDone int
	// requestOnPrompt makes Prompt ask the planner one permission request
	// before it finishes.
	requestOnPrompt bool
	requestID       string
}

func newFakePlannerBackend() *fakePlannerBackend {
	return &fakePlannerBackend{
		sessions:      map[string]*fakePlannerSession{},
		answers:       map[string]json.RawMessage{},
		hold:          make(chan struct{}),
		promptEntered: make(chan struct{}, 1),
	}
}

// bind records the sinks for the next Start, which is the order the service
// calls them in: build the backend, then start the session.
func (b *fakePlannerBackend) bind(events workerEventSink, requests workerRequestSink) {
	b.mu.Lock()
	b.pending = append(b.pending, fakePlannerSinks{events: events, requests: requests})
	b.mu.Unlock()
}

func (b *fakePlannerBackend) Start(_ context.Context, spec WorkerSessionSpec) (string, WorkerCapabilities, error) {
	b.mu.Lock()
	var sinks fakePlannerSinks
	if len(b.pending) > 0 {
		sinks = b.pending[0]
		b.pending = b.pending[1:]
	}
	b.nextID++
	sessionID := fmt.Sprintf("fake-planner-session-%d", b.nextID)
	caps := WorkerCapabilities{
		Prompt:             true,
		Cancel:             true,
		PermissionRequests: true,
		WorkspaceEdits:     true,
		SessionLoad:        b.sessionLoad,
		Modes:              []string{spec.Mode},
	}
	if b.omitMode {
		caps.Modes = nil
	}
	b.sessions[sessionID] = &fakePlannerSession{spec: spec, caps: caps, sinks: sinks}
	b.started = append(b.started, spec)
	b.mu.Unlock()
	return sessionID, caps, nil
}

func (b *fakePlannerBackend) Prompt(ctx context.Context, sessionID, text string) (WorkerTurnResult, error) {
	b.mu.Lock()
	session := b.sessions[sessionID]
	if session == nil {
		b.mu.Unlock()
		return WorkerTurnResult{}, fmt.Errorf("fake planner: unknown session %q", sessionID)
	}
	b.prompts = append(b.prompts, text)
	block, hold := b.holdPrompts, b.hold
	wantsRequest, requestID := b.requestOnPrompt, b.requestID
	requests := session.sinks.requests
	b.mu.Unlock()

	select {
	case b.promptEntered <- struct{}{}:
	default:
	}
	if block {
		<-hold
	}
	if err := ctx.Err(); err != nil {
		return WorkerTurnResult{}, err
	}
	if wantsRequest {
		if requests == nil {
			return WorkerTurnResult{}, ErrWorkerUnsupported
		}
		answer, err := requests(ctx, sessionID, requestID, "session/request_permission", json.RawMessage(`{"tool":"write"}`))
		if err != nil {
			return WorkerTurnResult{}, err
		}
		b.mu.Lock()
		b.answers[requestID] = answer
		b.mu.Unlock()
	}
	b.emit(sessionID, "turn.finished", map[string]any{"stopReason": "end_turn"})
	b.mu.Lock()
	b.promptDone++
	b.mu.Unlock()
	return WorkerTurnResult{StopReason: "end_turn"}, nil
}

func (b *fakePlannerBackend) Cancel(_ context.Context, sessionID string) error {
	b.mu.Lock()
	b.cancels = append(b.cancels, sessionID)
	b.mu.Unlock()
	b.emit(sessionID, "turn.cancelled", map[string]any{})
	return nil
}

func (b *fakePlannerBackend) Close(_ context.Context, sessionID string) error {
	b.mu.Lock()
	b.closed = append(b.closed, sessionID)
	b.mu.Unlock()
	b.emit(sessionID, "session.closed", map[string]any{})
	return nil
}

func (b *fakePlannerBackend) emit(sessionID, kind string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("fake planner: marshal %q event: %v", kind, err))
	}
	b.mu.Lock()
	session := b.sessions[sessionID]
	if session == nil {
		b.mu.Unlock()
		return
	}
	session.seq++
	sequence := session.seq
	sink := session.sinks.events
	b.emitted++
	b.mu.Unlock()
	if sink != nil {
		sink(sessionID, sequence, kind, raw)
	}
}

func (b *fakePlannerBackend) promptTexts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.prompts...)
}

func (b *fakePlannerBackend) donePrompts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.promptDone
}

func (b *fakePlannerBackend) cancelCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.cancels)
}

func (b *fakePlannerBackend) startSpecs() []WorkerSessionSpec {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]WorkerSessionSpec(nil), b.started...)
}

func (b *fakePlannerBackend) answerFor(requestID string) (json.RawMessage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	answer, ok := b.answers[requestID]
	return answer, ok
}

// fakePlannerProvisioner stands in for the git mirror and working-tree
// machinery: it records the workspace it was asked for and creates the
// directory the service would have handed the backend.
type fakePlannerProvisioner struct {
	stateDir string

	mu    sync.Mutex
	calls []string
	err   error
}

func (p *fakePlannerProvisioner) provision(_ context.Context, workspaceID string, _ Repo) error {
	p.mu.Lock()
	p.calls = append(p.calls, workspaceID)
	err := p.err
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return os.MkdirAll(workspaceDirFor(p.stateDir, workspaceID), 0o700)
}

func (p *fakePlannerProvisioner) provisioned() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// plannerTestEnv is one bridge state directory with a registered project and
// repo, a fake backend and the service under test.
type plannerTestEnv struct {
	t           *testing.T
	fleet       *Fleet
	ws          *Workspace
	stateDir    string
	root        string
	repo        Repo
	backend     *fakePlannerBackend
	provisioner *fakePlannerProvisioner
	svc         *PlannerService
	mux         *http.ServeMux
}

func newPlannerTestEnv(t *testing.T) *plannerTestEnv {
	t.Helper()
	dir := t.TempDir()
	env := &plannerTestEnv{
		t:        t,
		stateDir: filepath.Join(dir, "state"),
		root:     filepath.Join(dir, "project"),
		repo:     Repo{ID: "repo1", URL: "https://example.invalid/marshal.git", Branch: "main", OwnerID: DefaultOwnerID},
	}
	if err := os.MkdirAll(env.root, 0o700); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	env.ws = NewWorkspace(filepath.Join(dir, "workspace.json"))
	if err := env.ws.AddProject(env.root); err != nil {
		t.Fatalf("register project: %v", err)
	}
	if err := env.ws.PutRepo(env.repo); err != nil {
		t.Fatalf("register repo: %v", err)
	}
	if err := env.ws.PutProjectSettings(env.root, ProjectSettings{Intake: ProjectIntake{RepoID: env.repo.ID}}); err != nil {
		t.Fatalf("store project settings: %v", err)
	}
	env.fleet = NewFleet(env.ws, "unused", nil, env.stateDir, Limits{}, "", nil, "")
	t.Cleanup(env.fleet.Close)
	env.provisioner = &fakePlannerProvisioner{stateDir: env.stateDir}
	env.use(newFakePlannerBackend())
	return env
}

// use builds a fresh planner service over the same durable state with the given
// fake backend and installs its routes. Calling it twice is how a test
// simulates a bridge restart.
func (e *plannerTestEnv) use(backend *fakePlannerBackend) *PlannerService {
	e.t.Helper()
	svc := newPlannerService(e.fleet, e.stateDir)
	svc.backendFor = func(spec WorkerSessionSpec, events workerEventSink, requests workerRequestSink) (WorkerBackend, error) {
		backend.bind(events, requests)
		return backend, nil
	}
	svc.provisionWorkspace = e.provisioner.provision
	mux := http.NewServeMux()
	svc.routes(mux)
	e.svc, e.backend, e.mux = svc, backend, mux
	return svc
}

func (e *plannerTestEnv) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var r *http.Request
	if reader == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, reader)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// createBody is a create request for this env's project, with extra fields
// layered on. The values travel as JSON, never as a path a handler trusts.
func (e *plannerTestEnv) createBody(extra map[string]any) string {
	body := map[string]any{"projectRoot": e.root, "repoId": e.repo.ID}
	for key, value := range extra {
		body[key] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatalf("encode create body: %v", err)
	}
	return string(raw)
}

func (e *plannerTestEnv) create(extra map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/planners", e.createBody(extra))
}

func (e *plannerTestEnv) mustCreate(extra map[string]any) PlannerView {
	e.t.Helper()
	w := e.create(extra)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("create planner: status %d, body %s", w.Code, w.Body.String())
	}
	return decodePlannerView(e.t, w)
}

func (e *plannerTestEnv) promptBody(requestID, text string) string {
	raw, err := json.Marshal(PlannerPromptRequest{RequestID: requestID, Text: text})
	if err != nil {
		e.t.Fatalf("encode prompt body: %v", err)
	}
	return string(raw)
}

func (e *plannerTestEnv) prompt(id, requestID, text string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/planners/"+id+"/prompt", e.promptBody(requestID, text))
}

// turnState is the recorded state of one turn, read through the real route.
func (e *plannerTestEnv) turnState(id, requestID string) string {
	e.t.Helper()
	view, err := e.svc.Get(id)
	if err != nil {
		return ""
	}
	for _, turn := range view.Turns {
		if turn.RequestID == requestID {
			return turn.State
		}
	}
	return ""
}

func decodePlannerView(t *testing.T, w *httptest.ResponseRecorder) PlannerView {
	t.Helper()
	var view PlannerView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode planner view: %v (body %s)", err, w.Body.String())
	}
	return view
}

func decodePlannerBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode planner body: %v (body %s)", err, w.Body.String())
	}
	return body
}

func plannerWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestPlannerSeparateWorkspaceDefault: a planner created without a workspace ID
// always gets its own checkout, never another planner's.
func TestPlannerSeparateWorkspaceDefault(t *testing.T) {
	env := newPlannerTestEnv(t)

	first := env.mustCreate(nil)
	second := env.mustCreate(nil)

	if !validPlannerID(first.ID) || !validPlannerID(first.WorkspaceID) {
		t.Fatalf("IDs are not bridge-minted: planner %q workspace %q", first.ID, first.WorkspaceID)
	}
	if first.WorkspaceID == second.WorkspaceID {
		t.Fatalf("two planners share workspace %q", first.WorkspaceID)
	}
	if got := env.provisioner.provisioned(); len(got) != 2 {
		t.Fatalf("provisioned workspaces = %v, want two separate checkouts", got)
	}
	for _, id := range []string{first.WorkspaceID, second.WorkspaceID} {
		if info, err := os.Stat(workspaceDirFor(env.stateDir, id)); err != nil || !info.IsDir() {
			t.Fatalf("workspace %q was not created: %v", id, err)
		}
	}

	// A new session is permissive by default, and only the mode the session
	// confirmed counts.
	if first.RequestedMode != plannerModePermissive {
		t.Errorf("requested mode = %q, want %q", first.RequestedMode, plannerModePermissive)
	}
	if !first.ModeConfirmed || first.ConfirmedMode != plannerModePermissive {
		t.Errorf("confirmed mode = %q (confirmed %v), want %q", first.ConfirmedMode, first.ModeConfirmed, plannerModePermissive)
	}
	if first.State != plannerStateActive || !first.Live {
		t.Errorf("new planner state = %q (live %v), want active and live", first.State, first.Live)
	}
	if first.Occupancy != plannerOccupancyInUse {
		t.Errorf("occupancy = %q, want in_use", first.Occupancy)
	}
	if len(first.Owners) != 1 || first.Owners[0] != first.ID {
		t.Errorf("owners = %v, want just %s", first.Owners, first.ID)
	}
	// The fake backend has no session-load support, so resume is not offered.
	if first.SessionLoad || first.ResumeAvailable || first.ResumeReason == "" {
		t.Errorf("resume = load %v available %v reason %q, want unavailable with a reason",
			first.SessionLoad, first.ResumeAvailable, first.ResumeReason)
	}

	specs := env.backend.startSpecs()
	if len(specs) != 2 {
		t.Fatalf("backend started %d sessions, want 2", len(specs))
	}
	for i, spec := range specs {
		want := []string{first.WorkspaceID, second.WorkspaceID}[i]
		if spec.WorkspaceID != want {
			t.Errorf("session %d workspace = %q, want %q", i, spec.WorkspaceID, want)
		}
		if spec.CWD != workspaceDirFor(env.stateDir, want) {
			t.Errorf("session %d cwd = %q, want the workspace directory", i, spec.CWD)
		}
		if spec.Mode != plannerModePermissive {
			t.Errorf("session %d mode = %q, want %q", i, spec.Mode, plannerModePermissive)
		}
	}

	// The record and its journal are private to the user.
	recordPath, err := env.svc.store.recordPath(first.ID)
	if err != nil {
		t.Fatalf("record path: %v", err)
	}
	if info, err := os.Stat(recordPath); err != nil {
		t.Fatalf("stat record: %v", err)
	} else if perm := info.Mode().Perm(); perm != plannerFileMode {
		t.Errorf("record mode = %o, want %o", perm, plannerFileMode)
	}

	t.Run("the server registers the planner routes", func(t *testing.T) {
		// The production service is what NewServer builds: no fake backend, so
		// a create request with no adapter configured fails naming the variable
		// — proof the route exists and that nothing is silently substituted.
		t.Setenv(plannerAdapterEnv, "")
		before := countWorkspaceDirs(t, env.stateDir)
		srv := NewServer(env.fleet)
		r := testJSONRequest(http.MethodPost, "/api/planners", env.createBody(nil))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("unconfigured planner status = %d, want 500 (body %s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), plannerAdapterEnv) {
			t.Errorf("body does not name %s: %s", plannerAdapterEnv, w.Body.String())
		}
		if after := countWorkspaceDirs(t, env.stateDir); after != before {
			t.Errorf("a refused planner created a workspace: %d -> %d", before, after)
		}
	})
}

func countWorkspaceDirs(t *testing.T, stateDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, "work"))
	if err != nil {
		return 0
	}
	return len(entries)
}

// TestPlannerSharedWorkspaceRequiresExplicitProceed: an in_use workspace is a
// conflict until the caller says proceedShared, and then it is not blocked.
func TestPlannerSharedWorkspaceRequiresExplicitProceed(t *testing.T) {
	env := newPlannerTestEnv(t)
	owner := env.mustCreate(nil)

	w := env.create(map[string]any{"workspaceId": owner.WorkspaceID})
	if w.Code != http.StatusConflict {
		t.Fatalf("shared create status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	body := decodePlannerBody(t, w)
	if got := body["occupancy"]; got != string(plannerOccupancyInUse) {
		t.Errorf("occupancy in conflict body = %v, want %q", got, plannerOccupancyInUse)
	}
	if got := body["workspaceId"]; got != owner.WorkspaceID {
		t.Errorf("conflict names workspace %v, want %q", got, owner.WorkspaceID)
	}
	owners, _ := body["owners"].([]any)
	if len(owners) != 1 || owners[0] != owner.ID {
		t.Errorf("conflict owners = %v, want just %s", owners, owner.ID)
	}
	if got := env.provisioner.provisioned(); len(got) != 1 {
		t.Fatalf("a refused shared create provisioned a workspace: %v", got)
	}
	// The owner is untouched by the refusal.
	if view, err := env.svc.Get(owner.ID); err != nil || !view.Live {
		t.Fatalf("owner after refusal: live=%v err=%v", view.Live, err)
	}

	shared := env.mustCreate(map[string]any{"workspaceId": owner.WorkspaceID, "proceedShared": true})
	if shared.WorkspaceID != owner.WorkspaceID {
		t.Fatalf("shared planner workspace = %q, want %q", shared.WorkspaceID, owner.WorkspaceID)
	}
	if shared.Occupancy != plannerOccupancyInUse {
		t.Errorf("shared planner occupancy = %q, want in_use", shared.Occupancy)
	}
	if len(shared.Owners) != 2 {
		t.Errorf("shared planner owners = %v, want both live planners", shared.Owners)
	}
	if got := env.provisioner.provisioned(); len(got) != 1 {
		t.Errorf("a shared workspace was re-provisioned: %v", got)
	}
}

// TestPlannerUnknownOccupancy: a record whose owner is not live makes its
// workspace unknown, and an unknown workspace also needs proceedShared.
func TestPlannerUnknownOccupancy(t *testing.T) {
	cases := []struct {
		name string
		rec  func(id string, root string, repoID string) PlannerRecord
	}{
		{
			name: "a stale heartbeat",
			rec: func(id, root, repoID string) PlannerRecord {
				return PlannerRecord{ID: id, ProjectRoot: root, RepoID: repoID, RequestedMode: plannerModePermissive, State: plannerStateActive, PID: os.Getpid(), Heartbeat: time.Now().Add(-2 * plannerHeartbeatWindow)}
			},
		},
		{
			name: "another process's record",
			rec: func(id, root, repoID string) PlannerRecord {
				// A different PID wrote it, so this bridge cannot vouch for it
				// even though its heartbeat looks recent.
				return PlannerRecord{ID: id, ProjectRoot: root, RepoID: repoID, RequestedMode: plannerModePermissive, State: plannerStateActive, PID: os.Getpid() + 1, Heartbeat: time.Now()}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newPlannerTestEnv(t)
			id := mustPlannerID(t)
			workspaceID := mustPlannerID(t)
			if err := os.MkdirAll(workspaceDirFor(env.stateDir, workspaceID), 0o700); err != nil {
				t.Fatalf("create workspace: %v", err)
			}
			orphan := tc.rec(id, env.root, env.repo.ID)
			orphan.WorkspaceID = workspaceID
			if orphan.CreatedAt.IsZero() {
				orphan.CreatedAt = time.Now()
				orphan.UpdatedAt = orphan.CreatedAt
			}
			if err := env.svc.store.write(orphan); err != nil {
				t.Fatalf("write orphan record: %v", err)
			}

			w := env.create(map[string]any{"workspaceId": workspaceID})
			if w.Code != http.StatusConflict {
				t.Fatalf("unknown-occupancy create status = %d, want 409 (body %s)", w.Code, w.Body.String())
			}
			body := decodePlannerBody(t, w)
			if got := body["occupancy"]; got != string(plannerOccupancyUnknown) {
				t.Errorf("occupancy = %v, want %q", got, plannerOccupancyUnknown)
			}
			if owners, ok := body["owners"]; ok && owners != nil {
				if list, _ := owners.([]any); len(list) != 0 {
					t.Errorf("owners = %v, want none for an unknown owner", owners)
				}
			}

			shared := env.mustCreate(map[string]any{"workspaceId": workspaceID, "proceedShared": true})
			if shared.WorkspaceID != workspaceID {
				t.Errorf("workspace = %q, want %q", shared.WorkspaceID, workspaceID)
			}
			if !shared.Live {
				t.Errorf("the new planner is not live")
			}
		})
	}

	t.Run("an ended record frees the workspace", func(t *testing.T) {
		env := newPlannerTestEnv(t)
		view := env.mustCreate(nil)
		if err := env.svc.End(view.ID); err != nil {
			t.Fatalf("end planner: %v", err)
		}
		reused := env.mustCreate(map[string]any{"workspaceId": view.WorkspaceID})
		if reused.WorkspaceID != view.WorkspaceID {
			t.Errorf("workspace = %q, want %q", reused.WorkspaceID, view.WorkspaceID)
		}
	})
}

// TestPlannerInvalidProjectOrWorkspace: nothing the caller sends is turned into
// a path or trusted as an identity.
func TestPlannerInvalidProjectOrWorkspace(t *testing.T) {
	env := newPlannerTestEnv(t)
	if err := env.ws.PutRepo(Repo{ID: "repo2", URL: "https://example.invalid/other.git", Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatalf("register second repo: %v", err)
	}
	unknownWorkspace := strings.Repeat("a", plannerIDLength)

	cases := []struct {
		name string
		body map[string]any
	}{
		{name: "unregistered project", body: map[string]any{"projectRoot": filepath.Join(t.TempDir(), "elsewhere")}},
		{name: "missing repo id", body: map[string]any{"repoId": ""}},
		{name: "unknown repo", body: map[string]any{"repoId": "no-such-repo"}},
		{name: "repo belongs to no project", body: map[string]any{"repoId": "repo2"}},
		{name: "path instead of workspace id", body: map[string]any{"workspaceId": "/etc"}},
		{name: "workspace id the bridge never minted", body: map[string]any{"workspaceId": unknownWorkspace}},
		{name: "unsupported mode", body: map[string]any{"mode": "yolo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := env.create(tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "error") {
				t.Errorf("body has no error: %s", w.Body.String())
			}
		})
	}
	if got := env.provisioner.provisioned(); len(got) != 0 {
		t.Fatalf("invalid requests provisioned workspaces: %v", got)
	}
	if entries, err := os.ReadDir(filepath.Join(env.stateDir, "planners")); err == nil && len(entries) != 0 {
		t.Errorf("invalid requests wrote planner records: %v", entries)
	}

	t.Run("unknown and malformed planner ids are 404", func(t *testing.T) {
		for _, id := range []string{unknownWorkspace, ""} {
			w := env.do(http.MethodGet, "/api/planners/"+id, "")
			if w.Code != http.StatusNotFound {
				t.Fatalf("GET planner %q = %d, want 404", id, w.Code)
			}
		}
		// A traversal attempt never reaches the store: the mux normalises the
		// path before a handler sees it, and the ID validation refuses it even
		// when one does.
		if validPlannerID("../../etc/passwd") {
			t.Fatal("a traversal path passed planner ID validation")
		}
		if _, err := env.svc.store.recordPath("../../etc/passwd"); err == nil {
			t.Fatal("the store built a path from a traversal string")
		}
	})
}

// TestPlannerDisconnectDoesNotCancel: a turn runs on a context the service
// owns, so losing the HTTP connection cancels nothing.
func TestPlannerDisconnectDoesNotCancel(t *testing.T) {
	env := newPlannerTestEnv(t)
	env.backend.holdPrompts = true
	view := env.mustCreate(nil)

	r := httptest.NewRequest(http.MethodPost, "/api/planners/"+view.ID+"/prompt",
		strings.NewReader(env.promptBody("turn-1", "make the change")))
	ctx, cancel := context.WithCancel(context.Background())
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	env.mux.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}

	// The browser goes away while the turn is still running.
	cancel()
	select {
	case <-env.backend.promptEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("the backend never saw the turn")
	}
	if got := env.backend.cancelCount(); got != 0 {
		t.Fatalf("closing the connection cancelled the turn (%d cancels)", got)
	}

	close(env.backend.hold)
	plannerWaitFor(t, "the turn to finish", func() bool { return env.turnState(view.ID, "turn-1") == plannerTurnDone })
	if got := env.backend.cancelCount(); got != 0 {
		t.Errorf("cancel count = %d after the turn finished, want 0", got)
	}
}

// TestPlannerRequestSettlement: an agent request settles exactly once.
func TestPlannerRequestSettlement(t *testing.T) {
	env := newPlannerTestEnv(t)
	env.backend.requestOnPrompt = true
	env.backend.requestID = "req-1"
	view := env.mustCreate(nil)

	w := env.prompt(view.ID, "turn-1", "write the file")
	if w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	plannerWaitFor(t, "the request to be recorded as pending", func() bool {
		current, err := env.svc.Get(view.ID)
		if err != nil {
			return false
		}
		for _, decision := range current.Requests {
			if decision.RequestID == "req-1" && decision.State == plannerDecisionPending {
				return true
			}
		}
		return false
	})

	answer := `{"response":{"allow":true}}`
	w = env.do(http.MethodPost, "/api/planners/"+view.ID+"/requests/req-1", answer)
	if w.Code != http.StatusOK {
		t.Fatalf("answer status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	w = env.do(http.MethodPost, "/api/planners/"+view.ID+"/requests/req-1", answer)
	if w.Code != http.StatusConflict {
		t.Fatalf("second answer status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}

	plannerWaitFor(t, "the turn to finish", func() bool { return env.turnState(view.ID, "turn-1") == plannerTurnDone })
	delivered, ok := env.backend.answerFor("req-1")
	if !ok {
		t.Fatal("the backend never received the answer")
	}
	var chosen map[string]bool
	if err := json.Unmarshal(delivered, &chosen); err != nil || !chosen["allow"] {
		t.Fatalf("backend received %s, want the chosen response", delivered)
	}

	settled, err := env.svc.Get(view.ID)
	if err != nil {
		t.Fatalf("get planner: %v", err)
	}
	answered := 0
	for _, decision := range settled.Requests {
		if decision.RequestID != "req-1" {
			continue
		}
		answered++
		if decision.State != plannerDecisionAnswered {
			t.Errorf("decision state = %q, want %q", decision.State, plannerDecisionAnswered)
		}
		if !strings.Contains(string(decision.Response), `"allow":true`) {
			t.Errorf("recorded response = %s, want the answered choice", decision.Response)
		}
	}
	if answered != 1 {
		t.Errorf("recorded %d decisions for req-1, want exactly one", answered)
	}

	t.Run("an unknown request id is 404", func(t *testing.T) {
		w := env.do(http.MethodPost, "/api/planners/"+view.ID+"/requests/never-opened", answer)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
		}
	})
}

// TestPlannerRestartDoesNotReplay: a bridge that comes back finds an in-flight
// turn unknown, and never sends it again.
func TestPlannerRestartDoesNotReplay(t *testing.T) {
	env := newPlannerTestEnv(t)
	env.backend.holdPrompts = true
	view := env.mustCreate(nil)
	firstBackend := env.backend

	if w := env.prompt(view.ID, "turn-1", "start something long"); w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	// Wait until the turn is observably running, so nothing later can race the
	// recovery rewrite.
	plannerWaitFor(t, "the turn to be running", func() bool { return env.turnState(view.ID, "turn-1") == plannerTurnRunning })

	// A new bridge generation over the same state directory.
	env.use(newFakePlannerBackend())
	w := env.do(http.MethodGet, "/api/planners/"+view.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET after restart = %d (body %s)", w.Code, w.Body.String())
	}
	recovered := decodePlannerView(t, w)
	if recovered.Live {
		t.Errorf("a planner from the previous bridge reports live")
	}
	if recovered.State != plannerStateUnknown {
		t.Errorf("state after restart = %q, want %q", recovered.State, plannerStateUnknown)
	}
	if recovered.Occupancy != plannerOccupancyUnknown {
		t.Errorf("occupancy after restart = %q, want %q", recovered.Occupancy, plannerOccupancyUnknown)
	}
	if recovered.ResumeAvailable {
		t.Errorf("resume is offered after a restart it cannot perform")
	}
	if recovered.ResumeReason == "" {
		t.Errorf("no reason given for an unavailable resume")
	}
	var turn PlannerTurn
	for _, candidate := range recovered.Turns {
		if candidate.RequestID == "turn-1" {
			turn = candidate
		}
	}
	if turn.State != plannerTurnUnknown {
		t.Fatalf("turn state after restart = %q, want %q", turn.State, plannerTurnUnknown)
	}
	if !strings.Contains(turn.Reason, "not resent") {
		t.Errorf("turn reason = %q, want it to say the turn was not resent", turn.Reason)
	}

	// The same request is refused rather than dispatched into a dead session.
	w = env.do(http.MethodPost, "/api/planners/"+view.ID+"/prompt", env.promptBody("turn-1", "start something long"))
	if w.Code != http.StatusConflict {
		t.Fatalf("replay after restart = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if got := env.backend.promptTexts(); len(got) != 0 {
		t.Errorf("the new bridge dispatched %v, want nothing", got)
	}
	if got := firstBackend.promptTexts(); len(got) != 1 {
		t.Errorf("the old backend dispatched %d turns, want 1 (never resent)", len(got))
	}

	// Let the abandoned turn finish so no goroutine outlives the test.
	close(firstBackend.hold)
	plannerWaitFor(t, "the abandoned turn to finish", func() bool { return firstBackend.donePrompts() == 1 })
}

// TestPlannerModeNotConfirmedBlocksPrompt: an access mode the session did not
// confirm blocks prompts instead of being assumed.
func TestPlannerModeNotConfirmedBlocksPrompt(t *testing.T) {
	env := newPlannerTestEnv(t)
	env.backend.omitMode = true
	view := env.mustCreate(nil)

	if view.ConfirmedMode != "" || view.ModeConfirmed {
		t.Fatalf("confirmed mode = %q (confirmed %v), want unconfirmed", view.ConfirmedMode, view.ModeConfirmed)
	}
	if view.RequestedMode != plannerModePermissive {
		t.Errorf("requested mode = %q, want %q", view.RequestedMode, plannerModePermissive)
	}
	w := env.prompt(view.ID, "turn-1", "hello")
	if w.Code != http.StatusConflict {
		t.Fatalf("prompt status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "never confirmed") {
		t.Errorf("body does not explain the unconfirmed mode: %s", w.Body.String())
	}
	if got := env.backend.promptTexts(); len(got) != 0 {
		t.Errorf("a prompt reached the backend under an unconfirmed mode: %v", got)
	}
	if got := env.turnState(view.ID, "turn-1"); got != "" {
		t.Errorf("turn state = %q, want no turn recorded", got)
	}
}

// TestPlannerConcurrentPromptsRejected: one turn at a time per planner, and a
// repeated request ID replays instead of dispatching.
func TestPlannerConcurrentPromptsRejected(t *testing.T) {
	env := newPlannerTestEnv(t)
	env.backend.holdPrompts = true
	view := env.mustCreate(nil)

	if w := env.prompt(view.ID, "turn-1", "one"); w.Code != http.StatusAccepted {
		t.Fatalf("first prompt = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	select {
	case <-env.backend.promptEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("the backend never saw the first turn")
	}

	if w := env.prompt(view.ID, "turn-2", "two"); w.Code != http.StatusConflict {
		t.Fatalf("second prompt = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	// The same request ID while it is in flight is a replay, not a turn.
	w := env.prompt(view.ID, "turn-1", "one")
	if w.Code != http.StatusAccepted {
		t.Fatalf("replayed prompt = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	reply := decodePlannerBody(t, w)
	// The replay reports the recorded state; the turn may already have moved
	// from dispatched to running. What matters is that nothing was re-sent.
	if got := reply["state"]; got != plannerTurnDispatched && got != plannerTurnRunning {
		t.Errorf("replayed state = %v, want the recorded in-flight state", got)
	}
	if got := env.backend.promptTexts(); len(got) != 1 {
		t.Fatalf("backend dispatched %v, want just the first turn", got)
	}

	close(env.backend.hold)
	plannerWaitFor(t, "the first turn to finish", func() bool { return env.turnState(view.ID, "turn-1") == plannerTurnDone })

	if w := env.prompt(view.ID, "turn-3", "three"); w.Code != http.StatusAccepted {
		t.Fatalf("third prompt = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	plannerWaitFor(t, "the third turn to finish", func() bool { return env.turnState(view.ID, "turn-3") == plannerTurnDone })
	if got := env.backend.promptTexts(); len(got) != 2 || got[1] != "three" {
		t.Errorf("dispatched = %v, want the first and third turns only", got)
	}
}

// TestPlannerEventsReplayAndBounded: the journal replays by sequence and is
// bounded in both directions.
func TestPlannerEventsReplayAndBounded(t *testing.T) {
	env := newPlannerTestEnv(t)
	view := env.mustCreate(nil)
	inst, ok := env.svc.live(view.ID)
	if !ok {
		t.Fatal("the planner is not live")
	}

	const extra = 10
	for i := 0; i < plannerEventRetained+extra; i++ {
		inst.appendEvent("turn.text", map[string]string{"text": fmt.Sprintf("chunk-%d", i)})
	}
	// A payload past the per-event bound is stored as a marker, not a failure.
	inst.appendEvent("turn.text", map[string]string{"text": strings.Repeat("z", plannerEventPayloadLimit+64)})

	total := uint64(1 + plannerEventRetained + extra + 1)
	events := replayEvents(t, env, view.ID, 0)
	if len(events) != plannerEventRetained {
		t.Fatalf("retained events = %d, want %d", len(events), plannerEventRetained)
	}
	if want := total - plannerEventRetained + 1; events[0].Sequence != want {
		t.Errorf("first retained sequence = %d, want %d", events[0].Sequence, want)
	}
	if last := events[len(events)-1]; last.Sequence != total {
		t.Errorf("last sequence = %d, want %d", last.Sequence, total)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Sequence <= events[i-1].Sequence {
			t.Fatalf("sequences are not strictly increasing at %d: %d then %d", i, events[i-1].Sequence, events[i].Sequence)
		}
	}

	last := events[len(events)-1]
	if !last.Truncated {
		t.Errorf("the oversized payload is not marked truncated")
	}
	if !strings.Contains(string(last.Data), `"truncated":true`) {
		t.Errorf("truncated payload = %s, want a marker", last.Data)
	}
	if strings.Contains(string(last.Data), strings.Repeat("z", 32)) {
		t.Errorf("the oversized payload was stored: %s", last.Data)
	}

	if newer := replayEvents(t, env, view.ID, total); len(newer) != 0 {
		t.Errorf("replay after the newest sequence returned %d events, want none", len(newer))
	}

	// A replay must not carry secret material: the journal holds no environment.
	journalPath, err := env.svc.store.journalPath(view.ID)
	if err != nil {
		t.Fatalf("journal path: %v", err)
	}
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	for _, secret := range []string{"MARSHAL_", "API_KEY", "TOKEN", "PASSWORD"} {
		if strings.Contains(strings.ToUpper(string(raw)), secret) {
			t.Errorf("journal contains %q", secret)
		}
	}

	t.Run("a bad after value is 400", func(t *testing.T) {
		w := env.do(http.MethodGet, "/api/planners/"+view.ID+"/events?after=later", "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
		}
	})
}

func replayEvents(t *testing.T, env *plannerTestEnv, id string, after uint64) []PlannerEvent {
	t.Helper()
	path := "/api/planners/" + id + "/events"
	if after > 0 {
		path += "?after=" + strconv.FormatUint(after, 10)
	}
	w := env.do(http.MethodGet, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("events replay = %d (body %s)", w.Code, w.Body.String())
	}
	var events []PlannerEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v (body %s)", err, w.Body.String())
	}
	return events
}

func mustPlannerID(t *testing.T) string {
	t.Helper()
	id, err := newPlannerID()
	if err != nil {
		t.Fatalf("generate planner id: %v", err)
	}
	return id
}
