package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func rtFor(root string, containerized bool) *agentRuntime {
	return &agentRuntime{id: "a1", root: root, containerized: containerized}
}

func TestAgentPathTranslatesAContainerizedWorkspace(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	got, err := rt.agentPath("/state/work/a1/cmd/marshal/main.go")
	if err != nil {
		t.Fatalf("agentPath: %v", err)
	}
	if got != AgentPath("/work/cmd/marshal/main.go") {
		t.Fatalf("got %q, want /work/cmd/marshal/main.go", got)
	}
}

func TestAgentPathMapsTheWorkspaceRootItself(t *testing.T) {
	rt := rtFor("/host-projects/marshal", true)
	got, err := rt.agentPath("/host-projects/marshal")
	if err != nil {
		t.Fatalf("agentPath: %v", err)
	}
	if got != AgentPath("/work") {
		t.Fatalf("got %q, want /work", got)
	}
}

func TestAgentPathIsIdentityForAHostProcessAgent(t *testing.T) {
	rt := rtFor("/home/me/code", false)
	got, err := rt.agentPath("/home/me/code/pkg/x.go")
	if err != nil {
		t.Fatalf("agentPath: %v", err)
	}
	if got != AgentPath("/home/me/code/pkg/x.go") {
		t.Fatalf("got %q, want the path unchanged", got)
	}
}

func TestAgentPathRefusesAPathOutsideTheWorkspace(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	_, err := rt.agentPath("/state/work/a2/secret")
	if !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("got %v, want ErrOutsideWorkspace", err)
	}
	if !strings.Contains(err.Error(), "/state/work/a2/secret") ||
		!strings.Contains(err.Error(), "/state/work/a1") {
		t.Errorf("error names only one view: %v", err)
	}
}

func TestAgentPathRespectsSegmentBoundaries(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	if _, err := rt.agentPath("/state/work/a1-evil/x"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("matched a sibling sharing a prefix: %v", err)
	}
}

func TestAgentPathRefusesWhenTheWorkspaceRootIsUnset(t *testing.T) {
	rt := rtFor("", true)
	if _, err := rt.agentPath("/anything"); err == nil {
		t.Fatal("a containerized runtime with no workspace root accepted a path")
	}
}

func TestAgentPathRefusesARelativePath(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	if _, err := rt.agentPath("relative/path"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("accepted a relative path: %v", err)
	}
}

func TestAgentPathHandlesExplicitTraversal(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	if _, err := rt.agentPath("/state/work/a1/../../etc/passwd"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("accepted a path that escapes via ..: %v", err)
	}
}

func TestAgentPathHandlesTrailingSlashRoot(t *testing.T) {
	rt := rtFor("/state/work/a1/", true)
	got, err := rt.agentPath("/state/work/a1/pkg/x.go")
	if err != nil {
		t.Fatalf("agentPath: %v", err)
	}
	if got != AgentPath("/work/pkg/x.go") {
		t.Fatalf("got %q, want /work/pkg/x.go", got)
	}
}

func TestBridgePathReversesTheTranslation(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	got, err := rt.bridgePath("/work/cmd/marshal/main.go")
	if err != nil {
		t.Fatalf("bridgePath: %v", err)
	}
	if got != "/state/work/a1/cmd/marshal/main.go" {
		t.Fatalf("got %q, want /state/work/a1/cmd/marshal/main.go", got)
	}
}

func TestBridgePathMapsTheWorkspaceRootItself(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	got, err := rt.bridgePath("/work")
	if err != nil {
		t.Fatalf("bridgePath: %v", err)
	}
	if got != "/state/work/a1" {
		t.Fatalf("got %q, want /state/work/a1", got)
	}
}

func TestBridgePathIsIdentityForAHostProcessAgent(t *testing.T) {
	rt := rtFor("/home/me/code", false)
	got, err := rt.bridgePath("/home/me/code/pkg/x.go")
	if err != nil {
		t.Fatalf("bridgePath: %v", err)
	}
	if got != "/home/me/code/pkg/x.go" {
		t.Fatalf("got %q, want the path unchanged", got)
	}
}

func TestBridgePathLeavesUntranslatablePathsAsIs(t *testing.T) {
	rt := rtFor("/state/work/a1", true)
	got, err := rt.bridgePath("/something/else")
	if err != nil {
		t.Fatalf("bridgePath: %v", err)
	}
	if got != "/something/else" {
		t.Fatalf("got %q, want /something/else", got)
	}
}

// --- call-site coverage -----------------------------------------------
//
// The tests below assert that each site sending a path across the
// bridge/agent boundary translates it. Every one drives production code
// (Fleet.Spawn, the HTTP handlers, ReattachAll) rather than re-creating
// what production does, so removing a translation fails a test.

// capturingTransport is a test transport that records the params of
// each JSON-RPC request and returns canned responses.
type capturingTransport struct {
	mu       sync.Mutex
	captured map[string]any // method -> params (as decoded map)
	// pruneUnknown is returned as session/worktree_prune's "unknown"
	// list, in the agent's namespace, as a real agent would report it.
	pruneUnknown []string
}

func (t *capturingTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go t.serve(stdinR, stdoutW)
	return stdinW, stdoutR, io.NopCloser(strings.NewReader("")), nil
}

func (t *capturingTransport) serve(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		// Record the params for this method.
		var params map[string]any
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		t.mu.Lock()
		if t.captured == nil {
			t.captured = make(map[string]any)
		}
		t.captured[req.Method] = params
		unknown := t.pruneUnknown
		t.mu.Unlock()

		var result any
		switch req.Method {
		case "session/new":
			result = map[string]any{"sessionId": "s-1"}
		case "session/worktree_prune":
			result = map[string]any{"unknown": unknown}
		default:
			result = map[string]any{}
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

func (t *capturingTransport) Wait() error                { return nil }
func (t *capturingTransport) Signal(sig os.Signal) error { return nil }
func (t *capturingTransport) Kill() error                { return nil }
func (t *capturingTransport) Detach() error              { return nil }

func (t *capturingTransport) params(method string) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, _ := t.captured[method].(map[string]any)
	return v
}

// cwdSentTo returns the "cwd" param recorded for method.
func (t *capturingTransport) cwdSentTo(method string) string {
	v, _ := t.params(method)["cwd"].(string)
	return v
}

// testFleetWithContainerizedAgent builds a fleet whose agents run
// containerized, with a capturingTransport recording outbound requests.
// The runtime is marked containerized through the production seam
// (Child.Containerized), so agentPath translates exactly as it does in
// production and no test may reach in and set the flag by hand.
func testFleetWithContainerizedAgent(t *testing.T) (*Fleet, *capturingTransport) {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, "", Limits{}, "", nil, "marshal-state")
	tr := &capturingTransport{}
	f.newRuntime = func(a Agent) (*Child, error) {
		return &Child{Transport: tr, Containerized: true}, nil
	}
	t.Cleanup(f.Close)
	return f, tr
}

// startContainerizedAgent registers an agent rooted at root and starts
// its runtime, returning the live runtime.
func startContainerizedAgent(t *testing.T, f *Fleet, root string) *agentRuntime {
	t.Helper()
	a := Agent{ID: "a1", Project: root, SourceKind: "local", Profile: DefaultRuntimeProfile()}
	if err := f.ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	rt, err := f.startRuntime(context.Background(), a)
	if err != nil {
		t.Fatalf("startRuntime: %v", err)
	}
	return rt
}

func TestStartPlanSendsTheAgentsViewOfThePlanPath(t *testing.T) {
	// The project root must be a real, writable directory: startPlan
	// writes the plan to the bridge-side path before translating it.
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t)
	startContainerizedAgent(t, f, root)

	if err := f.startPlan(context.Background(), "a1", "p1", "## Task 1: x\n"); err != nil {
		t.Fatalf("startPlan: %v", err)
	}
	got, _ := tr.params("session/sdd_start")["planPath"].(string)
	if got != "/work/.marshal/intake/p1.md" {
		t.Fatalf("planPath = %q, want /work/.marshal/intake/p1.md", got)
	}
}

func TestWorktreePruneSendsTheAgentsView(t *testing.T) {
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t)
	startContainerizedAgent(t, f, root)

	f.reconcileOnce(context.Background(), root)
	if got := tr.cwdSentTo("session/worktree_prune"); got != "/work" {
		t.Fatalf("cwd = %q, want /work", got)
	}
}

func TestSpawnSendsTheAgentsViewOfCwd(t *testing.T) {
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t)

	// Drive the real Spawn: the translation under test happens inside
	// it, between startRuntime and session/new, where a test cannot
	// reach in to adjust the runtime.
	if _, err := f.Spawn(context.Background(), root, SpawnOptions{}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got := tr.cwdSentTo("session/new"); got != "/work" {
		t.Fatalf("session/new cwd = %q, want /work", got)
	}
}

func TestOrphanWorktreesAreReportedInTheBridgesView(t *testing.T) {
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t)
	// The agent names orphans in its own namespace.
	tr.pruneUnknown = []string{"/work/.marshal/worktrees/stale"}
	startContainerizedAgent(t, f, root)

	f.reconcileOnce(context.Background(), root)

	f.mu.Lock()
	got := f.orphans[root]
	f.mu.Unlock()
	want := filepath.Join(root, ".marshal/worktrees/stale")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("orphans = %v, want [%s] in the bridge's view", got, want)
	}
}

func TestRestoreSessionSendsTheAgentsViewOfCwd(t *testing.T) {
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t)

	// A persisted agent with a session id: ReattachAll must reload it,
	// and session/load carries a cwd that must be translated.
	a := Agent{ID: "a1", Project: root, SourceKind: "local",
		SessionID: "s-1", Profile: DefaultRuntimeProfile()}
	if err := f.ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	if errs := f.ReattachAll(context.Background()); len(errs) > 0 {
		t.Fatalf("ReattachAll: %v", errs)
	}
	if got := tr.cwdSentTo("session/load"); got != "/work" {
		t.Fatalf("session/load cwd = %q, want /work", got)
	}
}

func TestListSessionsSendsTheAgentsViewOfCwd(t *testing.T) {
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t)
	startContainerizedAgent(t, f, root)
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodGet, "/api/sessions?cwd="+url.QueryEscape(root), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := tr.cwdSentTo("session/list"); got != "/work" {
		t.Fatalf("session/list cwd = %q, want /work", got)
	}
}

func TestLoadSessionSendsTheAgentsViewOfCwd(t *testing.T) {
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t)

	if _, err := f.Spawn(context.Background(), root, SpawnOptions{}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	srv := NewServer(f, "")

	rec := doReq(t, srv, http.MethodPost, "/api/sessions/s-1/load",
		map[string]string{"cwd": root}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("load = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := tr.cwdSentTo("session/load"); got != "/work" {
		t.Fatalf("session/load cwd = %q, want /work", got)
	}
}

// TestListSessionsRejectsAPathOutsideTheWorkspace covers the error path
// writeErr added for ErrOutsideWorkspace: a client-supplied cwd that the
// agent has no view of is the caller's mistake (400), not a bad gateway.
func TestListSessionsRejectsAPathOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	f, _ := testFleetWithContainerizedAgent(t)
	startContainerizedAgent(t, f, root)
	srv := NewServer(f, "")

	// runtimeForRoot resolves by prefix, so ask for a path under the
	// project root that the runtime is not rooted at.
	rec := doReq(t, srv, http.MethodGet,
		"/api/sessions?cwd="+url.QueryEscape(filepath.Join(root, "..", "elsewhere")), nil, nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("a path outside the workspace was accepted: %s", rec.Body.String())
	}
}
