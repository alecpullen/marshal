package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The integration proof drives the whole planner path over a fake ACP adapter:
// the real PlannerService, the real routes, the real snapshot store, the real
// factory store and seed, and the real ACP worker backend, with the scripted
// in-process agent from worker_acp_test.go standing in for claude-code-acp.
//
// What it proves is the seam and the planner's lifecycle rules — session
// setup, both access modes, request routing, an accepted turn outliving the
// browser, an unobservable turn staying unknown, snapshot review, the factory
// draft and the selected-edit seed. What it does not prove is anything about
// Claude itself: no adapter binary is installed here and no provider is
// contacted, which is why every real-Claude row in
// docs/studio-fleet-factory/claude-acp-capability-report.md is untested.
//
// Every wait below is bounded and condition-based, and every answer the fake
// adapter gives is scripted by the test: there is no sleep that a race could
// hide behind.

// acpIntegrationEnv is a planner test environment whose backends are real ACP
// worker backends over scripted agents, one agent per planner, so a test can
// end exactly one planner's adapter process.
type acpIntegrationEnv struct {
	*plannerTestEnv
	agents []*scriptedAgent
	next   int
}

func newACPIntegrationEnv(t *testing.T) *acpIntegrationEnv {
	t.Helper()
	env := newPlannerTestEnv(t)
	e := &acpIntegrationEnv{
		plannerTestEnv: env,
		agents: []*scriptedAgent{
			newModeScriptedAgent("sess-a"),
			newModeScriptedAgent("sess-b"),
		},
	}
	svc := newPlannerService(env.fleet, env.stateDir)
	svc.backendFor = func(_ WorkerSessionSpec, events workerEventSink, requests workerRequestSink) (WorkerBackend, error) {
		if e.next >= len(e.agents) {
			return nil, fmt.Errorf("the test scripted %d adapter processes, want another", len(e.agents))
		}
		agent := e.agents[e.next]
		e.next++
		return newACPWorkerBackend(agent, events, requests), nil
	}
	svc.provisionWorkspace = env.provisioner.provision
	mux := http.NewServeMux()
	svc.routes(mux)
	env.svc, env.mux = svc, mux
	return e
}

// newModeScriptedAgent is a scripted adapter that offers both planner access
// modes, so a planner can confirm either one.
func newModeScriptedAgent(sessionID string) *scriptedAgent {
	agent := newScriptedAgent()
	agent.modes = []string{plannerModePermissive, plannerModeRestricted}
	agent.currentMode = plannerModeRestricted
	agent.sessionIDs = []string{sessionID}
	return agent
}

// awaitDecision waits until the planner has recorded one agent request as
// pending, which is the point at which a browser can answer it.
func (e *acpIntegrationEnv) awaitDecision(id, requestID string) {
	waitFor(e.t, acpTestTimeout, "the planner to record request "+requestID+" as pending", func() bool {
		view, err := e.svc.Get(id)
		if err != nil {
			return false
		}
		for _, decision := range view.Requests {
			if decision.RequestID == requestID && decision.State == plannerDecisionPending {
				return true
			}
		}
		return false
	})
}

// awaitTurn waits until one recorded turn reaches want.
func (e *acpIntegrationEnv) awaitTurn(id, requestID, want string) {
	waitFor(e.t, acpTestTimeout, "turn "+requestID+" to reach "+want, func() bool {
		view, err := e.svc.Get(id)
		if err != nil {
			return false
		}
		for _, turn := range view.Turns {
			if turn.RequestID == requestID {
				return turn.State == want
			}
		}
		return false
	})
}

// journalHas reports whether the planner's journal holds an event of kind
// whose payload carries want.
func (e *acpIntegrationEnv) journalHas(id, kind, want string) bool {
	events, err := e.svc.Events(id, 0)
	if err != nil {
		e.t.Fatalf("replay planner journal: %v", err)
	}
	for _, event := range events {
		if event.Kind == kind && strings.Contains(string(event.Data), want) {
			return true
		}
	}
	return false
}

// assertSessionStart pins the handshake and the mode negotiation the adapter
// must see for one planner, and the capabilities the planner recorded.
func (e *acpIntegrationEnv) assertSessionStart(view PlannerView, mode, sessionID string, agent *scriptedAgent) {
	e.t.Helper()
	if view.RequestedMode != mode {
		e.t.Errorf("planner %s requested mode = %q, want %q", view.ID, view.RequestedMode, mode)
	}
	if view.ConfirmedMode != mode || !view.ModeConfirmed {
		e.t.Errorf("planner %s confirmed mode = %q (confirmed %v), want %q", view.ID, view.ConfirmedMode, view.ModeConfirmed, mode)
	}
	if view.SessionID != sessionID {
		e.t.Errorf("planner %s session = %q, want the adapter's %q", view.ID, view.SessionID, sessionID)
	}
	if got := strings.Join(view.Capabilities.Modes, ","); got != plannerModePermissive+","+plannerModeRestricted {
		e.t.Errorf("planner %s capability modes = %q, want the modes the session reported", view.ID, got)
	}
	if !view.Capabilities.Prompt || !view.Capabilities.Cancel || !view.Capabilities.SessionLoad || !view.Capabilities.PermissionRequests {
		e.t.Errorf("planner %s capabilities = %+v, want prompt, cancel, session load and permission requests", view.ID, view.Capabilities)
	}
	if view.Capabilities.WorkspaceEdits {
		e.t.Errorf("planner %s claims workspace edits the adapter never advertised", view.ID)
	}

	init := agent.framesFor("initialize")
	if len(init) != 1 {
		e.t.Fatalf("initialize frames = %d, want 1", len(init))
	}
	for _, want := range []string{`"protocolVersion":1`, `"clientCapabilities":{}`} {
		if !strings.Contains(init[0], want) {
			e.t.Errorf("initialize params = %s, want it to carry %s", init[0], want)
		}
	}
	created := agent.framesFor("session/new")
	if len(created) != 1 {
		e.t.Fatalf("session/new frames = %d, want 1", len(created))
	}
	dir := workspaceDirFor(e.stateDir, view.WorkspaceID)
	abs, err := filepath.Abs(dir)
	if err != nil {
		e.t.Fatalf("abs %q: %v", dir, err)
	}
	if !strings.Contains(created[0], `"cwd":"`+abs+`"`) {
		e.t.Errorf("session/new params = %s, want the planner's workspace %q", created[0], abs)
	}
	if !strings.Contains(created[0], `"mcpServers":[]`) {
		e.t.Errorf("session/new params = %s, want an explicit empty mcpServers", created[0])
	}
	modes := agent.framesFor("session/set_mode")
	if len(modes) != 1 {
		e.t.Fatalf("session/set_mode frames = %d, want 1", len(modes))
	}
	for _, want := range []string{`"modeId":"` + mode + `"`, `"sessionId":"` + sessionID + `"`} {
		if !strings.Contains(modes[0], want) {
			e.t.Errorf("session/set_mode params = %s, want it to carry %s", modes[0], want)
		}
	}
	if !e.journalHas(view.ID, "planner.started", `"mode":"`+mode+`"`) {
		e.t.Errorf("planner %s journal holds no planner.started for mode %q", view.ID, mode)
	}
	if !e.journalHas(view.ID, workerEventSessionMode, `"modeId":"`+mode+`"`) {
		e.t.Errorf("planner %s journal holds no session.mode for %q", view.ID, mode)
	}
}

// TestACPWorkerIntegration is the end-to-end proof: one bridge, one fake
// adapter per planner, and every planner, factory and seed behaviour the
// capability report cites as passing.
func TestACPWorkerIntegration(t *testing.T) {
	e := newACPIntegrationEnv(t)
	if e.fleet.git == nil {
		t.Skip("git is not installed")
	}

	// Session setup, in both access modes. A new planner asks for the
	// permissive mode by default; an explicit "default" asks for the
	// restricted one. Nothing is dispatched under a mode the session did not
	// confirm.
	permissive := e.mustCreate(nil)
	restricted := e.mustCreate(map[string]any{"mode": plannerModeRestricted})
	e.assertSessionStart(permissive, plannerModePermissive, "sess-a", e.agents[0])
	e.assertSessionStart(restricted, plannerModeRestricted, "sess-b", e.agents[1])
	if got := e.agents[0].methodsSeen(); strings.Join(got, ",") != "initialize,session/new,session/set_mode" {
		t.Fatalf("adapter frames = %q, want the session set up before anything is dispatched", got)
	}

	// A permission request and a question reach the request sink under the
	// adapter's own ids, the browser's raw answer goes back verbatim, and each
	// settles exactly once.
	adapter := e.agents[0]
	held := make(chan json.RawMessage, 4)
	adapter.setPromptHook(func(g *scriptedGen, id json.RawMessage, _ json.RawMessage) *acpReply {
		g.request(9000, acpMethodRequestPermission, map[string]any{
			"sessionId":  "sess-a",
			"toolCallId": "tc-perm",
			"toolName":   "shell.run",
			"options":    []map[string]any{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}},
		})
		held <- id
		return &acpReply{hold: true}
	})
	if w := e.prompt(permissive.ID, "turn-permission", "run the tests"); w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	permissionTurn := <-held
	e.awaitDecision(permissive.ID, "tc-perm")
	answer := `{"response":{"optionId":"allow"}}`
	if w := e.do(http.MethodPost, "/api/planners/"+permissive.ID+"/requests/tc-perm", answer); w.Code != http.StatusOK {
		t.Fatalf("answer status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if w := e.do(http.MethodPost, "/api/planners/"+permissive.ID+"/requests/tc-perm", answer); w.Code != http.StatusConflict {
		t.Fatalf("second answer status = %d, want 409: a request settles once", w.Code)
	}
	adapter.current().respond(permissionTurn, map[string]any{"stopReason": "end_turn"})
	e.awaitTurn(permissive.ID, "turn-permission", plannerTurnDone)
	waitFor(t, acpTestTimeout, "the adapter to receive the permission answer verbatim", func() bool {
		got, ok := adapter.answerFor("9000")
		return ok && strings.Contains(got.result, `"optionId":"allow"`)
	})
	if !e.journalHas(permissive.ID, workerEventTurnPermission, `"requestId":"tc-perm"`) {
		t.Error("the permission request was not journaled under its own id")
	}

	adapter.setPromptHook(func(g *scriptedGen, id json.RawMessage, _ json.RawMessage) *acpReply {
		g.request("q-1", acpMethodRequestQuestion, map[string]any{
			"sessionId":  "sess-a",
			"questionId": "q-1",
			"questions":  []map[string]any{{"question": "proceed?"}},
		})
		held <- id
		return &acpReply{hold: true}
	})
	if w := e.prompt(permissive.ID, "turn-question", "carry on"); w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	questionTurn := <-held
	e.awaitDecision(permissive.ID, "q-1")
	question := `{"response":{"answers":[{"question":"proceed?","answer":"yes"}]}}`
	if w := e.do(http.MethodPost, "/api/planners/"+permissive.ID+"/requests/q-1", question); w.Code != http.StatusOK {
		t.Fatalf("answer status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if w := e.do(http.MethodPost, "/api/planners/"+permissive.ID+"/requests/q-1", question); w.Code != http.StatusConflict {
		t.Fatalf("second answer status = %d, want 409", w.Code)
	}
	adapter.current().respond(questionTurn, map[string]any{"stopReason": "end_turn"})
	e.awaitTurn(permissive.ID, "turn-question", plannerTurnDone)
	waitFor(t, acpTestTimeout, "the adapter to receive the question answer verbatim", func() bool {
		got, ok := adapter.answerFor(`"q-1"`)
		return ok && strings.Contains(got.result, `"answer":"yes"`)
	})
	if !e.journalHas(permissive.ID, workerEventTurnQuestion, `"requestId":"q-1"`) {
		t.Error("the question was not journaled under its own id")
	}

	// An accepted prompt survives the browser's disconnect: the turn runs on a
	// context the service owns, so the connection going away cancels nothing.
	adapter.setPromptHook(func(_ *scriptedGen, id json.RawMessage, _ json.RawMessage) *acpReply {
		held <- id
		return &acpReply{hold: true}
	})
	r := httptest.NewRequest(http.MethodPost, "/api/planners/"+permissive.ID+"/prompt",
		strings.NewReader(e.promptBody("turn-disconnect", "make the change")))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	disconnectTurn := <-held
	cancel()
	adapter.current().respond(disconnectTurn, map[string]any{"stopReason": "end_turn"})
	e.awaitTurn(permissive.ID, "turn-disconnect", plannerTurnDone)
	if n := adapter.countFrames(acpMethodSessionCancel); n != 0 {
		t.Fatalf("session/cancel frames = %d, want none: a closed connection cancels nothing", n)
	}

	// A turn the backend could not observe is unknown, and is never replayed
	// into the replacement adapter process.
	adapter.setPromptHook(func(_ *scriptedGen, id json.RawMessage, _ json.RawMessage) *acpReply {
		held <- id
		return &acpReply{hold: true}
	})
	if w := e.prompt(permissive.ID, "turn-lost", "change the tree"); w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	<-held
	dispatched := adapter.countFrames(acpMethodSessionPrompt)
	adapter.hangUp()
	e.awaitTurn(permissive.ID, "turn-lost", plannerTurnUnknown)
	if !e.journalHas(permissive.ID, workerEventTurnUncertain, `"reason"`) {
		t.Error("the lost turn was not journaled as uncertain")
	}
	// The same request ID reports the recorded unknown outcome instead of
	// sending the turn again.
	if w := e.prompt(permissive.ID, "turn-lost", "change the tree"); w.Code != http.StatusConflict {
		t.Fatalf("replaying the lost turn = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if got := adapter.countFrames(acpMethodSessionPrompt); got != dispatched {
		t.Fatalf("session/prompt frames = %d, want the %d already dispatched (never replayed)", got, dispatched)
	}
	// The planner is still live: a lost turn is not a lost planner.
	if view, err := e.svc.Get(permissive.ID); err != nil || !view.Live {
		t.Fatalf("planner after the process loss: live=%v err=%v", view.Live, err)
	}

	// Snapshot: one selected file and one rejected path, reviewed from the
	// bridge's own store, then bound to a factory draft and seeded.
	dir := workspaceDirFor(e.stateDir, permissive.WorkspaceID)
	mustGit(t, dir, "init", "--initial-branch=main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	snapshotWriteFile(t, dir, "a.txt", "one\ntwo\n", 0o644)
	snapshotWriteFile(t, dir, "keep.txt", "keep\n", 0o644)
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "base")
	snapshotWriteFile(t, dir, "a.txt", "one\nchanged\n", 0o644)
	// An unselected change stays in the workspace and must never reach a seed.
	snapshotWriteFile(t, dir, "keep.txt", "keep changed\n", 0o644)

	capture := e.mustCapture(permissive.ID, PlannerSnapshotRequest{
		Plan:  "the approved plan\n",
		Spec:  "the approved spec\n",
		Paths: []string{"a.txt", "../escape.txt"},
	})
	if len(capture.Files) != 1 || capture.Files[0].Path != "a.txt" {
		t.Fatalf("captured %+v, want just a.txt", capture.Files)
	}
	if len(capture.Rejected) != 1 || capture.Rejected[0].Path != "../escape.txt" {
		t.Fatalf("rejected %+v, want the escaping path reported", capture.Rejected)
	}
	if !strings.Contains(capture.Rejected[0].Reason, "escapes") {
		t.Errorf("rejection reason = %q, want it to say the path escapes the workspace", capture.Rejected[0].Reason)
	}
	if capture.BaseCommit != snapshotGitOut(t, dir, "rev-parse", "HEAD") {
		t.Errorf("snapshot base = %q, want the workspace HEAD", capture.BaseCommit)
	}
	files := snapshotFilesByPath(capture)
	if !strings.Contains(files["a.txt"].Diff, "+changed") {
		t.Errorf("a.txt diff = %q, want the captured change", files["a.txt"].Diff)
	}

	// The factory checkout is cloned from the approved commit before the
	// planner's workspace goes away: what a seed applies is the snapshot's own
	// bytes, and this checkout only supplies the base revision to apply them
	// onto.
	source := filepath.Join(t.TempDir(), "captured")
	gitText(t, "", "clone", dir, source)
	if got := snapshotGitOut(t, source, "rev-parse", "HEAD"); got != capture.BaseCommit {
		t.Fatalf("the captured checkout is at %q, want the approved base %q", got, capture.BaseCommit)
	}

	// The review is served after the workspace it came from is gone: the
	// snapshot is the durable record, not the checkout.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	review := e.getSnapshot(permissive.ID, capture.ID)
	if review.Code != http.StatusOK {
		t.Fatalf("GET snapshot status = %d, want 200 (body %s)", review.Code, review.Body.String())
	}
	var reviewed PlannerSnapshotView
	if err := json.Unmarshal(review.Body.Bytes(), &reviewed); err != nil {
		t.Fatalf("decode snapshot review: %v", err)
	}
	if reviewed.SHA256 != capture.SHA256 || len(reviewed.Files) != 1 || reviewed.Files[0].Diff == "" {
		t.Fatalf("review = %+v, want the captured snapshot with its diff", reviewed)
	}
	for _, hostPath := range []string{dir, e.stateDir, e.root} {
		if strings.Contains(review.Body.String(), hostPath) {
			t.Errorf("the review leaks the host path %q", hostPath)
		}
	}

	// The handoff creates the factory draft the review approves, carrying the
	// captured text and the exact selection.
	store, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("open factory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.svc.setFactory(&recordingFactory{store: store})
	handoff := e.handoff(permissive.ID, capture.ID, handoffBody(e.plannerTestEnv, "handoff-1"))
	if handoff.Code != http.StatusCreated {
		t.Fatalf("handoff status = %d, want 201 (body %s)", handoff.Code, handoff.Body.String())
	}
	item := decodeHandoffItem(t, handoff)
	plan := snapshotArtifactOfKind(t, capture, plannerArtifactPlan)
	spec := snapshotArtifactOfKind(t, capture, plannerArtifactSpec)
	if item.Draft.Plan != plan.Content || item.Draft.Spec != spec.Content {
		t.Fatalf("draft text = %q/%q, want the captured text %q/%q", item.Draft.Plan, item.Draft.Spec, plan.Content, spec.Content)
	}
	if item.Draft.EditSnapshot == nil || item.Draft.EditSnapshot.SHA256 != capture.SHA256 {
		t.Fatalf("draft selection = %+v, want snapshot %s", item.Draft.EditSnapshot, capture.SHA256)
	}
	if got := seedPaths(item.Draft.EditSnapshot); strings.Join(got, ",") != "a.txt" {
		t.Fatalf("the draft carries paths %v, want the one selected file", got)
	}

	// A seed applies exactly that selection onto a disposable checkout at the
	// approved base, and nothing else.
	fixture := newSeedFixtureFromClone(t, source, e.stateDir)
	if got := gitText(t, fixture.work, "rev-parse", "HEAD"); got != capture.BaseCommit {
		t.Fatalf("the factory checkout is at %q, want the approved base %q", got, capture.BaseCommit)
	}
	worker := fixture.worker(item.ID)
	seedItem := fixture.item(item.ID, capture.BaseCommit, item.Draft.EditSnapshot)
	head, _, err := fixture.g.Seed(context.Background(), seedItem, worker)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	seeded, err := os.ReadFile(filepath.Join(fixture.work, "a.txt"))
	if err != nil || string(seeded) != "one\nchanged\n" {
		t.Fatalf("seeded a.txt = %q, %v; want the captured bytes", seeded, err)
	}
	untouched, err := os.ReadFile(filepath.Join(fixture.work, "keep.txt"))
	if err != nil || string(untouched) != "keep\n" {
		t.Fatalf("unselected keep.txt = %q, %v; want the base bytes", untouched, err)
	}
	want := []string{"a.txt", "docs/marshal-factory/" + item.ID + "/plan.md", "docs/marshal-factory/" + item.ID + "/spec.md"}
	if got := seedCommittedPaths(t, fixture.work, head); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("seeded commit paths = %v, want %v", got, want)
	}

	// The adapter path is the only installation diagnostic this environment
	// can offer: no binary is installed, so a bridge that is not configured
	// refuses to create a planner rather than substituting or downloading one.
	t.Run("adapter diagnostics refuse instead of substituting", func(t *testing.T) {
		t.Setenv(plannerAdapterEnv, "")
		if _, err := newPlannerACPBackend(WorkerSessionSpec{CWD: t.TempDir()}, nil, nil); !errors.Is(err, errPlannerUnavailable) || !strings.Contains(err.Error(), plannerAdapterEnv) {
			t.Fatalf("unconfigured adapter = %v, want a refusal naming %s", err, plannerAdapterEnv)
		}
		t.Setenv(plannerAdapterEnv, "claude-code-acp")
		if _, err := newPlannerACPBackend(WorkerSessionSpec{CWD: t.TempDir()}, nil, nil); !errors.Is(err, errPlannerUnavailable) {
			t.Fatalf("relative adapter path = %v, want a refusal", err)
		}
	})

	// The factory roles are exercised against the same backend seam, on a
	// disposable repository, with the factory's own report parser.
	runFactoryRoleExercises(t)
}

// seedPaths lists the repository-relative paths one selection names.
func seedPaths(edit *EditSnapshot) []string {
	if edit == nil {
		return nil
	}
	out := make([]string, 0, len(edit.Files))
	for _, file := range edit.Files {
		out = append(out, file.Path)
	}
	return out
}

// roleFixture is one disposable repository plus an ACP worker backend over a
// scripted adapter: the inputs the factory's implementer and reviewer path
// reads, without a fleet, a worker registry or a provider.
type roleFixture struct {
	t       *testing.T
	dir     string
	base    string
	agent   *scriptedAgent
	backend WorkerBackend
	rec     *eventRecorder
	session string
}

// newRoleFixture commits one base revision and starts a worker session in a
// disposable repository. The adapter is the same scripted fake the planner
// proof uses; nothing here spawns a process.
func newRoleFixture(t *testing.T) *roleFixture {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "--initial-branch=main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	snapshotWriteFile(t, dir, "a.txt", "one\ntwo\n", 0o644)
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "base")

	agent := newModeScriptedAgent("sess-role")
	rec := &eventRecorder{}
	backend := newACPWorkerBackend(agent, rec.sink, nil)
	session, _, err := backend.Start(acpContext(t), WorkerSessionSpec{
		WorkerID: "worker-role", WorkspaceID: "workspace-role", ProjectRoot: dir, Mode: plannerModeRestricted,
	})
	if err != nil {
		t.Fatalf("start the role worker session: %v", err)
	}
	return &roleFixture{
		t: t, dir: dir, base: snapshotGitOut(t, dir, "rev-parse", "HEAD"),
		agent: agent, backend: backend, rec: rec, session: session,
	}
}

// scriptTurn makes the adapter answer every turn with the given text chunks and
// stop reason, as an adapter streaming its final message does.
func (r *roleFixture) scriptTurn(stopReason string, chunks ...string) {
	r.agent.setPromptHook(func(g *scriptedGen, _ json.RawMessage, _ json.RawMessage) *acpReply {
		for _, chunk := range chunks {
			g.notify(acpMethodSessionUpdate, map[string]any{
				"sessionId": r.session,
				"update": map[string]any{
					"kind":    "agent_message_chunk",
					"content": map[string]any{"type": "text", "text": chunk},
				},
			})
		}
		return &acpReply{result: map[string]any{"stopReason": stopReason}}
	})
}

// run dispatches one role turn and returns the final message the adapter
// streamed, which is what the factory's report parser reads.
func (r *roleFixture) run(prompt string) (string, WorkerTurnResult, error) {
	before := len(r.rec.all())
	result, err := r.backend.Prompt(acpContext(r.t), r.session, prompt)
	after := r.rec.all()
	message := &strings.Builder{}
	for _, event := range after[before:] {
		if event.sessionID != r.session || event.kind != workerEventTurnText {
			continue
		}
		var data struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.data, &data) == nil {
			message.WriteString(data.Text)
		}
	}
	return message.String(), result, err
}

// reportBlock renders one fenced JSON report, the form the factory prompt asks
// a worker for.
func reportBlock(report map[string]any) string {
	raw, err := json.Marshal(report)
	if err != nil {
		panic(fmt.Sprintf("render report: %v", err))
	}
	return "\n```json\n" + string(raw) + "\n```\n"
}

// runFactoryRoleExercises proves the factory-role contract on the WorkerBackend
// seam and the existing report parser: a correct revision, the expected role
// report, a bad or missing report rejected, a cancelled turn (not a role
// report) and a lost acknowledgement (uncertain, never replayed).
func runFactoryRoleExercises(t *testing.T) {
	t.Helper()

	// The implementer's expected report: a complete implementation with the
	// narration the prompt requires.
	t.Run("implementer report is accepted at the expected revision", func(t *testing.T) {
		r := newRoleFixture(t)
		r.scriptTurn("end_turn", "implementation complete", reportBlock(map[string]any{
			"operationId": "implement-1",
			"outcome":     "complete",
			"summary":     "Implemented and tested",
			"narration":   map[string]any{"issue": "the issue", "approach": "the approach"},
		}))
		message, result, err := r.run("Implement the approved scope.")
		if err != nil {
			t.Fatalf("implementer turn: %v", err)
		}
		if result.StopReason != "end_turn" {
			t.Fatalf("stop reason = %q, want end_turn", result.StopReason)
		}
		report, err := ParseFactoryReport(message, "implement-1", "implementer", r.base, "")
		if err != nil {
			t.Fatalf("parse implementer report: %v", err)
		}
		if report.Outcome != "complete" || report.Narration == nil {
			t.Fatalf("report = %+v, want a complete report with its narration", report)
		}
		// The revision the report is held to is the checkout the worker was
		// started on: the turn itself moved nothing.
		if got := snapshotGitOut(t, r.dir, "rev-parse", "HEAD"); got != r.base {
			t.Fatalf("checkout head = %q, want the approved base %q", got, r.base)
		}
		if report.HeadSHA != "" {
			t.Errorf("the implementer report names head %q, want the checkout's own evidence", report.HeadSHA)
		}
	})

	// The reviewer's expected report: a verdict tied to the exact revision
	// under review, with the evidence the parser requires.
	t.Run("reviewer verdict is accepted for the revision under review", func(t *testing.T) {
		r := newRoleFixture(t)
		snapshotWriteFile(t, r.dir, "a.txt", "one\nchanged\n", 0o644)
		mustGit(t, r.dir, "add", "-A")
		mustGit(t, r.dir, "commit", "-m", "candidate")
		head := snapshotGitOut(t, r.dir, "rev-parse", "HEAD")

		r.scriptTurn("end_turn", reportBlock(map[string]any{
			"operationId":      "review-1",
			"outcome":          "complete",
			"summary":          "Reviewed the candidate",
			"baseSha":          r.base,
			"headSha":          head,
			"inputsAccessible": true,
			"findings":         []map[string]any{{"title": "unused import", "severity": "nit"}},
		}))
		message, result, err := r.run("Review the candidate at " + head + ".")
		if err != nil {
			t.Fatalf("reviewer turn: %v", err)
		}
		if result.StopReason != "end_turn" {
			t.Fatalf("stop reason = %q, want end_turn", result.StopReason)
		}
		report, err := ParseFactoryReport(message, "review-1", "reviewer", r.base, head)
		if err != nil {
			t.Fatalf("parse reviewer report: %v", err)
		}
		if len(report.Findings) != 1 || report.Findings[0].Severity != "nit" {
			t.Fatalf("findings = %+v, want the reported nit", report.Findings)
		}
		// The same verdict for another revision is not this review.
		if _, err := ParseFactoryReport(message, "review-1", "reviewer", r.base, r.base); err == nil {
			t.Fatal("a reviewer report for one head was accepted for another")
		}
	})

	// A bad report is refused at parse time, so it never reaches the store.
	t.Run("a bad or missing report is rejected", func(t *testing.T) {
		r := newRoleFixture(t)

		r.scriptTurn("end_turn", reportBlock(map[string]any{
			"operationId": "implement-bad",
			"outcome":     "complete",
			"summary":     "Implemented",
		}))
		message, _, err := r.run("Implement the approved scope.")
		if err != nil {
			t.Fatalf("implementer turn: %v", err)
		}
		if _, err := ParseFactoryReport(message, "implement-bad", "implementer", "", ""); err == nil {
			t.Fatal("a complete implementer report without a narration was accepted")
		}
		if _, err := ParseFactoryReport(message, "another-operation", "implementer", "", ""); err == nil {
			t.Fatal("a report for another operation was accepted")
		}

		// No final block at all is a missing report, not an empty one.
		r.scriptTurn("end_turn", "I looked at the change but did not report.\n")
		message, result, err := r.run("Implement the approved scope.")
		if err != nil {
			t.Fatalf("implementer turn: %v", err)
		}
		if result.StopReason != "end_turn" {
			t.Fatalf("stop reason = %q, want end_turn", result.StopReason)
		}
		if _, err := ParseFactoryReport(message, "implement-missing", "implementer", "", ""); err == nil {
			t.Fatal("a turn with no final report was accepted")
		} else if !strings.Contains(err.Error(), "missing final JSON report") {
			t.Errorf("parse error = %v, want the missing-report refusal", err)
		}
	})

	// A cancelled turn is not a role report: the factory's worker path requires
	// end_turn before it reads one.
	t.Run("a cancelled turn is not a role report", func(t *testing.T) {
		r := newRoleFixture(t)
		held := make(chan json.RawMessage, 1)
		r.agent.setPromptHook(func(_ *scriptedGen, id json.RawMessage, _ json.RawMessage) *acpReply {
			held <- id
			return &acpReply{hold: true}
		})
		outcome := promptAsync(acpContext(t), r.backend, r.session, "Implement the approved scope.")
		promptID := <-held
		if err := r.backend.Cancel(acpContext(t), r.session); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		r.agent.current().respond(promptID, map[string]any{"stopReason": "cancelled"})
		out := <-outcome
		if out.err != nil {
			t.Fatalf("cancelled turn: %v", out.err)
		}
		if out.result.StopReason != "cancelled" {
			t.Fatalf("stop reason = %q, want the adapter's cancelled", out.result.StopReason)
		}
		if out.result.StopReason == "end_turn" {
			t.Fatal("a cancelled turn reported a finished turn")
		}
	})

	// A lost acknowledgement is uncertain, never a finished role turn, and the
	// turn is not dispatched a second time.
	t.Run("a lost acknowledgement is uncertain and not replayed", func(t *testing.T) {
		r := newRoleFixture(t)
		held := make(chan json.RawMessage, 1)
		r.agent.setPromptHook(func(_ *scriptedGen, id json.RawMessage, _ json.RawMessage) *acpReply {
			held <- id
			return &acpReply{hold: true}
		})
		outcome := promptAsync(acpContext(t), r.backend, r.session, "Implement the approved scope.")
		<-held
		r.agent.hangUp()
		out := <-outcome
		if out.err == nil {
			t.Fatal("a turn whose adapter died returned no error")
		}
		if !errors.Is(out.err, ErrWorkerUncertain) {
			t.Fatalf("turn error = %v, want ErrWorkerUncertain", out.err)
		}
		if out.result.StopReason != "" {
			t.Errorf("result = %+v, want no stop reason for an uncertain turn", out.result)
		}
		if n := r.agent.countFrames(acpMethodSessionPrompt); n != 1 {
			t.Fatalf("session/prompt frames = %d, want exactly 1", n)
		}
		// Kill the replacement generation too, so nothing outlives the test.
		r.agent.hangUp()
	})
}
