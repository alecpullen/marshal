package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/snapshot"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// lifecycleAppFixture is one conversation whose parent registry holds the real
// lifecycle tools over a real repository and one owned terminal child. It wires
// the production gate, write lock and command service, so a test exercises the
// serialization the runtime actually uses rather than a stand-in.
type lifecycleAppFixture struct {
	t      *testing.T
	repo   string
	owner  string
	state  *session.State
	lock   *swarm.WriteLock
	jobs   *native.JobManager
	gate   *subagentIdleGate
	svc    *subagentwork.Service
	record subagentwork.Record
	reg    *registry.Registry
	runner *agent.Runner
}

func newLifecycleAppFixture(t *testing.T) *lifecycleAppFixture {
	t.Helper()
	repo, _ := idleGateRepo(t)
	f := &lifecycleAppFixture{
		t:     t,
		repo:  repo,
		owner: "owner-1",
		lock:  &swarm.WriteLock{},
	}
	f.state = session.New(config.Default(), repo, time.Unix(100, 0), session.Persistence{SessionID: f.owner})
	f.jobs = native.NewJobManager(context.Background(), idleGateRunner{}, repo, 8, time.Hour, 1<<20)
	t.Cleanup(func() { _ = f.jobs.Shutdown(context.Background()) })
	f.gate = newSubagentIdleGate(f.state, f.lock, f.jobs, worktree.CLIGitOps{})
	f.svc = subagentwork.NewService(subagentwork.NewMemoryStore(), worktree.CLIGitOps{})

	ctx := context.Background()
	rec, err := f.svc.Prepare(ctx, subagentwork.PrepareRequest{
		OwnerSessionID:    f.owner,
		TaskKey:           "task-1",
		InvocationID:      "inv-1",
		ProjectRoot:       repo,
		ParentRoot:        repo,
		IsolatedSupported: true,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rec.Binding.Root, "candidate.txt"), []byte("child work\n"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	if out, err := runGitIn(rec.Binding.Root, "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := runGitIn(rec.Binding.Root, "commit", "-m", "child work"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	rec, err = f.svc.Complete(ctx, f.owner, "task-1", subagentwork.Completion{Outcome: subagentwork.OutcomeDone})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	f.record = rec

	f.reg = registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	pol.WithRegistry(f.reg)
	f.runner = agent.NewRunner(&agenttest.ScriptedProvider{}, f.reg, pol, f.state, "test-model")

	// Exactly the wiring the runtime installs: one gate serving the parent's
	// shared leases and the lifecycle's exclusive ones, and deps whose Admit the
	// tool supplies per call.
	deps := f.gate.lifecycleMutationDeps(nil)
	if err := f.reg.Register(agent.NewSubagentIntegrateTool(f.owner, f.svc, deps, f.runner.Authorizer)); err != nil {
		t.Fatalf("register agent.integrate: %v", err)
	}
	if err := f.reg.Register(agent.NewSubagentDiscardTool(f.owner, f.svc, deps, f.runner.Authorizer)); err != nil {
		t.Fatalf("register agent.discard: %v", err)
	}
	return f
}

// runGitIn runs one git command in dir and reports its output.
func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// dispatch runs one lifecycle tool exactly as dispatch does: it prepares the
// effect key, evaluates the call against the live authority, attaches that proof
// and invokes the handler. The proof is built here rather than by the runner's
// private dispatch loop because this package may not call it; the call facts are
// the same ones it would build.
func (f *lifecycleAppFixture) dispatch(ctx context.Context, toolName string) (registry.ToolResult, error) {
	f.t.Helper()
	argsMap := map[string]interface{}{
		"task_key":      f.record.TaskKey,
		"expected_head": f.record.Candidate,
	}
	raw, err := json.Marshal(argsMap)
	if err != nil {
		f.t.Fatalf("marshal args: %v", err)
	}
	tool, ok := f.reg.Lookup(toolName)
	if !ok {
		f.t.Fatalf("registry has no %s", toolName)
	}
	if tool.PrepareEffect == nil {
		f.t.Fatalf("%s prepares no effect key", toolName)
	}
	invocation := "call-" + toolName
	effectKey, prepErr := tool.PrepareEffect(ctx, registry.ToolCall{ID: invocation, Name: toolName, Args: raw})
	if prepErr != nil {
		return registry.ToolResult{}, prepErr
	}
	auth, _, evalErr := f.runner.Authorizer.Evaluate(ctx, policy.Call{
		InvocationID: invocation,
		ToolName:     toolName,
		Args:         argsMap,
		EffectKey:    effectKey,
	})
	if evalErr != nil {
		return registry.ToolResult{}, evalErr
	}
	return tool.Handler(policy.WithAuthorization(ctx, auth), registry.ToolCall{ID: invocation, Name: toolName, Args: raw})
}

// targetHead is the target checkout's current commit.
func (f *lifecycleAppFixture) targetHead(t *testing.T) string {
	t.Helper()
	out, err := runGitIn(f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v\n%s", err, out)
	}
	return out
}

// TestLifecycleDoesNotDeadlockSharedWrite pins the serialization contract with an
// ordinary write in the way: the lifecycle call completes (bounded) instead of
// deadlocking, and it does so again when the runtime write gate is held and its
// own context is cancelled. Once both are clear the same call succeeds.
func TestLifecycleDoesNotDeadlockSharedWrite(t *testing.T) {
	f := newLifecycleAppFixture(t)
	before := f.targetHead(t)
	ctx := context.Background()

	// An ordinary direct write is in flight at the target: it holds a shared
	// root-use lease for the length of its operation, which the exclusive
	// lifecycle lease refuses instead of blocking on.
	lease, err := f.gate.workspaceUse(ctx, f.repo, worktree.UseKindTool)
	if err != nil {
		t.Fatalf("workspaceUse: %v", err)
	}
	result, dispatchErr := f.dispatch(ctx, "agent.integrate")
	if dispatchErr == nil {
		t.Fatalf("the integration mutated underneath a live shared write:\n%s", result.Content)
	}
	if !strings.Contains(dispatchErr.Error(), "not idle") {
		t.Fatalf("error = %v, want a busy refusal rather than a blocked call", dispatchErr)
	}
	if err := lease.Release(true); err != nil {
		t.Fatalf("release the shared lease: %v", err)
	}
	if head := f.targetHead(t); head != before {
		t.Fatalf("the busy refusal moved the target to %s, want %s", head, before)
	}

	// The runtime write gate is held by an ordinary write, and this call's own
	// context is cancelled: it must return rather than wait forever.
	releaseGate := f.lock.Acquire()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, dispatchErr := f.dispatch(cancelled, "agent.integrate"); dispatchErr == nil {
		t.Fatal("a cancelled lifecycle call reported success")
	}
	releaseGate()
	if head := f.targetHead(t); head != before {
		t.Fatalf("the cancelled call moved the target to %s, want %s", head, before)
	}

	// Nothing holds the target now, so the same candidate integrates.
	result, dispatchErr = f.dispatch(ctx, "agent.integrate")
	if dispatchErr != nil {
		t.Fatalf("dispatch after the writes settled: %v\n%s", dispatchErr, result.Content)
	}
	if !strings.Contains(result.Content, f.record.Candidate) || !strings.Contains(result.Content, f.record.TaskKey) {
		t.Fatalf("receipt does not name the task key and candidate:\n%s", result.Content)
	}
	if head := f.targetHead(t); head == before {
		t.Fatal("the integration did not advance the target")
	}
}

// TestLifecycleManagedSnapshotTargetsTheRecordedRoot pins where the managed
// preflight captures: the checkout the RECORD named, not the parent's current
// root. It also pins the honest empty answer when this build has no snapshotter,
// which must never be reported as a hash.
func TestLifecycleManagedSnapshotTargetsTheRecordedRoot(t *testing.T) {
	f := newLifecycleAppFixture(t)
	ctx := context.Background()

	// No snapshotter configured: the callback reports no capture, and no
	// fabricated hash claims coverage that does not exist.
	if hash, err := subagentSnapshotAt(f.state)(ctx, f.record.Binding.Root); err != nil || hash != "" {
		t.Fatalf("capture with no snapshotter = %q, %v; want an empty hash and no error", hash, err)
	}

	// The snapshotter serves the parent's current root; the record names the
	// child's own worktree. A capture taken for the record must land on the
	// record's root.
	dataDir := t.TempDir()
	rooted := snapshot.NewRooted(dataDir, f.repo, func() string { return f.repo }, 1<<20, nil, f.state.Logger())
	f.state.SetSnapshotter(rooted)

	exactRoot := f.record.Binding.Root
	hash, err := subagentSnapshotAt(f.state)(ctx, exactRoot)
	if err != nil {
		t.Fatalf("capture at %s: %v", exactRoot, err)
	}
	if hash == "" {
		t.Fatal("the exact-root capture published no hash")
	}
	if _, err := rooted.Bound(exactRoot).LookupSnapshot(ctx, hash); err != nil {
		t.Fatalf("the capture does not resolve against the recorded root %s: %v", exactRoot, err)
	}
	if _, err := rooted.Bound(f.repo).LookupSnapshot(ctx, hash); err == nil {
		t.Fatal("the capture also resolves against the parent's current root, so it was not exact-root")
	}
}

// TestLifecycleToolsAreRegisteredForTheParentOnly pins the shape of the
// registration: the parent holds both managed tools, and the child scope view —
// the one filter a child's fully assembled registry passes through last — holds
// neither, while the tools a child needs for real work survive it.
func TestLifecycleToolsAreRegisteredForTheParentOnly(t *testing.T) {
	f := newLifecycleAppFixture(t)
	// The fixture registers only the lifecycle tools, so add one ordinary tool
	// the child MUST keep: without it the scope-view check below would prove
	// nothing about what survives the filter.
	if err := f.reg.Register(registry.Tool{
		Name: "file.read", Risk: registry.RiskReadOnly, Schema: []byte(`{"type":"object"}`),
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}); err != nil {
		t.Fatalf("register file.read: %v", err)
	}

	for _, name := range []string{"agent.integrate", "agent.discard"} {
		tool, ok := f.reg.Lookup(name)
		if !ok {
			t.Fatalf("the parent registry has no %s", name)
		}
		if !tool.OwnsAdmission || tool.WorkspaceEffect != registry.WorkspaceEffectManaged {
			t.Fatalf("%s is not a managed tool: owns=%v effect=%v", name, tool.OwnsAdmission, tool.WorkspaceEffect)
		}
		if tool.NeedsWorkspacePreflight() {
			t.Fatalf("%s kept the default root preflight", name)
		}
		if tool.PrepareEffect == nil {
			t.Fatalf("%s prepares no effect key", name)
		}
		if tool.ResolvePath != nil {
			t.Fatalf("%s resolves a path; its schema must name none", name)
		}
	}

	childView := agent.SubtaskScopeView(f.reg)
	for _, name := range []string{"agent.integrate", "agent.discard"} {
		if _, ok := childView.Lookup(name); ok {
			t.Fatalf("a child can see %s; integration and disposal are the parent's decisions", name)
		}
	}
	if _, ok := childView.Lookup("file.read"); !ok {
		t.Fatal("the child scope view lost file.read, which an implementation task needs")
	}
}
