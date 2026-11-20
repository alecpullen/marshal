package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// gitRepoIn runs one git command inside dir and returns its trimmed output.
func gitRepoIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newDispatchRepo builds a real repository the dispatch fixture seeds from, with
// the global and system git configuration blanked so no test contacts a remote
// or inherits a machine setting.
func newDispatchRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitRepoIn(t, dir, "init", "-b", "main")
	gitRepoIn(t, dir, "config", "user.name", "marshal test")
	gitRepoIn(t, dir, "config", "user.email", "test@marshal.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitRepoIn(t, dir, "add", "-A")
	gitRepoIn(t, dir, "commit", "-m", "base")
	return dir
}

// newDispatchState builds the parent conversation over a real directory. With
// persist it also gives the state a database and a session row, which is what
// makes the durable report reference (and therefore the record's
// ReportMessageID) real.
func newDispatchState(t *testing.T, sessionID, dir string, cfg config.Config, persist bool) (*session.State, *db.DB) {
	t.Helper()
	if !persist {
		return session.New(cfg, dir, time.Now(), session.Persistence{SessionID: sessionID}), nil
	}
	database := openTestDB(t)
	projectID, err := database.GetOrCreateProject(dir, "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession(sessionID, projectID, "work", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return session.New(cfg, dir, time.Now(), session.Persistence{DB: database, SessionID: sessionID, Logger: logger}), database
}

// newRestartedState rebuilds one conversation over an EXISTING database, which
// is what a process restart does: the durable rows are still there and nothing
// is live.
func newRestartedState(t *testing.T, sessionID, dir string, cfg config.Config, database *db.DB) *session.State {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return session.New(cfg, dir, time.Now(), session.Persistence{DB: database, SessionID: sessionID, Logger: logger})
}

// dispatchFixture is one conversation dispatching through the production
// agent.run handler with a real owned-workspace service.
type dispatchFixture struct {
	t     *testing.T
	repo  string
	cfg   config.Config
	state *session.State
	store subagentwork.Store
	svc   *subagentwork.Service
	tool  registry.Tool

	// exec is the child's model run.
	exec func(ctx context.Context, child *Runner, prompt string) (string, string, error)

	execCalls    atomic.Int32
	factoryCalls atomic.Int32
	mu           sync.Mutex
	requests     []SubagentRequest
	childRoots   []string
}

// newDispatchFixture wires the fixture. mutate may adjust the config before the
// state and the tool are built; exec may be replaced by the caller.
func newDispatchFixture(t *testing.T, sessionID string, mutate func(*config.Config, *dispatchFixture), execFn func(context.Context, *Runner, string) (string, string, error)) *dispatchFixture {
	t.Helper()
	cfg := config.Default()
	f := &dispatchFixture{t: t, cfg: cfg, repo: newDispatchRepo(t)}
	if mutate != nil {
		mutate(&f.cfg, f)
	}
	state, _ := newDispatchState(t, sessionID, f.repo, f.cfg, false)
	f.state = state
	f.store = subagentwork.NewMemoryStore()
	f.svc = subagentwork.NewService(f.store, worktree.CLIGitOps{})
	f.exec = execFn
	if f.exec == nil {
		f.exec = func(context.Context, *Runner, string) (string, string, error) { return "done", "", nil }
	}
	f.tool = NewSubagentTool(f.factory, nil, registry.New(), state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			f.execCalls.Add(1)
			return f.exec(ctx, child, prompt)
		}),
		WithSubagentWorkspaceService(f.svc),
		WithIsolatedWorktreeSupport(true),
	)
	return f
}

// factory builds a child bound to the workspace the dispatcher recorded, the way
// the production factory does through bindSubagentWorkspace.
func (f *dispatchFixture) factory(req SubagentRequest) (*Runner, *session.State, error) {
	f.factoryCalls.Add(1)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.childRoots = append(f.childRoots, req.Binding.Root)
	f.mu.Unlock()
	child := session.New(f.cfg, req.Binding.Root, time.Now(), session.Persistence{SessionID: "child-" + req.TaskKey})
	child.BindIsolatedWorkspace(req.Binding.Root)
	return &Runner{State: child}, child, nil
}

// recordedRequests returns the requests the factory was invoked with.
func (f *dispatchFixture) recordedRequests() []SubagentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SubagentRequest(nil), f.requests...)
}

// records lists the conversation's retained workspace records.
func (f *dispatchFixture) records() []subagentwork.Record {
	recs, err := f.store.List(context.Background(), f.state.SessionID())
	if err != nil {
		f.t.Fatalf("list records: %v", err)
	}
	return recs
}

// run dispatches one agent.run call and returns its result.
func (f *dispatchFixture) run(ctx context.Context, args string) registry.ToolResult {
	f.t.Helper()
	res, err := f.tool.Handler(ctx, registry.ToolCall{ID: "call", Name: "agent.run", Args: json.RawMessage(args)})
	if err != nil {
		f.t.Fatalf("agent.run(%s): %v", args, err)
	}
	return res
}

// workReference extracts the machine-readable work reference from a dispatch
// receipt.
func workReference(t *testing.T, res registry.ToolResult) registry.SubagentWorkReference {
	t.Helper()
	if res.Notice == nil {
		t.Fatal("dispatch receipt carries no notice")
	}
	ref, ok := res.Notice.Data[registry.NoticeSubagentWork].(registry.SubagentWorkReference)
	if !ok {
		t.Fatalf("dispatch notice data = %#v, want a SubagentWorkReference", res.Notice.Data)
	}
	return ref
}

// waitView waits for the child to settle.
func waitView(t *testing.T, state *session.State, id int64) session.SubagentView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	view, err := state.WaitSubagent(ctx, id)
	if err != nil {
		t.Fatalf("WaitSubagent(%d): %v", id, err)
	}
	return view
}

// dispatchOrigin is the execution origin one logical tool call carries.
func dispatchOrigin(turn, step, call string) context.Context {
	return session.WithExecutionOrigin(context.Background(), session.ExecutionOrigin{
		TurnID: turn, StepID: step, ToolCallID: call,
	})
}

// TestRunWorkspaceModeValidation pins the dispatch contract at the argument
// boundary: omitted means isolated, explicit shared is honored, and every
// refused spelling fails before a worktree is allocated or a provider is
// approached.
func TestRunWorkspaceModeValidation(t *testing.T) {
	f := newDispatchFixture(t, "mode-owner", nil, nil)

	// Omitted: isolated.
	f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"x","description":"isolated"}`)
	reqs := f.recordedRequests()
	if len(reqs) != 1 {
		t.Fatalf("factory invocations = %d, want 1", len(reqs))
	}
	if reqs[0].Binding.Mode != subagentwork.ModeIsolated {
		t.Fatalf("omitted workspace mode = %q, want isolated", reqs[0].Binding.Mode)
	}
	if reqs[0].Binding.Root == worktree.CanonicalPath(f.repo) {
		t.Fatal("an isolated child was bound to the parent's own checkout")
	}
	waitView(t, f.state, f.state.Subagents()[0].ID)

	// Explicit shared: honored, and bound to the captured parent root.
	f.run(dispatchOrigin("t1", "s2", "c2"), `{"prompt":"x","description":"shared","workspace":"shared"}`)
	reqs = f.recordedRequests()
	if len(reqs) != 2 {
		t.Fatalf("factory invocations = %d, want 2", len(reqs))
	}
	if reqs[1].Binding.Mode != subagentwork.ModeShared {
		t.Fatalf("explicit workspace mode = %q, want shared", reqs[1].Binding.Mode)
	}
	if reqs[1].Binding.Root != worktree.CanonicalPath(f.repo) {
		t.Fatalf("shared child root = %q, want the captured parent root", reqs[1].Binding.Root)
	}
	waitView(t, f.state, f.state.Subagents()[1].ID)

	// Every refusal below happens before allocation and before the factory.
	for _, tc := range []struct {
		name string
		args string
	}{
		{"explicit empty", `{"prompt":"x","description":"d","workspace":""}`},
		{"unknown", `{"prompt":"x","description":"d","workspace":"bogus"}`},
		{"shared with base", `{"prompt":"x","description":"d","workspace":"shared","base_commit":"HEAD"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.recordedRequests())
			recordsBefore := len(f.records())
			_, err := f.tool.Handler(context.Background(), registry.ToolCall{ID: "call", Name: "agent.run", Args: json.RawMessage(tc.args)})
			if err == nil {
				t.Fatalf("agent.run(%s) succeeded, want a refusal", tc.args)
			}
			if got := len(f.recordedRequests()); got != before {
				t.Fatalf("factory invocations = %d, want the refusal to build no child", got)
			}
			if got := len(f.records()); got != recordsBefore {
				t.Fatalf("records = %d, want the refusal to allocate nothing", got)
			}
		})
	}
}

// TestDuplicateRunDoesNotStartSecondChild pins deduplication: the same logical
// invocation observes the record it already owns, starts no second child and
// creates no second worktree; an ambiguous prior start reports unknown.
func TestDuplicateRunDoesNotStartSecondChild(t *testing.T) {
	release := make(chan struct{})
	f := newDispatchFixture(t, "duplicate-owner", nil, func(ctx context.Context, _ *Runner, _ string) (string, string, error) {
		<-release
		return "done", "", nil
	})

	ctx := dispatchOrigin("t1", "s1", "c1")
	first := f.run(ctx, `{"prompt":"x","description":"one"}`)
	ref := workReference(t, first)
	if ref.TaskKey == "" {
		t.Fatal("the dispatch receipt carries no task key")
	}

	// A live duplicate returns the original receipt and starts nothing. The
	// first child is still inside its model run here, so this is the in-flight
	// duplicate a retried call actually produces.
	second := f.run(ctx, `{"prompt":"x","description":"one"}`)
	if second.Summary != first.Summary {
		t.Fatalf("duplicate receipt = %q, want the original %q", second.Summary, first.Summary)
	}
	if ref2 := workReference(t, second); ref2.TaskKey != ref.TaskKey || ref2.AgentID != ref.AgentID {
		t.Fatalf("duplicate work reference = %+v, want the original %+v", ref2, ref)
	}
	if got := f.factoryCalls.Load(); got != 1 {
		t.Fatalf("factory invocations = %d, want exactly 1", got)
	}
	if got := len(f.state.Subagents()); got != 1 {
		t.Fatalf("registered cards = %d, want 1", got)
	}
	if got := len(f.records()); got != 1 {
		t.Fatalf("retained records = %d, want 1", got)
	}
	close(release)
	waitView(t, f.state, ref.AgentID)

	// An ambiguous prior start (an intent that never reached prepared) reports
	// unknown rather than running a second child.
	ambiguousCtx := dispatchOrigin("t2", "s1", "c1")
	ambiguousKey, ambiguousInvocation := subagentTaskIdentity(ambiguousCtx, f.state.SessionID())
	if _, _, err := f.store.Create(context.Background(), subagentwork.Record{
		Version:        subagentwork.RecordVersion,
		OwnerSessionID: f.state.SessionID(),
		TaskKey:        ambiguousKey,
		InvocationID:   ambiguousInvocation,
		Outcome:        subagentwork.OutcomePreparing,
	}); err != nil {
		t.Fatalf("seed ambiguous record: %v", err)
	}
	beforeFactory := f.factoryCalls.Load()
	res := f.run(ambiguousCtx, `{"prompt":"x","description":"ambiguous"}`)
	if !strings.Contains(res.Summary, "unknown") {
		t.Fatalf("ambiguous dispatch summary = %q, want an unknown-start report", res.Summary)
	}
	if got := f.factoryCalls.Load(); got != beforeFactory {
		t.Fatalf("factory invocations = %d, want the ambiguous start to build no child", got)
	}
}

// TestRunReceiptAndCompletionShareWorkspaceIdentity pins that the receipt, the
// card, the retained record and the persisted report name one preallocated
// task: the same key, worktree, target and observed candidate.
func TestRunReceiptAndCompletionShareWorkspaceIdentity(t *testing.T) {
	f := newDispatchFixture(t, "identity-owner", nil, func(ctx context.Context, child *Runner, _ string) (string, string, error) {
		root := child.State.Workspace().ActiveRoot
		if err := os.WriteFile(filepath.Join(root, "work.txt"), []byte("child work\n"), 0o644); err != nil {
			return "", "", err
		}
		gitRepoIn(t, root, "add", "-A")
		gitRepoIn(t, root, "commit", "-m", "child work")
		return "committed", "", nil
	})
	// A persisted conversation is what makes the report reference real.
	state, database := newDispatchState(t, "identity-owner", f.repo, f.cfg, true)
	f.state = state
	store := db.NewSubagentWorkspaceStore(database)
	f.store = store
	f.svc = subagentwork.NewService(store, worktree.CLIGitOps{})
	// The tool built by the fixture captured the memory-backed service, so the
	// dispatch handler is rebuilt against the durable one.
	f.tool = NewSubagentTool(f.factory, nil, registry.New(), state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			f.execCalls.Add(1)
			return f.exec(ctx, child, prompt)
		}),
		WithSubagentWorkspaceService(f.svc),
		WithIsolatedWorktreeSupport(true),
	)

	receipt := f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"x","description":"identity"}`)
	ref := workReference(t, receipt)
	view := waitView(t, f.state, ref.AgentID)

	rec, err := f.svc.Inspect(context.Background(), state.SessionID(), ref.TaskKey)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if rec.TaskKey != ref.TaskKey {
		t.Fatalf("record key = %q, receipt key = %q", rec.TaskKey, ref.TaskKey)
	}
	if view.WorkKey != ref.TaskKey {
		t.Fatalf("card work key = %q, receipt key = %q", view.WorkKey, ref.TaskKey)
	}
	if rec.Binding.Mode != subagentwork.ModeIsolated {
		t.Fatalf("record mode = %q, want isolated", rec.Binding.Mode)
	}
	if rec.Binding.Root == worktree.CanonicalPath(f.repo) {
		t.Fatal("record bound the child to the parent's checkout")
	}
	if rec.Target.Root != worktree.CanonicalPath(f.repo) {
		t.Fatalf("record target root = %q, want the captured parent root", rec.Target.Root)
	}
	// The candidate is the child's actual HEAD after its commit.
	candidate := gitRepoIn(t, rec.Binding.Root, "rev-parse", "HEAD")
	if rec.Candidate != candidate {
		t.Fatalf("record candidate = %q, want the child's HEAD %q", rec.Candidate, candidate)
	}
	if rec.Binding.BaseSHA == rec.Candidate {
		t.Fatal("the child's commit did not move its candidate off the base commit")
	}
	if rec.Outcome != subagentwork.OutcomeDone {
		t.Fatalf("record outcome = %q, want done", rec.Outcome)
	}
	if rec.ReportMessageID == 0 {
		t.Fatal("record carries no persisted report reference")
	}
	report := state.SubagentReportText(rec.ReportMessageID)
	if report == "" {
		t.Fatal("the referenced report body is not in the conversation")
	}
	for _, want := range []string{"task: " + ref.TaskKey, "candidate: " + candidate, "workspace: isolated"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report is missing %q:\n%s", want, report)
		}
	}
	// The receipt names the same workspace facts before the run even starts.
	for _, want := range []string{"task: " + ref.TaskKey, "branch: marshal/subagent/" + ref.TaskKey} {
		if !strings.Contains(receipt.Content, want) {
			t.Fatalf("dispatch receipt is missing %q:\n%s", want, receipt.Content)
		}
	}
}

// failRunner is a command runner whose every execution reports a non-zero exit,
// standing in for a setup hook that ran and failed. It captures a fixed launch
// environment because P1's setup path prepares the hook's real facts before
// admitting it.
type failRunner struct{ calls atomic.Int32 }

func (r *failRunner) Run(context.Context, native.CommandRequest) (native.CommandResult, error) {
	r.calls.Add(1)
	code := 1
	return native.CommandResult{ExitCode: code, Meta: registry.SandboxMeta{Backend: "fixture", Enabled: true}}, nil
}

func (r *failRunner) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	return []string{"PATH=/fixture/bin", "PWD=" + dir}, map[string]string{"PATH": "/fixture/bin", "PWD": dir}, nil
}

// TestRunSetupFailureRetainsOwnedArtifacts pins that required setup runs before
// the model: a hook that fails fails the dispatch, spends no model tokens, and
// leaves the branch and worktree retained as evidence.
func TestRunSetupFailureRetainsOwnedArtifacts(t *testing.T) {
	runner := &failRunner{}
	var jobs *native.JobManager
	var childRegistry func(root string, state *session.State) *registry.Registry

	f := newDispatchFixture(t, "setup-owner", func(cfg *config.Config, f *dispatchFixture) {
		cfg.Worktree.SetupHooks = []config.WorktreeSetupHook{{Command: "make generate"}}
	}, nil)
	jobs = native.NewCommandService(context.Background(), native.LaunchPolicy{
		Runner:         runner,
		Dir:            f.repo,
		MaxOutputBytes: 1 << 20,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = jobs.Shutdown(ctx)
	})
	f.state.SetTurnStopService(jobs)

	childRegistry = func(root string, child *session.State) *registry.Registry {
		authorizer := policy.NewAuthorizer(policy.NewEngine(&f.cfg, nil), child.InteractionAuthority())
		reg := registry.New()
		if err := native.RegisterAll(reg, native.Options{
			WorkspaceRoot:    root,
			CommandRunner:    runner,
			JobManager:       jobs,
			SessionState:     child,
			Config:           f.cfg,
			CommandAdmission: authorizer.BeginCommand,
			SetupAuthorizer:  authorizer,
		}); err != nil {
			t.Fatalf("child registry: %v", err)
		}
		return reg
	}

	// Rebuild the tool with a factory that builds the child's real registry and
	// authorizer, which is what P1's setup path admits through.
	f.tool = NewSubagentTool(func(req SubagentRequest) (*Runner, *session.State, error) {
		f.factoryCalls.Add(1)
		child := session.New(f.cfg, req.Binding.Root, time.Now(), session.Persistence{SessionID: "child-" + req.TaskKey})
		child.BindIsolatedWorkspace(req.Binding.Root)
		reg := childRegistry(req.Binding.Root, child)
		childPol := policy.NewEngine(&f.cfg, nil)
		childPol.WithRegistry(reg)
		return &Runner{State: child, Registry: reg, Authorizer: policy.NewAuthorizer(childPol, child.InteractionAuthority())}, child, nil
	}, nil, registry.New(), f.state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			f.execCalls.Add(1)
			return f.exec(ctx, child, prompt)
		}),
		WithSubagentWorkspaceService(f.svc),
		WithIsolatedWorktreeSupport(true),
	)

	receipt := f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"x","description":"setup"}`)
	ref := workReference(t, receipt)
	view := waitView(t, f.state, ref.AgentID)

	if view.Status != session.SubagentFailed {
		t.Fatalf("card status = %v, want failed after a failed required setup", view.Status)
	}
	if got := f.execCalls.Load(); got != 0 {
		t.Fatalf("child model runs = %d, want 0 (a failed setup consumes no model tokens)", got)
	}
	if got := runner.calls.Load(); got == 0 {
		t.Fatalf("the required setup hook never ran (card error: %s); the test proves nothing about a failed hook", view.Error)
	}
	rec, err := f.svc.Inspect(context.Background(), f.state.SessionID(), ref.TaskKey)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if rec.Outcome != subagentwork.OutcomeFailed {
		t.Fatalf("record outcome = %q, want failed", rec.Outcome)
	}
	if rec.Binding.Root == "" || rec.Binding.Branch == "" {
		t.Fatalf("record = %+v, want the retained worktree and branch as ownership evidence", rec.Binding)
	}
	if _, err := os.Stat(rec.Binding.Root); err != nil {
		t.Fatalf("the owned worktree was removed instead of retained: %v", err)
	}
	if branches := gitRepoIn(t, f.repo, "branch", "--list", rec.Binding.Branch); branches == "" {
		t.Fatalf("branch %q was silently deleted", rec.Binding.Branch)
	}
}

// TestRunPanicCancelAndPartialRetainCandidate pins every terminal path: the
// record is settled before the card announces its terminal status, and the
// outcome stays truthful for a panic, a cancel and a partial report.
func TestRunPanicCancelAndPartialRetainCandidate(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		f := newDispatchFixture(t, "panic-owner", nil, func(context.Context, *Runner, string) (string, string, error) {
			panic("boom in the child")
		})
		receipt := f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"x","description":"panic"}`)
		ref := workReference(t, receipt)
		waitView(t, f.state, ref.AgentID)
		rec, err := f.svc.Inspect(context.Background(), f.state.SessionID(), ref.TaskKey)
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if rec.Outcome != subagentwork.OutcomeFailed {
			t.Fatalf("panicked child outcome = %q, want failed", rec.Outcome)
		}
		if rec.Binding.Root == "" {
			t.Fatal("panicked child's workspace evidence was lost")
		}
		if _, err := os.Stat(rec.Binding.Root); err != nil {
			t.Fatalf("panicked child's worktree was removed: %v", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		f := newDispatchFixture(t, "cancel-owner", nil, func(ctx context.Context, _ *Runner, _ string) (string, string, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return "", "", ctx.Err()
		})
		receipt := f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"x","description":"cancel"}`)
		ref := workReference(t, receipt)
		<-entered
		if !f.state.CancelSubagent(ref.AgentID) {
			t.Fatal("the registered child has no cancel handle")
		}
		waitView(t, f.state, ref.AgentID)
		rec, err := f.svc.Inspect(context.Background(), f.state.SessionID(), ref.TaskKey)
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if rec.Outcome != subagentwork.OutcomeStopped {
			t.Fatalf("cancelled child outcome = %q, want stopped", rec.Outcome)
		}
	})

	t.Run("partial", func(t *testing.T) {
		f := newDispatchFixture(t, "partial-owner", nil, func(context.Context, *Runner, string) (string, string, error) {
			return "half a report", "exhausted", nil
		})
		receipt := f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"x","description":"partial"}`)
		ref := workReference(t, receipt)
		waitView(t, f.state, ref.AgentID)
		rec, err := f.svc.Inspect(context.Background(), f.state.SessionID(), ref.TaskKey)
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if rec.Outcome != subagentwork.OutcomePartial {
			t.Fatalf("salvaged child outcome = %q, want partial", rec.Outcome)
		}
	})
}

// TestAwaitTaskKeySurvivesRestart pins the retained lookup: the owner inspects a
// finished task's report and candidate by key after a restart, no child is
// revived, and another owner cannot read it.
func TestAwaitTaskKeySurvivesRestart(t *testing.T) {
	f := newDispatchFixture(t, "restart-owner", nil, func(ctx context.Context, child *Runner, _ string) (string, string, error) {
		root := child.State.Workspace().ActiveRoot
		if err := os.WriteFile(filepath.Join(root, "candidate.txt"), []byte("candidate\n"), 0o644); err != nil {
			return "", "", err
		}
		gitRepoIn(t, root, "add", "-A")
		gitRepoIn(t, root, "commit", "-m", "candidate")
		return "the retained report", "", nil
	})
	state, database := newDispatchState(t, "restart-owner", f.repo, f.cfg, true)
	f.state = state
	store := db.NewSubagentWorkspaceStore(database)
	f.store = store
	f.svc = subagentwork.NewService(store, worktree.CLIGitOps{})
	f.tool = NewSubagentTool(f.factory, nil, registry.New(), state,
		WithSubagentExec(func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
			f.execCalls.Add(1)
			return f.exec(ctx, child, prompt)
		}),
		WithSubagentWorkspaceService(f.svc),
		WithIsolatedWorktreeSupport(true),
	)

	receipt := f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"x","description":"restart"}`)
	ref := workReference(t, receipt)
	waitView(t, f.state, ref.AgentID)

	// A fresh conversation state over the SAME database is what a restart looks
	// like: no live children, the durable records still there.
	restarted := newRestartedState(t, "restart-owner", f.repo, f.cfg, database)
	if len(restarted.Subagents()) != 0 {
		t.Fatal("precondition: a restarted conversation has no live child")
	}
	await := NewSubagentAwaitTool(restarted, WithAwaitWorkspaces(f.svc))
	res, err := await.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(fmt.Sprintf(`{"task_key":%q}`, ref.TaskKey)),
	})
	if err != nil {
		t.Fatalf("await task_key: %v", err)
	}
	if !strings.Contains(res.Content, "candidate: ") {
		t.Fatalf("retained lookup carries no candidate:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "the retained report") {
		t.Fatalf("retained lookup carries no report:\n%s", res.Content)
	}
	if len(restarted.Subagents()) != 0 {
		t.Fatal("the retained lookup revived a live child")
	}

	// Another owner cannot query it, even with the exact key.
	other, _ := newDispatchState(t, "other-owner", f.repo, f.cfg, false)
	otherAwait := NewSubagentAwaitTool(other, WithAwaitWorkspaces(f.svc))
	if _, err := otherAwait.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(fmt.Sprintf(`{"task_key":%q}`, ref.TaskKey)),
	}); err == nil {
		t.Fatal("another owner read a retained task it does not own")
	}
}

// TestConcurrentChildrenHaveIndependentCommits pins real isolation: two
// genuinely concurrent children edit the same path in different trees, commit
// in different indexes and leave the original checkout untouched.
func TestConcurrentChildrenHaveIndependentCommits(t *testing.T) {
	var mu sync.Mutex
	entered := make(chan string, 2)
	release := make(chan struct{})
	f := newDispatchFixture(t, "concurrent-owner", nil, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		root := child.State.Workspace().ActiveRoot
		mu.Lock()
		entered <- prompt
		mu.Unlock()
		<-release
		if err := os.WriteFile(filepath.Join(root, "shared.go"), []byte("package "+prompt+"\n"), 0o644); err != nil {
			return "", "", err
		}
		gitRepoIn(t, root, "add", "-A")
		gitRepoIn(t, root, "commit", "-m", "work "+prompt)
		return "done " + prompt, "", nil
	})

	first := f.run(dispatchOrigin("t1", "s1", "c1"), `{"prompt":"alpha","description":"alpha"}`)
	second := f.run(dispatchOrigin("t1", "s2", "c2"), `{"prompt":"beta","description":"beta"}`)
	firstRef := workReference(t, first)
	secondRef := workReference(t, second)
	if firstRef.TaskKey == secondRef.TaskKey {
		t.Fatal("two different logical calls share one task key")
	}

	// Both children must be inside their own model run before either proceeds,
	// so this is a genuine concurrency test rather than two sequential runs.
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the two children never ran concurrently")
		}
	}
	close(release)
	waitView(t, f.state, firstRef.AgentID)
	waitView(t, f.state, secondRef.AgentID)

	records := f.records()
	if len(records) != 2 {
		t.Fatalf("retained records = %d, want 2", len(records))
	}
	byKey := map[string]subagentwork.Record{}
	for _, rec := range records {
		byKey[rec.TaskKey] = rec
	}
	a, b := byKey[firstRef.TaskKey], byKey[secondRef.TaskKey]
	if a.Binding.Root == b.Binding.Root {
		t.Fatalf("both children work in %q, want one worktree each", a.Binding.Root)
	}
	if a.Candidate == b.Candidate {
		t.Fatalf("both children produced candidate %q, want independent commits", a.Candidate)
	}
	// Same path, different content, different index: each commit is its own.
	dataA, err := os.ReadFile(filepath.Join(a.Binding.Root, "shared.go"))
	if err != nil {
		t.Fatalf("read child A's file: %v", err)
	}
	dataB, err := os.ReadFile(filepath.Join(b.Binding.Root, "shared.go"))
	if err != nil {
		t.Fatalf("read child B's file: %v", err)
	}
	if string(dataA) == string(dataB) {
		t.Fatalf("both children's shared.go = %q, want each its own content", dataA)
	}
	// The original checkout saw none of it.
	if status := gitRepoIn(t, f.repo, "status", "--porcelain"); status != "" {
		t.Fatalf("the parent checkout is dirty after two isolated children:\n%s", status)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "shared.go")); err == nil {
		t.Fatal("a child's edit landed in the parent checkout")
	}
	if head := gitRepoIn(t, f.repo, "rev-parse", "HEAD"); head != a.Target.BaseSHA {
		t.Fatalf("parent HEAD = %q, want the unchanged captured base %q", head, a.Target.BaseSHA)
	}
}

// TestCommitFirstAndSharedInstructions pins the honest shipped instructions: a
// parent commits prerequisites, an isolated child commits its own work, a
// shared child leaves commits to the parent, and integration of a retained
// branch is described as an explicit git step (no lifecycle tool is promised).
func TestCommitFirstAndSharedInstructions(t *testing.T) {
	tool := NewSubagentTool(func(SubagentRequest) (*Runner, *session.State, error) { return &Runner{}, nil, nil },
		nil, registry.New(), session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}))

	for _, want := range []string{
		"worktree",
		"committed HEAD",
		"commits its own scoped work",
		`"workspace": "shared"`,
		"leaves commits to you",
	} {
		if !strings.Contains(tool.Description, want) {
			t.Fatalf("agent.run description is missing %q:\n%s", want, tool.Description)
		}
	}

	// The core prompt's delegation paragraph carries the same contract.
	for _, want := range []string{
		"committed HEAD",
		"commit what they need",
		"merge their branch",
		`"shared"`,
		"you commit",
	} {
		if !strings.Contains(delegationWorkspaceDirective, want) {
			t.Fatalf("delegation directive is missing %q:\n%s", want, delegationWorkspaceDirective)
		}
	}

	for name, wants := range map[string][]string{
		"using-worktrees.md": {
			"committed** HEAD",
			"An isolated child commits its own scoped work",
			"A shared child never commits",
			"Integration is explicit",
		},
		"dispatching-parallel-agents.md": {
			"Commit first.",
			"committed HEAD",
		},
		"marshal-executing-plans.md": {
			"isolated child starts at your committed HEAD",
			"merge the retained branch",
		},
	} {
		raw, err := os.ReadFile(filepath.Join("..", "skills", "builtin", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := strings.Join(strings.Fields(string(raw)), " ")
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Fatalf("%s is missing %q", name, want)
			}
		}
		// Honesty: no unavailable lifecycle tool is advertised as existing.
		for _, banned := range []string{"workspace.integrate", "workspace.discard"} {
			if strings.Contains(body, banned) {
				t.Fatalf("%s names %s, which does not exist yet", name, banned)
			}
		}
	}
}
