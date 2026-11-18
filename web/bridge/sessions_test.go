package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// Listing sessions for a project with no live agent answers on the control
// child. That child is the bridge's own, and other code holds hooks on it, so
// listing must ask it directly: building a Registry over it would install
// that Registry's OnRequest on the live child, and each listing would replace
// whatever the previous owner had put there.
func TestListingLeavesTheControlChildsHooksAlone(t *testing.T) {
	root := t.TempDir()
	f, _ := testFleetWithContainerizedAgent(t)
	ctl := newFakeAgent()
	child := &Child{Transport: ctl}
	f.newControl = func() (*Child, error) { return child, nil }
	srv := NewServer(f, "")

	path := "/api/sessions?cwd=" + url.QueryEscape(root)
	if rec := doReq(t, srv, http.MethodGet, path, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("first GET /api/sessions = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// The control agent is live now, so from here its hooks belong to
	// whoever set them, not to the listing.
	sentinel := func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return map[string]any{}, nil
	}
	child.OnRequest = sentinel
	want := reflect.ValueOf(sentinel).Pointer()

	for i := 0; i < 2; i++ {
		if rec := doReq(t, srv, http.MethodGet, path, nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("GET /api/sessions = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	}
	if child.OnRequest == nil {
		t.Fatal("listing sessions cleared the control child's OnRequest")
	}
	if got := reflect.ValueOf(child.OnRequest).Pointer(); got != want {
		t.Fatalf("listing sessions replaced the control child's OnRequest: got %p, want %p", child.OnRequest, sentinel)
	}
}

// assertChildCall waits until the fake child recorded a call of method naming
// sessionID, so a route that dispatches on a goroutine is asserted without a
// sleep.
func assertChildCall(t *testing.T, fake *fakeProjectChild, method, sessionID string) {
	t.Helper()
	eventually(t, method+" on the project child for "+sessionID, func() bool {
		for _, raw := range fake.calls(method) {
			if paramsOf(t, raw)["sessionId"] == sessionID {
				return true
			}
		}
		return false
	})
}

// TestEveryPerSessionRouteReachesAProjectSession: a client session living on a
// project child answers every per-session route there — stack, access, prompt,
// cancel and the event stream — because they all resolve through the one rule
// (P7 D2).
func TestEveryPerSessionRouteReachesAProjectSession(t *testing.T) {
	f, fakeFor := projectFleetWith(t, nil)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	sessionID, err := pc.reg.New(ctx, AgentPath(root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}
	srv := NewServer(f, "")

	// A read of the session's stack and of its access state each travel as one
	// child request naming the session.
	for _, tc := range []struct{ path, method string }{
		{path: "/api/sessions/" + sessionID + "/stack", method: "session/stack"},
		{path: "/api/sessions/" + sessionID + "/access", method: "session/access_state"},
	} {
		if rec := doReq(t, srv, http.MethodGet, tc.path, nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body %s)", tc.path, rec.Code, rec.Body.String())
		}
		assertChildCall(t, fake, tc.method, sessionID)
	}

	// A prompt is accepted at once and reaches the child on its own goroutine.
	if rec := doReq(t, srv, http.MethodPost, "/api/sessions/"+sessionID+"/prompt",
		contract.PromptRequest{Text: "run the tests"}, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("prompt = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	assertChildCall(t, fake, "session/prompt", sessionID)

	// Cancel is a notification, and it too names the session on the child.
	if rec := doReq(t, srv, http.MethodPost, "/api/sessions/"+sessionID+"/cancel", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	assertChildCall(t, fake, "session/cancel", sessionID)

	// The event stream subscribes to the project child's own log.
	subscribeSession(t, srv, pc.log, sessionID)
}

// TestLoadOpensAClosedSession: load on a session only its project's listing
// names reopens it on that project's child, answers the log's tail, and leaves
// the child tracking it — session/load is never sent, because the session is
// already open on its child (P7 D3, decision 5).
func TestLoadOpensAClosedSession(t *testing.T) {
	root := t.TempDir()
	f, fakeFor, ctl := controlFleet(t, func(cwd string) (any, *rpcError) {
		if cwd != root {
			return listing(), nil
		}
		return listing(map[string]any{"sessionId": "closed-1", "title": "old"}), nil
	})
	if err := f.ws.AddProject(root); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodPost, "/api/sessions/closed-1/load", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("load = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		SessionID string          `json:"sessionId"`
		Events    json.RawMessage `json:"events"`
	}
	decodeBody(t, rec, &got)
	if got.SessionID != "closed-1" {
		t.Fatalf("load answered sessionId %q, want closed-1", got.SessionID)
	}
	if string(got.Events) != "[]" {
		t.Fatalf("load answered events %s, want an empty array", got.Events)
	}

	pc, ok := projectEntry(f, root)
	if !ok {
		t.Fatal("load did not resolve the session's project child")
	}
	if !pc.reg.tracksClient("closed-1") {
		t.Fatal("the reopened session is not tracked as the child's client session")
	}
	fake := fakeFor(root)
	if fake == nil {
		t.Fatal("no fake was built for the session's root")
	}
	if loads := fake.calls("session/load"); len(loads) != 0 {
		t.Fatalf("the project child was asked to session/load an open session: %v", loads)
	}
	if resumes := fake.calls("session/resume"); len(resumes) != 1 {
		t.Fatalf("session/resume on the project child = %v, want one", resumes)
	}
	for _, raw := range ctl.calls("session/load") {
		if strings.Contains(raw, "closed-1") {
			t.Fatalf("the control child was asked to session/load the closed session: %s", raw)
		}
	}
}

// TestLoadOfAnOwnedSessionIs409: load of a session the bridge owns answers 409
// with the owner's refusal, never a reopen and never a 404 (P7 D5).
func TestLoadOfAnOwnedSessionIs409(t *testing.T) {
	root := t.TempDir()
	f, _, _ := controlFleet(t, nil)
	if err := f.ws.PutAgent(Agent{ID: "a-paused", Project: root, Name: "paused one",
		SessionID: "s-paused", OwnerID: DefaultOwnerID, Origin: OriginUI}); err != nil {
		t.Fatalf("PutAgent: %v", err)
	}
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodPost, "/api/sessions/s-paused/load", nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("load of an owned session = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var got map[string]string
	decodeBody(t, rec, &got)
	if !strings.Contains(got["error"], "agent paused one") {
		t.Fatalf("error = %q, want it to name the owner", got["error"])
	}
	if _, started := projectEntry(f, root); started {
		t.Fatal("a refused load started a project child")
	}
}

// TestLoadOfAnAgentSessionIsUnchanged: an agent's load keeps today's body — the
// agent's own view of cwd, then session/load on its child (decision 5).
func TestLoadOfAnAgentSessionIsUnchanged(t *testing.T) {
	f, fakeFor := hostOnlyProjectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	id, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, ok := f.ws.Agent(id)
	if !ok || a.SessionID == "" {
		t.Fatalf("agent %s has no recorded session", id)
	}
	fake := fakeFor(root)
	if fake == nil {
		t.Fatal("no project child was built for the root")
	}
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodPost, "/api/sessions/"+a.SessionID+"/load",
		map[string]string{"cwd": root}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("load of an agent's session = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got struct {
		SessionID string            `json:"sessionId"`
		Events    []json.RawMessage `json:"events"`
	}
	decodeBody(t, rec, &got)
	if got.SessionID != a.SessionID {
		t.Fatalf("load answered sessionId %q, want %s", got.SessionID, a.SessionID)
	}
	if got.Events == nil {
		t.Fatal("load answered no events array")
	}
	loads := fake.calls("session/load")
	if len(loads) != 1 {
		t.Fatalf("session/load on the agent's child = %v, want exactly one", loads)
	}
	if id, _ := paramsOf(t, loads[0])["sessionId"].(string); id != a.SessionID {
		t.Fatalf("session/load named %q, want %s", id, a.SessionID)
	}
}

// TestAnImageReachesAProjectSession: an upload addressed to a client session
// reaches the project child that holds it, through the same one rule every
// per-session route uses (P7 D2).
func TestAnImageReachesAProjectSession(t *testing.T) {
	f, fakeFor := projectFleetWith(t, storeImages)
	root := t.TempDir()
	sessionID := uploadSession(t, f, root)
	fake := fakeFor(root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}

	srv := NewServer(f, "")
	rec := doReq(t, srv, http.MethodPost, imageURL(root, sessionID),
		contract.ImageUpload{Data: base64.StdEncoding.EncodeToString([]byte("raw bytes"))}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	calls := fake.calls(imageMethod)
	if len(calls) != 1 {
		t.Fatalf("%s on the project child = %v, want one", imageMethod, calls)
	}
	if got, _ := paramsOf(t, calls[0])["sessionId"].(string); got != sessionID {
		t.Fatalf("%s named %q, want %s", imageMethod, got, sessionID)
	}
}

// controlFleet is projectFleetWith plus a fake control child whose session/list
// answers through list, which is asked for one root at a time.
func controlFleet(t *testing.T, list func(cwd string) (any, *rpcError)) (*Fleet, func(root string) *fakeProjectChild, *fakeAgent) {
	t.Helper()
	f, fakeFor := projectFleetWith(t, nil)
	ctl := newFakeAgent()
	if list != nil {
		ctl.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
			if method != "session/list" {
				return nil, nil, false
			}
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(params, &p)
			res, rerr := list(p.Cwd)
			return res, rerr, true
		}
	}
	f.newControl = func() (*Child, error) { return &Child{Transport: ctl}, nil }
	return f, fakeFor, ctl
}

// listing answers session/list with rows.
func listing(rows ...map[string]any) any {
	if rows == nil {
		rows = []map[string]any{}
	}
	return map[string]any{"sessions": rows}
}

// TestResolveSessionOrder: an agent's session and a client session on a project
// child resolve to their own registries, and only the agent's names an agent.
func TestResolveSessionOrder(t *testing.T) {
	f, fakeFor := hostOnlyProjectFleet(t)
	ctx := ctlContext(t)
	agentRoot, clientRoot := t.TempDir(), t.TempDir()

	id, err := f.Spawn(ctx, agentRoot, SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatalf("runtimeForAgent: %v", err)
	}
	agentSession := f.sessionIDFor(rt)
	if agentSession == "" {
		t.Fatal("the spawned agent has no session id")
	}

	// The two children's fakes each number their own sessions from s-1, so the
	// client's id is named explicitly: one id must never belong to two
	// children.
	pc, err := f.projectChildFor(ctx, clientRoot)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	clientFake := fakeFor(pc.root)
	if clientFake == nil {
		t.Fatal("no fake was built for the client root")
	}
	clientFake.fakeAgent.mu.Lock()
	clientFake.fakeAgent.results["session/new"] = map[string]any{"sessionId": "client-1"}
	clientFake.fakeAgent.mu.Unlock()
	clientSession, err := pc.reg.New(ctx, AgentPath(pc.root), "")
	if err != nil {
		t.Fatalf("session/new on the client child: %v", err)
	}
	if clientSession == agentSession {
		t.Fatalf("both sessions are %q", clientSession)
	}

	got, err := f.ResolveSession(agentSession)
	if err != nil {
		t.Fatalf("ResolveSession(agent session): %v", err)
	}
	if got.Reg != rt.reg || got.Agent != rt || got.Root != agentRoot || got.SessionID != agentSession {
		t.Fatalf("agent session resolved to %+v, want the agent's runtime at %s", got, agentRoot)
	}

	got, err = f.ResolveSession(clientSession)
	if err != nil {
		t.Fatalf("ResolveSession(client session): %v", err)
	}
	if got.Reg != pc.reg || got.Log != pc.log || got.Root != clientRoot || got.SessionID != clientSession {
		t.Fatalf("client session resolved to %+v, want the project child at %s", got, clientRoot)
	}
	if got.Agent != nil {
		t.Fatalf("a client session named agent %q", got.Agent.id)
	}
}

// TestResolveSessionSkipsFleetSessions: a fleet session on a project child is
// the agent's, not a client's, and resolution leaves it alone even when no
// agent mapping names it.
func TestResolveSessionSkipsFleetSessions(t *testing.T) {
	f, _ := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	pc.reg.TrackFleet("fleet-1", AgentPath(root))

	if _, err := f.ResolveSession("fleet-1"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("ResolveSession(fleet session) = %v, want ErrUnknownSession", err)
	}
}

// TestResolveSessionNeverStartsAChild: an id only a listing names is unknown
// to resolution, which never starts a project child to find it.
func TestResolveSessionNeverStartsAChild(t *testing.T) {
	root := t.TempDir()
	f, fakeFor, _ := controlFleet(t, func(string) (any, *rpcError) {
		return listing(map[string]any{"sessionId": "closed-1"}), nil
	})
	ctx := ctlContext(t)

	rows, err := f.listRoot(ctx, root)
	if err != nil {
		t.Fatalf("listRoot: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "closed-1" {
		t.Fatalf("listing = %+v, want closed-1", rows)
	}

	if _, err := f.ResolveSession("closed-1"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("ResolveSession(listed id) = %v, want ErrUnknownSession", err)
	}
	if fake := fakeFor(root); fake != nil {
		t.Fatal("ResolveSession built a project child")
	}
	if _, ok := projectEntry(f, root); ok {
		t.Fatal("ResolveSession left a project child in the fleet's map")
	}
}

// TestOpenSessionResumesOnTheProjectChild: a closed session reopens on the
// child of the root whose listing names it, and the control child is never
// asked to load, resume or open it.
func TestOpenSessionResumesOnTheProjectChild(t *testing.T) {
	root := t.TempDir()
	f, fakeFor, ctl := controlFleet(t, func(cwd string) (any, *rpcError) {
		if cwd != root {
			return listing(), nil
		}
		return listing(map[string]any{"sessionId": "closed-1", "title": "old"}), nil
	})
	ctx := ctlContext(t)
	if err := f.ws.AddProject(root); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	got, err := f.OpenSession(ctx, "closed-1")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	pc, ok := projectEntry(f, root)
	if !ok {
		t.Fatal("OpenSession did not resolve a project child for the session's root")
	}
	if got.Reg != pc.reg || got.Log != pc.log || got.Root != root || got.SessionID != "closed-1" {
		t.Fatalf("OpenSession = %+v, want the project child at %s", got, root)
	}
	if got.Agent != nil {
		t.Fatalf("a closed client session named agent %q", got.Agent.id)
	}

	fake := fakeFor(root)
	if fake == nil {
		t.Fatal("no fake was built for the session's root")
	}
	resumes := fake.calls("session/resume")
	if len(resumes) != 1 {
		t.Fatalf("session/resume on the project child = %v, want one", resumes)
	}
	if id, _ := paramsOf(t, resumes[0])["sessionId"].(string); id != "closed-1" {
		t.Fatalf("session/resume named %q, want closed-1", id)
	}
	follows := fake.calls("session/follow")
	if len(follows) != 1 {
		t.Fatalf("session/follow on the project child = %v, want one", follows)
	}
	if id, _ := paramsOf(t, follows[0])["sessionId"].(string); id != "closed-1" {
		t.Fatalf("session/follow named %q, want closed-1", id)
	}

	for _, method := range []string{"session/load", "session/resume", "session/new"} {
		for _, raw := range ctl.calls(method) {
			if strings.Contains(raw, "closed-1") {
				t.Fatalf("the control child was asked for %s on the closed session: %s", method, raw)
			}
		}
	}
}

// TestOpenSessionRefusesTheBridgesOwn: the bridge's own sessions are refused,
// each naming its owner, and none of them starts a project child (P7 D5).
func TestOpenSessionRefusesTheBridgesOwn(t *testing.T) {
	ctx := ctlContext(t)

	t.Run("a paused agent's session", func(t *testing.T) {
		root := t.TempDir()
		f, fakeFor, _ := controlFleet(t, nil)
		if err := f.ws.PutAgent(Agent{ID: "a-paused", Project: root, Name: "paused one",
			SessionID: "s-paused", OwnerID: DefaultOwnerID, Origin: OriginUI}); err != nil {
			t.Fatalf("PutAgent: %v", err)
		}

		_, err := f.OpenSession(ctx, "s-paused")
		var owned ErrSessionOwned
		if !errors.As(err, &owned) {
			t.Fatalf("OpenSession(paused agent's session) = %v, want ErrSessionOwned", err)
		}
		if owned.Owner != "agent paused one" {
			t.Fatalf("owner = %q, want \"agent paused one\"", owned.Owner)
		}
		if fakeFor(root) != nil {
			t.Fatal("a refused session started a project child")
		}
	})

	t.Run("the control agent's project session", func(t *testing.T) {
		root := t.TempDir()
		f, _, _ := controlFleet(t, nil)
		sid, err := f.projectSession(ctx, root)
		if err != nil {
			t.Fatalf("projectSession: %v", err)
		}

		_, err = f.OpenSession(ctx, sid)
		var owned ErrSessionOwned
		if !errors.As(err, &owned) {
			t.Fatalf("OpenSession(control project session) = %v, want ErrSessionOwned", err)
		}
		if owned.Owner != "the control agent" {
			t.Fatalf("owner = %q, want \"the control agent\"", owned.Owner)
		}
		if _, ok := projectEntry(f, root); ok {
			t.Fatal("a refused session started a project child")
		}
	})

	t.Run("a planner's session", func(t *testing.T) {
		root := t.TempDir()
		f, _, _ := controlFleet(t, nil)
		svc := newPlannerService(f, f.stateDir)
		plannerID := strings.Repeat("a", plannerIDLength)
		if err := svc.store.write(PlannerRecord{ID: plannerID, SessionID: "s-planner",
			ProjectRoot: root, State: plannerStateEnded}); err != nil {
			t.Fatalf("write the planner record: %v", err)
		}
		f.planners = svc

		_, err := f.OpenSession(ctx, "s-planner")
		var owned ErrSessionOwned
		if !errors.As(err, &owned) {
			t.Fatalf("OpenSession(planner session) = %v, want ErrSessionOwned", err)
		}
		if owned.Owner != "planner "+plannerID {
			t.Fatalf("owner = %q, want \"planner %s\"", owned.Owner, plannerID)
		}
		if _, ok := projectEntry(f, root); ok {
			t.Fatal("a refused session started a project child")
		}
	})
}

// TestOpenSessionUnknownIs404: an id no listing names is unknown.
func TestOpenSessionUnknownIs404(t *testing.T) {
	root := t.TempDir()
	f, fakeFor, _ := controlFleet(t, func(string) (any, *rpcError) {
		return listing(), nil
	})
	ctx := ctlContext(t)
	if err := f.ws.AddProject(root); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	if _, err := f.OpenSession(ctx, "no-such-session"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("OpenSession(unknown) = %v, want ErrUnknownSession", err)
	}
	if fakeFor(root) != nil {
		t.Fatal("an unknown session started a project child")
	}
}

// TestListRootNormalises: the child's updatedAt becomes Unix milliseconds and
// _meta.messageCount becomes messageCount, both 0 when absent or unreadable.
func TestListRootNormalises(t *testing.T) {
	root := t.TempDir()
	f, _, _ := controlFleet(t, func(string) (any, *rpcError) {
		return listing(
			map[string]any{"sessionId": "s-1", "title": "one",
				"updatedAt": "2026-01-02T03:04:05Z", "_meta": map[string]any{"messageCount": 7}},
			map[string]any{"sessionId": "s-2", "title": "two"},
			map[string]any{"sessionId": "s-3", "updatedAt": "not a time"},
		), nil
	})

	rows, err := f.listRoot(ctlContext(t), root)
	if err != nil {
		t.Fatalf("listRoot: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want three", rows)
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()
	if rows[0].UpdatedAt != want {
		t.Fatalf("updatedAt = %d, want %d", rows[0].UpdatedAt, want)
	}
	if rows[0].MessageCount != 7 {
		t.Fatalf("messageCount = %d, want 7", rows[0].MessageCount)
	}
	if rows[0].Title != "one" || rows[0].SessionID != "s-1" {
		t.Fatalf("row = %+v", rows[0])
	}
	for _, row := range rows[1:] {
		if row.UpdatedAt != 0 || row.MessageCount != 0 {
			t.Fatalf("%s = %+v, want zero updatedAt and messageCount", row.SessionID, row)
		}
	}
}

// TestListRootCachesAFailedRoot: a root that cannot be listed is retried once
// per thirty seconds, not on every read.
func TestListRootCachesAFailedRoot(t *testing.T) {
	root := t.TempDir()
	f, _, ctl := controlFleet(t, func(string) (any, *rpcError) {
		return nil, &rpcError{Code: -32603, Message: "no listing"}
	})
	now := time.Unix(1_760_000_000, 0)
	f.listNow = func() time.Time { return now }
	ctx := ctlContext(t)

	for i := 0; i < 2; i++ {
		if _, err := f.listRoot(ctx, root); err == nil {
			t.Fatalf("call %d: listRoot succeeded, want the child's failure", i+1)
		}
	}
	if got := len(ctl.calls("session/list")); got != 1 {
		t.Fatalf("session/list calls within 30 s = %d, want 1", got)
	}

	now = now.Add(31 * time.Second)
	if _, err := f.listRoot(ctx, root); err == nil {
		t.Fatal("listRoot succeeded after the failure aged out, want it retried and failing")
	}
	if got := len(ctl.calls("session/list")); got != 2 {
		t.Fatalf("session/list calls after 31 s = %d, want 2", got)
	}
}

// TestAControlRestartDropsTheListings: a restarted control child has forgotten
// every session it listed, so the cached listings go with it.
func TestAControlRestartDropsTheListings(t *testing.T) {
	root := t.TempDir()
	f, _, ctl := controlFleet(t, func(string) (any, *rpcError) {
		return listing(map[string]any{"sessionId": "s-1"}), nil
	})
	ctx := ctlContext(t)

	if _, err := f.listRoot(ctx, root); err != nil {
		t.Fatalf("listRoot: %v", err)
	}
	child, err := f.controlChild(ctx)
	if err != nil {
		t.Fatalf("controlChild: %v", err)
	}
	if child.OnRestart == nil {
		t.Fatal("the control child has no restart hook")
	}
	// This is the call the child's supervisor makes after an unexpected exit.
	child.OnRestart()

	if _, err := f.listRoot(ctx, root); err != nil {
		t.Fatalf("listRoot after the restart: %v", err)
	}
	if got := len(ctl.calls("session/list")); got != 2 {
		t.Fatalf("session/list calls = %d, want 2: the restart must drop the cached listing", got)
	}
}
