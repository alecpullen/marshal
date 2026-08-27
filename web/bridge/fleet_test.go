package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testFleet(t *testing.T) *Fleet {
	t.Helper()
	return newTestFleetWithLimit(t, 4)
}

func newTestFleetWithLimit(t *testing.T, limit int) *Fleet {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, "")
	bin, args, env := helperCommand("registry")
	f.newRuntime = func(a Agent) *Child { return &Child{MarshalBin: bin, Args: args, Env: env} }
	f.slots = newSlots(limit)
	t.Cleanup(f.Close)
	return f
}

func TestFleetSpawnsChildLazilyPerProject(t *testing.T) {
	f := testFleet(t)
	ctx := context.Background()
	if _, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "one"}); err != nil {
		t.Fatal(err)
	}
	if len(f.runtimes) != 1 {
		t.Fatalf("runtimes = %d, want 1", len(f.runtimes))
	}
	if _, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "two"}); err != nil {
		t.Fatal(err)
	}
	if len(f.runtimes) != 2 {
		t.Fatalf("same project spawned same runtime count, want 2")
	}
	if _, err := f.Spawn(ctx, "/home/u/b", SpawnOptions{Name: "three"}); err != nil {
		t.Fatal(err)
	}
	if len(f.runtimes) != 3 {
		t.Fatalf("runtimes = %d, want 3", len(f.runtimes))
	}
}

func TestFleetResolvesSessionToProject(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/home/u/a", SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if rt.root != "/home/u/a" {
		t.Fatalf("root = %q", rt.root)
	}
	if _, err := f.RuntimeForSession("unknown"); err != ErrUnknownSession {
		t.Fatalf("err = %v", err)
	}
}

func TestSpawnIsolatedRecordsBranchAndTarget(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()

	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "fix", Mode: "edit", Isolated: true, BaseRef: "HEAD"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, ok := f.ws.Agent(id)
	if !ok {
		t.Fatal("agent not recorded")
	}
	if !a.Isolated {
		t.Error("Isolated must be recorded")
	}
	// TargetBranch cannot be derived later — it must be persisted at spawn.
	if a.TargetBranch == "" {
		t.Error("TargetBranch must be recorded at spawn")
	}
	if a.Branch == "" {
		t.Error("Branch must be recorded")
	}
}

func TestMergeClearsIsolationOnSuccess(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, "")
	bin, args, env := helperCommand("registry-merged")
	f.newRuntime = func(a Agent) *Child { return &Child{MarshalBin: bin, Args: args, Env: env} }
	t.Cleanup(f.Close)

	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "fix", Mode: "edit", Isolated: true, BaseRef: "HEAD"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, _ := f.ws.Agent(id)
	if !a.Isolated {
		t.Fatal("agent must start isolated")
	}

	if _, err := f.Merge(ctx, id, ""); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// A successful merge returns the session to the project root; the agent
	// must no longer be reported as isolated.
	a, _ = f.ws.Agent(id)
	if a.Isolated || a.Branch != "" || a.TargetBranch != "" {
		t.Errorf("agent still isolated after successful merge: %+v", a)
	}
}

func TestSpawnNonIsolatedRecordsNoBranch(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()

	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "quick", Mode: "edit"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, _ := f.ws.Agent(id)
	if a.Isolated || a.Branch != "" {
		t.Errorf("expected a non-isolated agent, got %+v", a)
	}
}

func TestProjectStatusReportsIsolationUnavailableOutsideAGitRepo(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir() // a directory, not a git repo
	if err := ws.AddProject(plain); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, "")
	t.Cleanup(f.Close)

	for _, st := range f.ProjectStatus() {
		if st.Root != plain {
			continue
		}
		if st.Isolation == "available" {
			t.Error("isolation must not be available outside a git repo")
		}
		if st.Isolation == "" {
			t.Error("Isolation must carry a reason the composer can display")
		}
	}
}

func TestReconcileWorktreesRecordsOrphans(t *testing.T) {
	f := testFleet(t)
	ctx, cancel := testContext(t)
	defer cancel()
	// Bring a project up so it has a runtime to ask.
	if _, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "x"}); err != nil {
		t.Fatal(err)
	}

	f.ReconcileWorktrees(ctx)

	// The fake child answers session/worktree_prune with one unknown entry.
	var found bool
	for _, st := range f.ProjectStatus() {
		if st.Root == "/home/u/a" && len(st.OrphanWorktrees) > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an orphan recorded for the project: %+v", f.ProjectStatus())
	}
}

func TestFleetSpawnFailureIsReported(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "not-a-real-marshal-binary", nil, "")
	defer f.Close()
	if _, err := f.Spawn(context.Background(), "/home/u/a", SpawnOptions{Name: "one"}); err == nil {
		t.Fatal("expected spawn failure")
	}
	statuses := f.ProjectStatus()
	if len(statuses) != 1 || statuses[0].Available || statuses[0].Error == "" {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestSpawnPassesAgentEnvToContainer(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, "")
	t.Cleanup(f.Close)

	// Invoke the production newRuntime closure directly so no real
	// container or session/new round-trip is needed. The closure must
	// copy f.agentEnv into the ContainerConfig.
	child := f.newRuntime(Agent{ID: "abc", Project: "/p", Profile: RuntimeProfile{Image: "img"}})
	tr, ok := child.Transport.(*containerTransport)
	if !ok {
		// No container runtime on this host: the production closure falls
		// back to a host process, so the container env path is not
		// exercised. Skip rather than fail.
		t.Skip("no container runtime detected; container env path not exercised")
	}
	if tr.cfg.Env["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Fatalf("agent env = %v, want the provider key to reach the container", tr.cfg.Env)
	}
}

func TestTwoAgentsOnOneProjectGetSeparateRuntimes(t *testing.T) {
	f := testFleet(t)

	first, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "one"})
	if err != nil {
		t.Fatalf("Spawn first: %v", err)
	}
	second, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "two"})
	if err != nil {
		t.Fatalf("Spawn second: %v", err)
	}
	if first == second {
		t.Fatal("both spawns returned the same agent id")
	}

	rt1, err := f.runtimeForAgent(first)
	if err != nil {
		t.Fatalf("runtimeForAgent(first): %v", err)
	}
	rt2, err := f.runtimeForAgent(second)
	if err != nil {
		t.Fatalf("runtimeForAgent(second): %v", err)
	}
	if rt1 == rt2 {
		t.Fatal("two agents on one project shared a runtime; each needs its own container")
	}
	if rt1.child == rt2.child {
		t.Fatal("two agents shared a Child")
	}
}

func TestAgentIDIsMintedBeforeTheSessionExists(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatalf("runtimeForAgent: %v", err)
	}
	if rt.id != id {
		t.Fatalf("runtime id = %q, want %q", rt.id, id)
	}
	// The ACP session id is assigned by the agent and is a separate value.
	if rt.sessionID == id {
		t.Fatal("agent id and ACP session id must be distinct values")
	}
}

func TestNewAgentIDIsUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := newAgentID()
		if seen[id] {
			t.Fatalf("newAgentID collided on %q", id)
		}
		seen[id] = true
	}
}

func TestReattachAllReconnectsPersistedAgents(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Simulate a control-plane restart: drop every live runtime but keep
	// the persisted records, exactly as a process restart would.
	f.dropRuntimesForTest()
	if _, err := f.runtimeForAgent(id); err == nil {
		t.Fatal("runtime survived the simulated restart; the test is not exercising reattach")
	}

	if errs := f.ReattachAll(context.Background()); len(errs) != 0 {
		t.Fatalf("ReattachAll: %v", errs)
	}
	if _, err := f.runtimeForAgent(id); err != nil {
		t.Fatalf("agent %s was not reattached: %v", id, err)
	}
}

func TestPauseStopsContainerAndKeepsRecord(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := f.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := f.runtimeForAgent(id); err == nil {
		t.Fatal("paused agent still has a live runtime")
	}

	agents := f.ws.Agents()
	var found bool
	for _, a := range agents {
		if a.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("Pause removed the persisted agent record; it must survive")
	}
}

func TestPauseFreesASlot(t *testing.T) {
	f := newTestFleetWithLimit(t, 1)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := f.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := f.Spawn(ctx, "/p", SpawnOptions{Prompt: "two"}); err != nil {
		t.Fatalf("Spawn after Pause: %v — the paused agent did not free its slot", err)
	}
}

func TestPauseResumeRoundTrip(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Pause: stops the container, keeps the record, frees the slot.
	if err := f.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := f.runtimeForAgent(id); err == nil {
		t.Fatal("paused agent still has a live runtime")
	}

	// Resume: re-acquires a slot and starts a fresh runtime.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.Resume(ctx, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatalf("runtimeForAgent after Resume: %v", err)
	}
	if rt.id != id {
		t.Fatalf("runtime id = %q, want %q", rt.id, id)
	}

	// The persisted record must still be present.
	if _, ok := f.ws.Agent(id); !ok {
		t.Fatal("Resume lost the persisted agent record")
	}
}

func TestResumeIsNoOpWhenAlreadyRunning(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Resume on a running agent must be a no-op, not an error.
	if err := f.Resume(context.Background(), id); err != nil {
		t.Fatalf("Resume on running agent: %v", err)
	}
}

// dropRuntimesForTest discards every live runtime without touching the
// persisted records, standing in for a control-plane restart.
func (f *Fleet) dropRuntimesForTest() {
	f.mu.Lock()
	f.runtimes = make(map[string]*agentRuntime)
	f.sessionAgent = make(map[string]string)
	f.mu.Unlock()
}

func TestReattachAllRestoresSessionMapping(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatalf("runtimeForAgent: %v", err)
	}
	sessionID := rt.sessionID
	if sessionID == "" {
		t.Fatal("Spawn left no session id")
	}

	f.dropRuntimesForTest()
	if errs := f.ReattachAll(context.Background()); len(errs) != 0 {
		t.Fatalf("ReattachAll: %v", errs)
	}

	// The agent must be reachable by its session id, not merely present.
	if _, err := f.liveRuntimeForSession(sessionID); err != nil {
		t.Fatalf("reattached agent is not addressable by session %s: %v", sessionID, err)
	}
	rt2, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatalf("runtimeForAgent after reattach: %v", err)
	}
	if rt2.sessionID != sessionID {
		t.Fatalf("session id = %q after reattach, want %q", rt2.sessionID, sessionID)
	}
}

func TestSpawnPersistsSessionID(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, ok := f.ws.Agent(id)
	if !ok {
		t.Fatalf("agent %s not persisted", id)
	}
	if a.SessionID == "" {
		t.Fatal("SessionID was not persisted; reattach cannot restore the session")
	}
	if a.SessionID == a.ID {
		t.Fatal("SessionID equals the agent id; the two must stay distinct")
	}
}

func TestRuntimeForSessionDoesNotLoadAnAgentIDAsASession(t *testing.T) {
	f := testFleet(t)
	id, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, _ := f.ws.Agent(id)
	f.dropRuntimesForTest()

	// Look the agent up by AGENT id; the restore must use the persisted
	// session id, never the agent id.
	rt, err := f.RuntimeForSession(id)
	if err != nil {
		t.Fatalf("RuntimeForSession(agent id): %v", err)
	}
	if rt.sessionID != a.SessionID {
		t.Fatalf("restored session %q, want the persisted %q", rt.sessionID, a.SessionID)
	}
}

func TestSpawnAgainstRegisteredRepoIsWritable(t *testing.T) {
	f := testFleet(t)
	if f.git == nil {
		t.Skip("git not installed")
	}
	if err := f.ws.PutRepo(Repo{ID: "r1", URL: newBareRepoFixture(t),
		Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}

	id, err := f.Spawn(context.Background(), "", SpawnOptions{RepoID: "r1", Prompt: "x"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, _ := f.ws.Agent(id)
	if a.SourceKind != "git" {
		t.Errorf("SourceKind = %q, want git", a.SourceKind)
	}
	if a.ReadOnly {
		t.Error("a registered repo must not produce a read-only agent")
	}
	if a.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want main (the repo's default branch)", a.TargetBranch)
	}
}

func TestSpawnAgainstRawURLIsReadOnly(t *testing.T) {
	f := testFleet(t)
	if f.git == nil {
		t.Skip("git not installed")
	}
	id, err := f.Spawn(context.Background(), "", SpawnOptions{
		URL: newBareRepoFixture(t), Ref: "main", Prompt: "x",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, _ := f.ws.Agent(id)
	if !a.ReadOnly {
		t.Fatal("an unregistered URL must produce a read-only agent")
	}
}

func TestRawURLIsRejectedForNonUIOrigins(t *testing.T) {
	f := testFleet(t)
	_, err := f.Spawn(context.Background(), "", SpawnOptions{
		URL: newBareRepoFixture(t), Ref: "main", Origin: OriginMCP, Prompt: "x",
	})
	if !errors.Is(err, ErrUnregisteredRepo) {
		t.Fatalf("MCP spawn against a raw URL = %v, want ErrUnregisteredRepo", err)
	}
}

func TestLocalPathSpawnStillWorks(t *testing.T) {
	f := testFleet(t)
	if _, err := f.Spawn(context.Background(), "/p", SpawnOptions{Prompt: "x"}); err != nil {
		t.Fatalf("local-path spawn regressed: %v", err)
	}
}
