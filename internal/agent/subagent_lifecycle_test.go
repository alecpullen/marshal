package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/permissions"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// lifecycleRepo initializes one git repository with a single commit and returns
// its canonical path. Every lifecycle test acts on a real temporary repository:
// nothing here reads the developer's checkout or their git configuration.
func lifecycleRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	lifecycleGit(t, resolved, "init", "-b", "main")
	lifecycleGit(t, resolved, "config", "user.name", "marshal test")
	lifecycleGit(t, resolved, "config", "user.email", "test@marshal.invalid")
	if err := os.WriteFile(filepath.Join(resolved, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatalf("write base: %v", err)
	}
	lifecycleGit(t, resolved, "add", "-A")
	lifecycleGit(t, resolved, "commit", "-m", "base")
	return resolved
}

// lifecycleGit runs one git command in dir and fails the test when it refuses.
func lifecycleGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// lifecycleMerges lists the merges a mutation created in dir: the commits that
// carry this package's operation trailer.
func lifecycleMerges(t *testing.T, dir string) []string {
	t.Helper()
	out := lifecycleGit(t, dir, "log", "--format=%H%x1f%B%x1e")
	var merges []string
	for _, entry := range strings.Split(out, "\x1e") {
		parts := strings.SplitN(entry, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.Contains(parts[1], "Marshal-Subagent-Operation:") {
			merges = append(merges, strings.TrimSpace(parts[0]))
		}
	}
	return merges
}

// lifecycleIdle is the explicit idle lease these tests hand the mutation. It
// records what the service asked for, can park the call at the lease boundary so
// a test can revoke authority while a task waits, and refuses on demand.
type lifecycleIdle struct {
	mu     sync.Mutex
	calls  int
	roots  [][]string
	enterr error
	// entered is closed on the first acquisition, so a test can act while the
	// mutation is provably waiting for the lease.
	entered chan struct{}
	// release, when non-nil, is what the acquisition waits on.
	release chan struct{}
	once    sync.Once
}

func newLifecycleIdle() *lifecycleIdle {
	return &lifecycleIdle{entered: make(chan struct{})}
}

// parked makes the next acquisition wait until the returned function is called,
// signalling the test the moment the mutation reaches the lease boundary.
func (g *lifecycleIdle) parked() func() {
	release := make(chan struct{})
	g.mu.Lock()
	g.release = release
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(release) }) }
}

func (g *lifecycleIdle) AcquireIdle(ctx context.Context, _ string, roots []string) (func(), error) {
	g.mu.Lock()
	g.calls++
	g.roots = append(g.roots, append([]string(nil), roots...))
	release := g.release
	err := g.enterr
	g.mu.Unlock()
	g.once.Do(func() { close(g.entered) })
	if err != nil {
		return nil, err
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return func() {}, nil
}

// lifecycleFixture is one parent conversation over a real repository, holding
// one owned terminal child whose candidate is ready to integrate.
type lifecycleFixture struct {
	t      *testing.T
	repo   string
	owner  string
	store  subagentwork.Store
	svc    *subagentwork.Service
	record subagentwork.Record
	state  *session.State
	pol    *policy.PolicyEngine
	reg    *registry.Registry
	runner *Runner
	idle   *lifecycleIdle

	mu      sync.Mutex
	snaps   []string
	rescans []string
}

// newLifecycleFixture builds the parent runtime and allocates one owned child
// with a committed candidate, so each test starts from exact-mutation-ready
// state rather than an invented record.
func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	ctx := context.Background()
	repo := lifecycleRepo(t)
	f := &lifecycleFixture{
		t:     t,
		repo:  repo,
		owner: "owner-1",
		idle:  newLifecycleIdle(),
	}
	f.store = subagentwork.NewMemoryStore()
	f.svc = subagentwork.NewService(f.store, worktree.CLIGitOps{})

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
	lifecycleGit(t, rec.Binding.Root, "add", "-A")
	lifecycleGit(t, rec.Binding.Root, "commit", "-m", "child work")
	rec, err = f.svc.Complete(ctx, f.owner, "task-1", subagentwork.Completion{Outcome: subagentwork.OutcomeDone})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if rec.Candidate == "" || rec.Binding.Root == "" {
		t.Fatalf("fixture child is not integration-ready: %+v", rec)
	}
	f.record = rec

	f.state = session.New(config.Default(), repo, time.Unix(100, 0), session.Persistence{SessionID: f.owner})
	f.reg = registry.New()
	f.pol = policy.NewEngine(&config.Config{}, nil)
	f.pol.WithRegistry(f.reg)
	f.runner = NewRunner(&agenttest.ScriptedProvider{}, f.reg, f.pol, f.state, "test-model")

	deps := f.mutationDeps()
	if err := f.reg.Register(NewSubagentIntegrateTool(f.owner, f.svc, deps, f.runner.Authorizer)); err != nil {
		t.Fatalf("register agent.integrate: %v", err)
	}
	if err := f.reg.Register(NewSubagentDiscardTool(f.owner, f.svc, deps, f.runner.Authorizer)); err != nil {
		t.Fatalf("register agent.discard: %v", err)
	}
	return f
}

// mutationDeps is the exact dependency set these tests give the service. Admit
// is deliberately absent: each handler call supplies its own.
func (f *lifecycleFixture) mutationDeps() subagentwork.MutationDeps {
	return subagentwork.MutationDeps{
		Idle: f.idle,
		Snapshot: func(_ context.Context, root string) (string, error) {
			f.mu.Lock()
			f.snaps = append(f.snaps, root)
			f.mu.Unlock()
			return "snapshot-" + filepath.Base(root), nil
		},
		Rescan: func(root string) {
			f.mu.Lock()
			f.rescans = append(f.rescans, root)
			f.mu.Unlock()
		},
	}
}

// args is the exact argument object both tools accept.
func (f *lifecycleFixture) args(key, head string) json.RawMessage {
	encoded, err := json.Marshal(map[string]string{"task_key": key, "expected_head": head})
	if err != nil {
		f.t.Fatalf("marshal args: %v", err)
	}
	return encoded
}

// dispatch runs one lifecycle tool the way the runner does and returns the
// model-facing messages.
func (f *lifecycleFixture) dispatch(t *testing.T, toolName, key, head string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msgs, err := f.runner.executeToolCall(ctx, ModelAction{
		Type:       ActionToolCall,
		Tool:       toolName,
		Args:       f.args(key, head),
		ToolCallID: "call-" + toolName,
	})
	if err != nil {
		t.Fatalf("%s dispatch: %v", toolName, err)
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

// proof evaluates one lifecycle call the way dispatch does and returns the
// authorization bound to it, with the exact argument map the handler must
// reproduce at admission.
func (f *lifecycleFixture) proof(t *testing.T, toolName, key, head string) (context.Context, *policy.Authorization) {
	t.Helper()
	args := f.args(key, head)
	argsMap := map[string]interface{}{}
	if err := json.Unmarshal(args, &argsMap); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	call, err := f.runner.policyCall(context.Background(), "call-"+toolName, toolName, argsMap)
	if err != nil {
		t.Fatalf("policyCall: %v", err)
	}
	auth, _, err := f.runner.Authorizer.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return policy.WithAuthorization(context.Background(), auth), auth
}

// handler calls one registered lifecycle tool's handler directly, which is the
// path an unapproved caller would take.
func (f *lifecycleFixture) handler(t *testing.T, ctx context.Context, toolName, key, head string) (registry.ToolResult, error) {
	t.Helper()
	tool, ok := f.reg.Lookup(toolName)
	if !ok {
		t.Fatalf("registry has no %s", toolName)
	}
	return tool.Handler(ctx, registry.ToolCall{ID: "call-" + toolName, Name: toolName, Args: f.args(key, head)})
}

// requireNoMutation pins the refusal contract: the target never moved, no merge
// was created and the owned artifacts are still there.
func (f *lifecycleFixture) requireNoMutation(t *testing.T, before string) {
	t.Helper()
	if head := lifecycleGit(t, f.repo, "rev-parse", "HEAD"); head != before {
		t.Fatalf("the target moved to %s during a refusal, want %s", head, before)
	}
	if merges := lifecycleMerges(t, f.repo); len(merges) != 0 {
		t.Fatalf("a refusal created merge commits: %v", merges)
	}
	if _, err := os.Stat(f.record.Binding.Root); err != nil {
		t.Fatalf("a refusal removed the owned worktree: %v", err)
	}
	branches := lifecycleGit(t, f.repo, "branch", "--list", "marshal/subagent/*")
	if strings.TrimSpace(branches) == "" {
		t.Fatal("a refusal deleted the owned branch")
	}
}

// requireReport pins that every receipt — a completed cleanup or a refusal —
// names the task key and the candidate it belongs to, so a client can pair it
// with the work without parsing prose.
func requireReport(t *testing.T, text, key, head string) {
	t.Helper()
	if !strings.Contains(text, key) {
		t.Fatalf("report does not name task key %q:\n%s", key, text)
	}
	if head != "" && !strings.Contains(text, head) {
		t.Fatalf("report does not name candidate %q:\n%s", head, text)
	}
}

// TestLifecycleToolsRequireParentProof pins that the managed tools are not
// ordinary tools: a handler reached without the dispatch proof mutates nothing,
// because the admission exists only at the mutation boundary.
func TestLifecycleToolsRequireParentProof(t *testing.T) {
	for _, toolName := range []string{"agent.integrate", "agent.discard"} {
		t.Run(toolName, func(t *testing.T) {
			f := newLifecycleFixture(t)
			before := lifecycleGit(t, f.repo, "rev-parse", "HEAD")

			result, err := f.handler(t, context.Background(), toolName, f.record.TaskKey, f.record.Candidate)
			if err == nil {
				t.Fatalf("%s ran without the parent's proof", toolName)
			}
			f.requireNoMutation(t, before)
			requireReport(t, result.Content+result.Summary, f.record.TaskKey, f.record.Candidate)
		})
	}
}

// TestLifecycleRevocationWhileQueued pins revocation at the lease boundary: a
// task that waits for the target lease and is then refused the authority it was
// evaluated under never reaches git.
func TestLifecycleRevocationWhileQueued(t *testing.T) {
	f := newLifecycleFixture(t)
	before := lifecycleGit(t, f.repo, "rev-parse", "HEAD")
	release := f.idle.parked()

	done := make(chan []string, 1)
	go func() { done <- f.dispatch(t, "agent.integrate", f.record.TaskKey, f.record.Candidate) }()

	// The mutation is provably waiting for the lease; revoke the editing
	// authority it was evaluated under, then let it proceed to admission.
	<-f.idle.entered
	if _, err := f.state.ApplyInteraction(interaction.Patch{Mode: modePtr(interaction.ModeAsk)}); err != nil {
		t.Fatalf("ApplyInteraction: %v", err)
	}
	release()

	select {
	case msgs := <-done:
		// The refusal happens at the mutation boundary, so it is reported as a
		// failed call rather than as an unstarted one — either way the model is
		// told it was not admitted.
		if len(msgs) != 1 || !strings.Contains(msgs[0], "not admitted") {
			t.Fatalf("messages = %v, want a refusal", msgs)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the mutation never returned")
	}
	f.requireNoMutation(t, before)
}

// TestLifecycleOwnedSiblingNeedsNoSystem pins that a valid owned sibling is
// integrated and discarded without System, because the mutation names a task key
// the record owns rather than a path the model chose — and that an arbitrary
// path argument, another owner's task key and a non-owned legacy record are all
// refused.
func TestLifecycleOwnedSiblingNeedsNoSystem(t *testing.T) {
	t.Run("integrate", func(t *testing.T) {
		f := newLifecycleFixture(t)
		before := lifecycleGit(t, f.repo, "rev-parse", "HEAD")

		msgs := f.dispatch(t, "agent.integrate", f.record.TaskKey, f.record.Candidate)
		if f.state.PendingApproval() != nil {
			t.Fatal("an owned sibling integration reached for System")
		}
		if len(msgs) != 1 {
			t.Fatalf("messages = %v", msgs)
		}
		requireReport(t, msgs[0], f.record.TaskKey, f.record.Candidate)

		after := lifecycleGit(t, f.repo, "rev-parse", "HEAD")
		if after == before {
			t.Fatal("the integration did not advance the target")
		}
		if parents := strings.Fields(lifecycleGit(t, f.repo, "rev-list", "--parents", "-n", "1", after)); len(parents) != 3 {
			t.Fatalf("the integration is not a merge commit: %v", parents)
		} else if parents[1] != before || parents[2] != f.record.Candidate {
			t.Fatalf("merge parents = %v, want [%s %s]", parents[1:], before, f.record.Candidate)
		}
	})

	t.Run("discard", func(t *testing.T) {
		f := newLifecycleFixture(t)
		msgs := f.dispatch(t, "agent.discard", f.record.TaskKey, f.record.Candidate)
		if f.state.PendingApproval() != nil {
			t.Fatal("an owned sibling discard reached for System")
		}
		if len(msgs) != 1 {
			t.Fatalf("messages = %v", msgs)
		}
		requireReport(t, msgs[0], f.record.TaskKey, f.record.Candidate)
		if _, err := os.Stat(f.record.Binding.Root); err == nil {
			t.Fatal("the discard retained the owned worktree")
		}
		if branches := strings.TrimSpace(lifecycleGit(t, f.repo, "branch", "--list", "marshal/subagent/*")); branches != "" {
			t.Fatalf("the discard retained owned branches: %q", branches)
		}
	})

	t.Run("arbitrary path", func(t *testing.T) {
		f := newLifecycleFixture(t)
		before := lifecycleGit(t, f.repo, "rev-parse", "HEAD")
		tool, ok := f.reg.Lookup("agent.integrate")
		if !ok {
			t.Fatal("registry has no agent.integrate")
		}
		args, err := json.Marshal(map[string]string{
			"task_key":      f.record.TaskKey,
			"expected_head": f.record.Candidate,
			"path":          "/tmp/elsewhere",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.ValidateArgs(tool, args); err == nil {
			t.Fatal("the schema accepted a path argument; a model must never choose the mutation target")
		}
		// The dispatch path rejects it before any handler runs.
		msgs, dispatchErr := f.runner.executeToolCall(context.Background(), ModelAction{
			Type: ActionToolCall, Tool: "agent.integrate", Args: args, ToolCallID: "call-path",
		})
		if dispatchErr != nil || len(msgs) != 1 || !strings.Contains(msgs[0].Content, "invalid tool arguments") {
			t.Fatalf("dispatch = %v, %v; want an argument rejection", msgs, dispatchErr)
		}
		f.requireNoMutation(t, before)
	})

	t.Run("another owner", func(t *testing.T) {
		f := newLifecycleFixture(t)
		before := lifecycleGit(t, f.repo, "rev-parse", "HEAD")
		// A record under another session's ownership is invisible to this
		// parent, so its task key can never name work this tool may mutate.
		if _, err := f.svc.Prepare(context.Background(), subagentwork.PrepareRequest{
			OwnerSessionID:    "owner-2",
			TaskKey:           "task-2",
			InvocationID:      "inv-2",
			ProjectRoot:       f.repo,
			ParentRoot:        f.repo,
			IsolatedSupported: true,
		}); err != nil {
			t.Fatalf("Prepare(owner-2): %v", err)
		}
		result, err := f.handler(t, context.Background(), "agent.integrate", "task-2", f.record.Candidate)
		if err == nil {
			t.Fatal("another owner's task key was mutated")
		}
		f.requireNoMutation(t, before)
		requireReport(t, result.Content+result.Summary, "task-2", f.record.Candidate)
	})

	t.Run("legacy record", func(t *testing.T) {
		f := newLifecycleFixture(t)
		before := lifecycleGit(t, f.repo, "rev-parse", "HEAD")
		if _, err := f.svc.Prepare(context.Background(), subagentwork.PrepareRequest{
			OwnerSessionID:    f.owner,
			TaskKey:           "task-shared",
			InvocationID:      "inv-shared",
			ProjectRoot:       f.repo,
			ParentRoot:        f.repo,
			Mode:              subagentwork.ModeShared,
			IsolatedSupported: true,
		}); err != nil {
			t.Fatalf("Prepare(shared): %v", err)
		}
		result, err := f.handler(t, context.Background(), "agent.integrate", "task-shared", f.record.Candidate)
		if err == nil {
			t.Fatal("a shared child, which owns no candidate, was integrated")
		}
		f.requireNoMutation(t, before)
		requireReport(t, result.Content+result.Summary, "task-shared", f.record.Candidate)
	})
}

// TestDiscardHonorsDestructivePolicy pins that discard is judged by the ordinary
// destructive-operation policy: Ask refuses it, a configured confirm rule asks
// and is honoured in both directions, and a configured deny rule refuses it.
func TestDiscardHonorsDestructivePolicy(t *testing.T) {
	askRule := []permissions.Rule{{Permission: "agent.discard", Pattern: "*", Action: permissions.ActionAsk}}
	denyRule := []permissions.Rule{{Permission: "agent.discard", Pattern: "*", Action: permissions.ActionDeny}}

	t.Run("ask mode denies", func(t *testing.T) {
		f := newLifecycleFixture(t)
		if _, err := f.state.ApplyInteraction(interaction.Patch{Mode: modePtr(interaction.ModeAsk)}); err != nil {
			t.Fatalf("ApplyInteraction: %v", err)
		}
		msgs := f.dispatch(t, "agent.discard", f.record.TaskKey, f.record.Candidate)
		if len(msgs) != 1 || !strings.Contains(msgs[0], "requires Act") {
			t.Fatalf("messages = %v, want an Ask refusal", msgs)
		}
		if _, err := os.Stat(f.record.Binding.Root); err != nil {
			t.Fatalf("an Ask refusal removed the owned worktree: %v", err)
		}
	})

	t.Run("configured confirmation denied", func(t *testing.T) {
		f := newLifecycleFixture(t)
		f.pol.SetRules(askRule)
		go answerApproval(t, f.state, session.UserApprovalDecision{Approved: false})
		msgs := f.dispatch(t, "agent.discard", f.record.TaskKey, f.record.Candidate)
		if len(msgs) != 1 || !strings.Contains(msgs[0], "denied by user") {
			t.Fatalf("messages = %v, want a declined confirmation", msgs)
		}
		if _, err := os.Stat(f.record.Binding.Root); err != nil {
			t.Fatalf("a declined confirmation removed the owned worktree: %v", err)
		}
	})

	t.Run("configured confirmation approved", func(t *testing.T) {
		f := newLifecycleFixture(t)
		f.pol.SetRules(askRule)
		go answerApproval(t, f.state, session.UserApprovalDecision{Approved: true})
		msgs := f.dispatch(t, "agent.discard", f.record.TaskKey, f.record.Candidate)
		if len(msgs) != 1 {
			t.Fatalf("messages = %v", msgs)
		}
		requireReport(t, msgs[0], f.record.TaskKey, f.record.Candidate)
		if _, err := os.Stat(f.record.Binding.Root); err == nil {
			t.Fatal("the approved discard retained the owned worktree")
		}
	})

	t.Run("configured deny", func(t *testing.T) {
		f := newLifecycleFixture(t)
		f.pol.SetRules(denyRule)
		msgs := f.dispatch(t, "agent.discard", f.record.TaskKey, f.record.Candidate)
		if len(msgs) != 1 || !strings.Contains(msgs[0], "denied") {
			t.Fatalf("messages = %v, want a denial", msgs)
		}
		if _, err := os.Stat(f.record.Binding.Root); err != nil {
			t.Fatalf("a denied discard removed the owned worktree: %v", err)
		}
	})
}

// TestLifecycleEffectKeyRejectsTargetHeadChange pins the effect key end to end: a
// target that advances between evaluation and mutation refuses the stale proof
// without touching git, and the same candidate integrates only after a fresh
// evaluation. A detached target branch is refused the same way.
func TestLifecycleEffectKeyRejectsTargetHeadChange(t *testing.T) {
	f := newLifecycleFixture(t)
	before := lifecycleGit(t, f.repo, "rev-parse", "HEAD")

	ctx, _ := f.proof(t, "agent.integrate", f.record.TaskKey, f.record.Candidate)

	// The target advances after the parent evaluated the candidate: another
	// child's integration, an operator's commit — either way the proof now
	// describes a target that no longer exists.
	if err := os.WriteFile(filepath.Join(f.repo, "concurrent.txt"), []byte("advanced\n"), 0o600); err != nil {
		t.Fatalf("write concurrent: %v", err)
	}
	lifecycleGit(t, f.repo, "add", "-A")
	lifecycleGit(t, f.repo, "commit", "-m", "advance the target")
	advanced := lifecycleGit(t, f.repo, "rev-parse", "HEAD")

	result, err := f.handler(t, ctx, "agent.integrate", f.record.TaskKey, f.record.Candidate)
	if err == nil {
		t.Fatal("a stale effect key was admitted")
	}
	if head := lifecycleGit(t, f.repo, "rev-parse", "HEAD"); head != advanced {
		t.Fatalf("the stale proof mutated the target to %s, want %s", head, advanced)
	}
	if merges := lifecycleMerges(t, f.repo); len(merges) != 0 {
		t.Fatalf("the stale proof created merge commits: %v", merges)
	}
	requireReport(t, result.Content+result.Summary, f.record.TaskKey, f.record.Candidate)

	// A fresh evaluation sees the new target facts and is admitted.
	fresh, _ := f.proof(t, "agent.integrate", f.record.TaskKey, f.record.Candidate)
	result, err = f.handler(t, fresh, "agent.integrate", f.record.TaskKey, f.record.Candidate)
	if err != nil {
		t.Fatalf("the freshly evaluated candidate was refused: %v\n%s", err, result.Content)
	}
	if merges := lifecycleMerges(t, f.repo); len(merges) != 1 {
		t.Fatalf("merges after the fresh evaluation = %v, want one", merges)
	}
	if head := lifecycleGit(t, f.repo, "rev-parse", "HEAD"); head == advanced {
		t.Fatal("the fresh evaluation did not advance the target")
	}
	_ = before
}

// TestLifecycleDetachedTargetIsRefused pins the drift case the record already
// knows about: a target checkout that is no longer on the recorded branch cannot
// receive an integration, and no effect key is prepared for it.
func TestLifecycleDetachedTargetIsRefused(t *testing.T) {
	f := newLifecycleFixture(t)
	detached := lifecycleGit(t, f.repo, "rev-parse", "HEAD")
	lifecycleGit(t, f.repo, "checkout", "--detach", detached)

	tool, ok := f.reg.Lookup("agent.integrate")
	if !ok {
		t.Fatal("registry has no agent.integrate")
	}
	if tool.PrepareEffect == nil {
		t.Fatal("agent.integrate prepares no effect key")
	}
	if _, err := tool.PrepareEffect(context.Background(), registry.ToolCall{
		ID: "call-integrate", Name: "agent.integrate", Args: f.args(f.record.TaskKey, f.record.Candidate),
	}); err == nil {
		t.Fatal("a detached target still produced an effect key")
	}
	if merges := lifecycleMerges(t, f.repo); len(merges) != 0 {
		t.Fatalf("a detached target received a merge: %v", merges)
	}
}

// TestLifecycleToolsAreManagedOnlyTools pins the registry contract that makes
// the managed path possible and bounded: only these two tools own their
// admission, and they own their own exact-root preflight, so dispatch neither
// consumes their proof nor takes the workspace write gate for them.
func TestLifecycleToolsAreManagedOnlyTools(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, name := range []string{"agent.integrate", "agent.discard"} {
		tool, ok := f.reg.Lookup(name)
		if !ok {
			t.Fatalf("registry has no %s", name)
		}
		if !tool.OwnsAdmission {
			t.Fatalf("%s does not own its admission", name)
		}
		if tool.WorkspaceEffect != registry.WorkspaceEffectManaged {
			t.Fatalf("%s workspace effect = %v, want managed", name, tool.WorkspaceEffect)
		}
		if tool.NeedsWorkspacePreflight() {
			t.Fatalf("%s kept the default root preflight, which would deadlock the lifecycle lease", name)
		}
		if tool.PrepareEffect == nil {
			t.Fatalf("%s prepares no effect key", name)
		}
		if tool.ResolvePath != nil {
			t.Fatalf("%s exposes a path resolver; its arguments must name no filesystem path", name)
		}
	}
	if risk, _ := f.reg.Lookup("agent.integrate"); risk.Risk != registry.RiskWorkspaceWrite {
		t.Fatalf("agent.integrate risk = %v, want workspace_write", risk.Risk)
	}
	if risk, _ := f.reg.Lookup("agent.discard"); risk.Risk != registry.RiskDestructive {
		t.Fatalf("agent.discard risk = %v, want destructive", risk.Risk)
	}
}

// TestLifecycleRefusesUnsupportedService pins that the tools refuse rather than
// invent an answer when this build wires no owned-workspace service.
func TestLifecycleRefusesUnsupportedService(t *testing.T) {
	tool := NewSubagentIntegrateTool("owner-1", nil, subagentwork.MutationDeps{}, nil)
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "agent.integrate", Args: json.RawMessage(`{"task_key":"task-1","expected_head":"abc"}`),
	}); err == nil {
		t.Fatal("a tool with no workspace service integrated something")
	}
}

// TestLifecycleRefusesNilAuthorizer pins that a managed tool refuses to admit a
// mutation without the live authorizer, rather than treating a missing decision
// as permission.
func TestLifecycleRefusesNilAuthorizer(t *testing.T) {
	f := newLifecycleFixture(t)
	deps := f.mutationDeps()
	deps.Admit = nil
	tool := NewSubagentDiscardTool(f.owner, f.svc, deps, nil)
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "agent.discard", Args: f.args(f.record.TaskKey, f.record.Candidate),
	})
	if err == nil {
		t.Fatal("a managed tool admitted a mutation without an authorizer")
	}
	if _, statErr := os.Stat(f.record.Binding.Root); statErr != nil {
		t.Fatalf("the refused discard removed the owned worktree: %v", statErr)
	}
	var approvalErr error
	if !errors.Is(err, policy.ErrApprovalRequired) && !strings.Contains(err.Error(), "authoriz") {
		approvalErr = err
	}
	_ = approvalErr
}
