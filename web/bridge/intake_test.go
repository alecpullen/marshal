package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testFleetWithClient(t *testing.T, c MCPClient) *Fleet {
	t.Helper()
	f := testFleet(t)
	c.OwnerID = DefaultOwnerID
	if c.ID == "" {
		c.ID = "c1"
	}
	if err := f.ws.PutClient(c); err != nil {
		t.Fatal(err)
	}
	return f
}

func registerRepo(t *testing.T, f *Fleet, id string) {
	t.Helper()
	if err := f.ws.PutRepo(Repo{ID: id, URL: "file:///tmp/" + id, OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
}

// registerGitRepo registers a repo backed by a real bare git repo so a
// spawn against it can actually succeed. Skips when git is unavailable.
func registerGitRepo(t *testing.T, f *Fleet, id string) {
	t.Helper()
	if f.git == nil {
		t.Skip("git not installed")
	}
	if err := f.ws.PutRepo(Repo{ID: id, URL: newBareRepoFixture(t), Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitFromMCPRequiresARegisteredRepo(t *testing.T) {
	f := testFleet(t)
	_, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "not-registered", Title: "x", Prompt: "y",
	})
	if !errors.Is(err, ErrUnregisteredRepo) {
		t.Fatalf("got %v, want ErrUnregisteredRepo", err)
	}
}

func TestSubmitFromNonAutonomousClientLandsPending(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")

	res, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "add a flag", Prompt: "do it",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Status != "pending" || res.PendingID == "" {
		t.Fatalf("got %+v, want a pending submission", res)
	}
	if len(f.Snapshot()) != 0 {
		t.Fatal("an unconfirmed submission started an agent")
	}
}

func TestSubmitFromAutonomousClientStartsImmediately(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID, Autonomous: true})
	registerGitRepo(t, f, "r1")

	res, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "x", Prompt: "y",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Status != "running" || res.AgentID == "" {
		t.Fatalf("got %+v, want a started agent", res)
	}
}

func TestSubmitRespectsAllowedRepos(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{
		ID: "c1", OwnerID: DefaultOwnerID, Autonomous: true, AllowedRepos: []string{"other"},
	})
	registerRepo(t, f, "r1")

	_, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "x", Prompt: "y",
	})
	if !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("got %v, want ErrRepoNotAllowed", err)
	}
}

func TestSubmitEnforcesTheDailyCap(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{
		ID: "c1", OwnerID: DefaultOwnerID, Autonomous: true, MaxPerDay: 1,
	})
	registerGitRepo(t, f, "r1")
	req := SpawnRequest{Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "x", Prompt: "y"}

	if _, err := f.Submit(context.Background(), req); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := f.Submit(context.Background(), req); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("second submit = %v, want ErrCapExceeded", err)
	}
}

func TestSubmitRequiresATitle(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")
	if _, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Prompt: "y",
	}); err == nil {
		t.Fatal("accepted a submission with no title; the operator would confirm a blank row")
	}
}

func TestApproveWritesThePlanToABridgeChosenPath(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerGitRepo(t, f, "r1") // skips if no git
	res, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "t",
		Plan: "## Global Constraints\n\n- none\n\n### Task 1: Do it\n\n- [ ] step\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	agentID, err := f.Approve(context.Background(), res.PendingID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	a, _ := f.ws.Agent(agentID)
	planPath := planPathFor(a.Project, res.PendingID)
	body, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("plan was not written: %v", err)
	}
	if !strings.Contains(string(body), "### Task 1: Do it") {
		t.Fatal("plan content was not preserved verbatim")
	}
}

// TestPlanPathIsNeverClientControlled is the path-traversal regression.
func TestPlanPathIsNeverClientControlled(t *testing.T) {
	// The intake surface exposes no path field at all, so the only way a
	// client could steer the write is through the pending id. Prove that
	// a hostile id cannot escape the workspace.
	for _, hostile := range []string{"../../etc/cron.d/x", "..", "/etc/passwd", "a/../../b"} {
		got := planPathFor("/srv/work/agent1", hostile)
		if !strings.HasPrefix(filepath.Clean(got), "/srv/work/agent1/") {
			t.Fatalf("planPathFor(%q) escaped the workspace: %q", hostile, got)
		}
	}
}

func TestDenyDiscardsWithoutSpawning(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")
	res, _ := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "t", Prompt: "p",
	})

	if err := f.Deny(res.PendingID); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if len(f.ws.Pending()) != 0 {
		t.Fatal("Deny left the submission queued")
	}
	if len(f.Snapshot()) != 0 {
		t.Fatal("Deny started an agent")
	}
}

func TestApproveRefusesAnExpiredSubmission(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerRepo(t, f, "r1")
	if err := f.ws.PutPending(PendingSpawn{
		ID: "old", Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "t",
		Prompt: "p", ExpiresAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Approve(context.Background(), "old"); err == nil {
		t.Fatal("an expired submission was approved")
	}
}

// TestCapsAreScopedPerClient proves that one client's agents do not count
// against another client's cap. Without the ClientID filter in
// checkCaps, client B's agent would exhaust client A's MaxPerDay=1.
func TestCapsAreScopedPerClient(t *testing.T) {
	f := testFleetWithClient(t, MCPClient{
		ID: "cA", OwnerID: DefaultOwnerID, Autonomous: true, MaxPerDay: 1,
	})
	if err := f.ws.PutClient(MCPClient{
		ID: "cB", OwnerID: DefaultOwnerID, Autonomous: true,
	}); err != nil {
		t.Fatal(err)
	}
	registerGitRepo(t, f, "r1")

	// Client B uses the (shared) daily quota first.
	if _, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "cB", RepoID: "r1", Title: "b work", Prompt: "y",
	}); err != nil {
		t.Fatalf("client B submit: %v", err)
	}

	// Client A must still be able to submit — its cap is independent.
	if _, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "cA", RepoID: "r1", Title: "a work", Prompt: "y",
	}); err != nil {
		t.Fatalf("client A submit after B used its own quota: %v", err)
	}

	// Now client A's own cap is exhausted; a second submit must fail.
	if _, err := f.Submit(context.Background(), SpawnRequest{
		Origin: OriginMCP, ClientID: "cA", RepoID: "r1", Title: "a again", Prompt: "y",
	}); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("client A second submit = %v, want ErrCapExceeded", err)
	}
}

// capturingTransport is a test transport that records the params of
// each JSON-RPC request and returns canned responses.
type capturingTransport struct {
	mu       sync.Mutex
	captured map[string]any // method -> params (as decoded map)
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
		t.mu.Unlock()

		var result any
		switch req.Method {
		case "session/new":
			result = map[string]any{"sessionId": "s-1"}
		default:
			result = map[string]any{}
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

func (t *capturingTransport) Wait() error                { return nil }
func (t *capturingTransport) Signal(sig os.Signal) error { return nil }
func (t *capturingTransport) Kill() error                { return nil }

func (t *capturingTransport) params(method string) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, _ := t.captured[method].(map[string]any)
	return v
}

// testFleetWithContainerizedAgent builds a fleet whose single agent has
// containerized: true and the given root, with a capturingTransport
// recording outbound requests.
func testFleetWithContainerizedAgent(t *testing.T, root string) (*Fleet, *capturingTransport) {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, "", Limits{}, "", nil, "marshal-state")
	tr := &capturingTransport{}
	f.newRuntime = func(a Agent) *Child { return &Child{Transport: tr} }
	t.Cleanup(f.Close)
	return f, tr
}

func TestStartPlanSendsTheAgentsViewOfThePlanPath(t *testing.T) {
	// The project root must be a real, writable directory: startPlan
	// writes the plan to the bridge-side path before translating it.
	root := t.TempDir()
	f, tr := testFleetWithContainerizedAgent(t, root)

	// The agent must exist in the workspace and have a live runtime.
	a := Agent{ID: "a1", Project: root, SourceKind: "local", Profile: DefaultRuntimeProfile()}
	if err := f.ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	// Start the runtime so rt.agentPath is available.
	rt, err := f.startRuntime(context.Background(), a)
	if err != nil {
		t.Fatalf("startRuntime: %v", err)
	}
	// Mark it containerized (the capturingTransport is not a *containerTransport,
	// so the type assertion in startRuntime set containerized=false).
	rt.containerized = true

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
	f, tr := testFleetWithContainerizedAgent(t, root)

	a := Agent{ID: "a1", Project: root, SourceKind: "local", Profile: DefaultRuntimeProfile()}
	if err := f.ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	rt, err := f.startRuntime(context.Background(), a)
	if err != nil {
		t.Fatalf("startRuntime: %v", err)
	}
	rt.containerized = true

	f.reconcileOnce(context.Background(), root)
	got, _ := tr.params("session/worktree_prune")["cwd"].(string)
	if got != "/work" {
		t.Fatalf("cwd = %q, want /work", got)
	}
}
