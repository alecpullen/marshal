package bridge

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
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

// groupOfRoot is the index's group for root.
func groupOfRoot(t *testing.T, index contract.SessionIndex, root string) contract.ProjectSessions {
	t.Helper()
	for _, group := range index.Projects {
		if group.Root == root {
			return group
		}
	}
	t.Fatalf("the index has no group for %s: %+v", root, index.Projects)
	return contract.ProjectSessions{}
}

// rowOfSession is the group's row for sessionID.
func rowOfSession(t *testing.T, group contract.ProjectSessions, sessionID string) contract.SessionRow {
	t.Helper()
	for _, row := range group.Sessions {
		if row.SessionID == sessionID {
			return row
		}
	}
	t.Fatalf("%s has no row for %s: %+v", group.Root, sessionID, group.Sessions)
	return contract.SessionRow{}
}

// askFromTheChild makes the fake child send a request of its own, the way a
// real child does when a tool call needs the client's approval.
func askFromTheChild(t *testing.T, a *fakeAgent, id int, method string, params any) {
	t.Helper()
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if a.out == nil {
		t.Fatal("the fake child has no stream to write on")
	}
	if err := json.NewEncoder(a.out).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		t.Fatalf("send the child's %s: %v", method, err)
	}
}

// tapTransport is a project child's transport that keeps every frame the
// bridge wrote to it. The fake records the bridge's requests; the bridge's
// answers to the child's own requests carry no method, so a test that asserts
// a decision reached the child needs its own copy.
type tapTransport struct {
	agentTransport

	mu   sync.Mutex
	sent []string
}

func (t *tapTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdin, stdout, stderr, err := t.agentTransport.Open()
	if err != nil {
		return nil, nil, nil, err
	}
	return &tapWriter{WriteCloser: stdin, into: t}, stdout, stderr, nil
}

// wrote returns the frames the bridge wrote, in order.
func (t *tapTransport) wrote() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.sent...)
}

// tapWriter records one frame per Write: Child writes each frame whole, under
// its own write lock.
type tapWriter struct {
	io.WriteCloser
	into *tapTransport
}

func (w *tapWriter) Write(p []byte) (int, error) {
	w.into.mu.Lock()
	w.into.sent = append(w.into.sent, string(p))
	w.into.mu.Unlock()
	return w.WriteCloser.Write(p)
}

// tapProjectChildren makes every project child built from here on report what
// the bridge wrote to it, and returns its tap for a root.
func tapProjectChildren(f *Fleet) func(root string) *tapTransport {
	var mu sync.Mutex
	byRoot := map[string]*tapTransport{}
	inner := f.newProjectChild
	f.newProjectChild = func(root, socket string) (*Child, error) {
		child, err := inner(root, socket)
		if err != nil {
			return nil, err
		}
		tap := &tapTransport{agentTransport: child.Transport}
		child.Transport = tap
		mu.Lock()
		byRoot[root] = tap
		mu.Unlock()
		return child, nil
	}
	return func(root string) *tapTransport {
		mu.Lock()
		defer mu.Unlock()
		return byRoot[root]
	}
}

// stuckListing is a control child that answers everything at once except one
// root's session/list, which it never answers: a root whose index is wedged is
// exactly the case listTimeout bounds (decision 6). The other root's list is
// the first page a real child would send.
type stuckListing struct {
	root string

	// mu guards out, which a stop closes to end the bridge's read of this
	// child.
	mu  sync.Mutex
	out io.WriteCloser
}

func (s *stuckListing) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	s.mu.Lock()
	s.out = stdoutW
	s.mu.Unlock()
	go s.serve(stdinR, stdoutW)
	return stdinW, stdoutR, io.NopCloser(strings.NewReader("")), nil
}

func (s *stuckListing) serve(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion":   1,
				"agentInfo":         map[string]any{"version": "dev"},
				"agentCapabilities": map[string]any{"configAccess": map[string]any{}, "watchAccess": map[string]any{}},
			}
		case "session/new":
			result = map[string]any{"sessionId": "ctl-1"}
		case "session/list":
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Cwd == s.root {
				continue // this root never answers
			}
			result = map[string]any{"sessions": []any{
				map[string]any{"sessionId": "s-fast", "title": "fast"},
			}}
		default:
			result = map[string]any{}
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

// stop closes this generation's stream, so the bridge stops reading it as a
// live child does when it exits.
func (s *stuckListing) stop() {
	s.mu.Lock()
	out := s.out
	s.mu.Unlock()
	if out != nil {
		_ = out.Close()
	}
}

func (s *stuckListing) Wait() error                { return nil }
func (s *stuckListing) Signal(sig os.Signal) error { s.stop(); return nil }
func (s *stuckListing) Kill() error                { s.stop(); return nil }
func (s *stuckListing) Detach() error              { return nil }

// TestTheIndexGroupsOpenAndClosedSessions: every root lists, and a root with a
// live child puts its open sessions first — with the listing's title and the
// registry's own viewers and busy flag — and never repeats one as closed
// (P7 D7).
func TestTheIndexGroupsOpenAndClosedSessions(t *testing.T) {
	openRoot, quietRoot := t.TempDir(), t.TempDir()
	var clientID string
	f, fakeFor, _ := controlFleet(t, func(cwd string) (any, *rpcError) {
		switch cwd {
		case openRoot:
			if clientID == "" {
				return listing(), nil
			}
			return listing(
				map[string]any{"sessionId": clientID, "title": "from the listing",
					"updatedAt": "2026-01-02T03:04:05Z",
					"_meta":     map[string]any{"messageCount": 4}},
				map[string]any{"sessionId": "closed-1", "title": "old",
					"updatedAt": "2025-12-01T00:00:00Z"},
			), nil
		case quietRoot:
			return listing(map[string]any{"sessionId": "quiet-1", "title": "quiet",
				"updatedAt": "2025-11-01T00:00:00Z"}), nil
		}
		return listing(), nil
	})
	ctx := ctlContext(t)
	for _, root := range []string{openRoot, quietRoot} {
		if err := f.ws.AddProject(root); err != nil {
			t.Fatalf("AddProject(%s): %v", root, err)
		}
	}

	pc, err := f.projectChildFor(ctx, openRoot)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	// The control child numbers its own sessions from s-1 too, and the
	// control agent's session is one the index never offers; naming the
	// client's explicitly keeps the two apart.
	fake := fakeFor(pc.root)
	fake.fakeAgent.mu.Lock()
	fake.results["session/new"] = map[string]any{"sessionId": "client-1"}
	fake.fakeAgent.mu.Unlock()
	clientID, err = pc.reg.New(ctx, AgentPath(pc.root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if !pc.reg.attachViewer(clientID) {
		t.Fatalf("attachViewer(%s) = false", clientID)
	}
	pc.reg.beginTurn(clientID)

	index := f.SessionIndex(ctx, "")
	if len(index.Projects) != 2 {
		t.Fatalf("projects = %+v, want the two roots", index.Projects)
	}

	group := groupOfRoot(t, index, openRoot)
	if len(group.Sessions) != 2 {
		t.Fatalf("%s lists %+v, want the open session and the closed one", openRoot, group.Sessions)
	}
	open := group.Sessions[0]
	if open.SessionID != clientID || !open.Open {
		t.Fatalf("first row = %+v, want %s open", open, clientID)
	}
	if open.Title != "from the listing" {
		t.Fatalf("open row title = %q, want the listing's", open.Title)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli(); open.UpdatedAt != want {
		t.Fatalf("open row updatedAt = %d, want %d", open.UpdatedAt, want)
	}
	if open.MessageCount != 4 {
		t.Fatalf("open row messageCount = %d, want 4", open.MessageCount)
	}
	if open.Viewers != 1 || !open.Busy {
		t.Fatalf("open row = %+v, want one viewer and busy", open)
	}
	if closed := group.Sessions[1]; closed.SessionID != "closed-1" || closed.Open {
		t.Fatalf("second row = %+v, want closed-1 closed", closed)
	}

	quiet := groupOfRoot(t, index, quietRoot)
	if len(quiet.Sessions) != 1 || quiet.Sessions[0].SessionID != "quiet-1" || quiet.Sessions[0].Open {
		t.Fatalf("%s lists %+v, want quiet-1 closed", quietRoot, quiet.Sessions)
	}
}

// TestTheIndexLeavesTheBridgesOwnOut: an agent's, the control agent's and a
// planner's sessions are the bridge's own, and the index never offers them for
// opening (P7 D5).
func TestTheIndexLeavesTheBridgesOwnOut(t *testing.T) {
	root := t.TempDir()
	var rows []map[string]any
	f, _, _ := controlFleet(t, func(string) (any, *rpcError) { return listing(rows...), nil })
	ctx := ctlContext(t)
	if err := f.ws.AddProject(root); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := f.ws.PutAgent(Agent{ID: "a-paused", Project: root, Name: "paused one",
		SessionID: "s-agent", OwnerID: DefaultOwnerID, Origin: OriginUI}); err != nil {
		t.Fatalf("PutAgent: %v", err)
	}
	f.ctl.mu.Lock()
	f.ctl.projectSessions[root] = "s-control"
	f.ctl.mu.Unlock()
	svc := newPlannerService(f, f.stateDir)
	plannerID := strings.Repeat("a", plannerIDLength)
	if err := svc.store.write(PlannerRecord{ID: plannerID, SessionID: "s-planner",
		ProjectRoot: root, State: plannerStateEnded}); err != nil {
		t.Fatalf("write the planner record: %v", err)
	}
	f.planners = svc

	// Every one of them is in the listing, as a paused agent's, a control
	// agent's and a planner's session always are.
	rows = []map[string]any{
		{"sessionId": "s-agent", "title": "an agent"},
		{"sessionId": "s-control", "title": "the control agent"},
		{"sessionId": "s-planner", "title": "a planner"},
		{"sessionId": "s-client", "title": "a terminal's"},
	}

	index := f.SessionIndex(ctx, "")
	group := groupOfRoot(t, index, root)
	if group.Error != "" {
		t.Fatalf("%s lists with error %q", root, group.Error)
	}
	if len(group.Sessions) != 1 || group.Sessions[0].SessionID != "s-client" {
		t.Fatalf("the index lists %+v, want only the client's session", group.Sessions)
	}
}

// TestTheIndexShowsADeadRootBesideItsSiblings: a root whose listing fails is a
// group with an error, and every other root still lists (P7 D7).
func TestTheIndexShowsADeadRootBesideItsSiblings(t *testing.T) {
	dead, live := t.TempDir(), t.TempDir()
	f, _, _ := controlFleet(t, func(cwd string) (any, *rpcError) {
		if cwd == dead {
			return nil, &rpcError{Code: -32603, Message: "the index is unreadable"}
		}
		return listing(map[string]any{"sessionId": "s-live", "title": "live"}), nil
	})
	ctx := ctlContext(t)
	for _, root := range []string{dead, live} {
		if err := f.ws.AddProject(root); err != nil {
			t.Fatalf("AddProject(%s): %v", root, err)
		}
	}
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodGet, "/api/sessions/all", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/all = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var index contract.SessionIndex
	decodeBody(t, rec, &index)

	broken := groupOfRoot(t, index, dead)
	if !strings.Contains(broken.Error, "the index is unreadable") {
		t.Fatalf("%s lists with error %q, want the child's failure", dead, broken.Error)
	}
	if broken.Sessions == nil {
		t.Fatalf("%s answers a null sessions array, want []", dead)
	}
	if got := groupOfRoot(t, index, live); len(got.Sessions) != 1 || got.Sessions[0].SessionID != "s-live" {
		t.Fatalf("%s lists %+v beside a dead root, want s-live", live, got.Sessions)
	}
	_ = ctx
}

// TestTheIndexIsBoundedByASlowRoot: a root that never answers costs its own
// listTimeout and no other root's, so the request returns at once with that
// root's error (decision 6).
func TestTheIndexIsBoundedByASlowRoot(t *testing.T) {
	fast, slow := t.TempDir(), t.TempDir()
	f, _ := projectFleetWith(t, nil)
	f.newControl = func() (*Child, error) { return &Child{Transport: &stuckListing{root: slow}}, nil }
	f.listTimeout = 100 * time.Millisecond
	for _, root := range []string{fast, slow} {
		if err := f.ws.AddProject(root); err != nil {
			t.Fatalf("AddProject(%s): %v", root, err)
		}
	}
	srv := NewServer(f, "")

	start := time.Now()
	rec := doReq(t, srv, http.MethodGet, "/api/sessions/all", nil, nil)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("GET /api/sessions/all took %v, want it bounded by the 100 ms list timeout", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/all = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var index contract.SessionIndex
	decodeBody(t, rec, &index)

	if got := groupOfRoot(t, index, slow); got.Error == "" {
		t.Fatalf("%s never answered and lists with no error", slow)
	}
	if got := groupOfRoot(t, index, fast); len(got.Sessions) != 1 || got.Sessions[0].SessionID != "s-fast" {
		t.Fatalf("%s lists %+v beside a root that never answered, want s-fast", fast, got.Sessions)
	}
}

// TestTheIndexNarrowsToARoot: the root query parameter narrows the index to
// that project, and a root the fleet does not know is an index with none.
func TestTheIndexNarrowsToARoot(t *testing.T) {
	wanted, other := t.TempDir(), t.TempDir()
	f, _, _ := controlFleet(t, func(cwd string) (any, *rpcError) {
		if cwd == wanted {
			return listing(map[string]any{"sessionId": "s-wanted"}), nil
		}
		return listing(map[string]any{"sessionId": "s-other"}), nil
	})
	for _, root := range []string{wanted, other} {
		if err := f.ws.AddProject(root); err != nil {
			t.Fatalf("AddProject(%s): %v", root, err)
		}
	}
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodGet, "/api/sessions/all?root="+url.QueryEscape(wanted), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/all?root= = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var index contract.SessionIndex
	decodeBody(t, rec, &index)
	if len(index.Projects) != 1 || index.Projects[0].Root != wanted {
		t.Fatalf("narrowed index = %+v, want %s alone", index.Projects, wanted)
	}
	if len(index.Projects[0].Sessions) != 1 || index.Projects[0].Sessions[0].SessionID != "s-wanted" {
		t.Fatalf("%s lists %+v, want s-wanted", wanted, index.Projects[0].Sessions)
	}

	unknown := t.TempDir()
	rec = doReq(t, srv, http.MethodGet, "/api/sessions/all?root="+url.QueryEscape(unknown), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/all?root=<unknown> = %d, want 200", rec.Code)
	}
	decodeBody(t, rec, &index)
	if index.Projects == nil {
		t.Fatal("an unknown root answered a null projects array, want []")
	}
	if len(index.Projects) != 0 {
		t.Fatalf("an unknown root lists %+v, want no projects", index.Projects)
	}
}

// TestTheIndexIsNeverNull: no projects answers an empty array, not null, and so
// does a project with no sessions (P7 D7).
func TestTheIndexIsNeverNull(t *testing.T) {
	f, _ := projectFleetWith(t, nil)
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodGet, "/api/sessions/all", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/all = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"projects":[]}` {
		t.Fatalf("an index with no projects = %s, want {\"projects\":[]}", got)
	}

	root := t.TempDir()
	f2, _, _ := controlFleet(t, func(string) (any, *rpcError) { return listing(), nil })
	if err := f2.ws.AddProject(root); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	rec = doReq(t, NewServer(f2, ""), http.MethodGet, "/api/sessions/all", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/all = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"sessions":[]`) {
		t.Fatalf("an empty project = %s, want it to answer \"sessions\":[]", body)
	}
}

// TestTheIndexCarriesAParkedRequest: an ask the client is holding open is in
// its session's row, with the id an answer goes to and the payload it arrived
// with (P7 D7).
func TestTheIndexCarriesAParkedRequest(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	prompting := make(chan struct{}, 1)
	var releaseOnce sync.Once
	f, fakeFor := projectFleetWith(t, func(fake *fakeProjectChild) {
		// A real child's prompt stays open while it asks for a tool call's
		// approval; a fake that answered at once would end the turn before
		// the ask arrived, and a session accepts an ask only during its turn.
		fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
			if method != "session/prompt" {
				return nil, nil, false
			}
			prompting <- struct{}{}
			<-release
			return map[string]any{"stopReason": "end_turn"}, nil, true
		}
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f.newControl = func() (*Child, error) { return &Child{Transport: newFakeAgent()}, nil }
	ctx := ctlContext(t)
	if err := f.ws.AddProject(root); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}
	// The control child numbers its own sessions from s-1 too, and the
	// control agent's session is one the index never offers; naming the
	// client's explicitly keeps the two apart.
	fake.fakeAgent.mu.Lock()
	fake.results["session/new"] = map[string]any{"sessionId": "client-1"}
	fake.fakeAgent.mu.Unlock()
	sessionID, err := pc.reg.New(ctx, AgentPath(pc.root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	// A prompt from the bridge opens the turn the child then asks about.
	go func() { _ = pc.reg.Prompt(ctx, sessionID, "run the tests") }()
	select {
	case <-prompting:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt never reached the child")
	}
	askFromTheChild(t, fake.fakeAgent, 9000, "session/request_permission", map[string]any{
		"sessionId": sessionID, "toolCallId": "tc-1", "toolName": "shell.run",
	})
	eventually(t, "the parked permission", func() bool {
		_, ok := pc.reg.PendingRequest(sessionID)
		return ok
	})

	rec := doReq(t, NewServer(f, ""), http.MethodGet, "/api/sessions/all", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions/all = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var index contract.SessionIndex
	decodeBody(t, rec, &index)
	row := rowOfSession(t, groupOfRoot(t, index, root), sessionID)
	if !row.Open {
		t.Fatalf("the row for %s is closed: %+v", sessionID, row)
	}
	if row.Pending == nil {
		t.Fatalf("the row for %s carries no parked request: %+v", sessionID, row)
	}
	if row.Pending.Kind != contract.PendingApproval || row.Pending.ID != "tc-1" {
		t.Fatalf("pending = %+v, want the approval tc-1", *row.Pending)
	}
	var params map[string]any
	if err := json.Unmarshal(row.Pending.Params, &params); err != nil {
		t.Fatalf("pending params %s: %v", row.Pending.Params, err)
	}
	if params["toolCallId"] != "tc-1" || params["toolName"] != "shell.run" {
		t.Fatalf("pending params = %s, want the request's own payload", row.Pending.Params)
	}
}

// TestAnAnswerReachesAProjectSession: an answer posted to the permission route
// resolves the ask parked on a client session, and the child receives it as
// the answer to its own request (P7 D2).
func TestAnAnswerReachesAProjectSession(t *testing.T) {
	root := t.TempDir()
	f, fakeFor := projectFleetWith(t, nil)
	taps := tapProjectChildren(f)
	f.newControl = func() (*Child, error) { return &Child{Transport: newFakeAgent()}, nil }
	ctx := ctlContext(t)
	if err := f.ws.AddProject(root); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	sessionID, err := pc.reg.New(ctx, AgentPath(pc.root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}
	askFromTheChild(t, fake.fakeAgent, 9000, "session/request_permission", map[string]any{
		"sessionId": sessionID, "toolCallId": "tc-1", "toolName": "shell.run",
	})
	eventually(t, "the parked permission", func() bool {
		_, ok := pc.reg.PendingRequest(sessionID)
		return ok
	})

	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/permissions/tc-1",
		contract.Decision{Approved: true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/permissions/tc-1 = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if _, ok := pc.reg.PendingRequest(sessionID); ok {
		t.Fatalf("the answer left %s's request pending", sessionID)
	}

	tap := taps(pc.root)
	if tap == nil {
		t.Fatal("no transport recorded what the bridge wrote to the project child")
	}
	eventually(t, "the decision reaching the child", func() bool {
		for _, frame := range tap.wrote() {
			var answer struct {
				ID     json.RawMessage `json:"id"`
				Result struct {
					Approved bool `json:"approved"`
				} `json:"result"`
			}
			if json.Unmarshal([]byte(frame), &answer) != nil {
				continue
			}
			if string(answer.ID) == "9000" && answer.Result.Approved {
				return true
			}
		}
		return false
	})
}
