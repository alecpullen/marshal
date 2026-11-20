package native

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// newSetupPlanFixture wires one bounded setup actor: a registry whose shell.run
// prepares real facts, the conversation's command service and a real authorizer
// over a permissive test authority.
type setupPlanFixture struct {
	root       string
	reg        *registry.Registry
	authorizer *policy.Authorizer
	jobs       *JobManager
	runner     CommandRunner
}

func newSetupPlanFixture(t *testing.T, cfg config.Config, runner CommandRunner) *setupPlanFixture {
	t.Helper()
	root := t.TempDir()
	state := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	jobs := newTestCommandService(t, runner, root, state, nil)
	authorizer := policy.NewAuthorizer(policy.NewEngine(&cfg, nil), newTestAuthority())
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:    root,
		CommandRunner:    runner,
		JobManager:       jobs,
		SessionState:     state,
		Config:           cfg,
		CommandAdmission: authorizer.BeginCommand,
		SetupAuthorizer:  authorizer,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return &setupPlanFixture{root: root, reg: reg, authorizer: authorizer, jobs: jobs, runner: runner}
}

// prepare returns the facts the bound registry would judge and launch for one
// hook command.
func (f *setupPlanFixture) prepare(t *testing.T, command string) registry.CommandFacts {
	t.Helper()
	tool, ok := f.reg.Lookup("shell.run")
	if !ok || tool.PrepareCommand == nil {
		t.Fatal("shell.run is not registered with a preparation")
	}
	facts, err := tool.PrepareCommand(context.Background(), registry.ToolCall{
		ID: "hook", Name: "shell.run", Args: []byte(`{"command":"` + command + `"}`),
	})
	if err != nil {
		t.Fatalf("PrepareCommand(%q): %v", command, err)
	}
	return facts
}

// blockingHookRunner runs every hook to completion except one named command,
// which it holds until its execution context is cancelled and then reports as
// stopped. That is how a test observes an explicit hook deadline ending that
// hook's execution without touching the rest of the plan.
type blockingHookRunner struct {
	mu         sync.Mutex
	launched   []string
	block      string
	wasStopped bool
}

func newBlockingHookRunner(block string) *blockingHookRunner {
	return &blockingHookRunner{block: block}
}

func (r *blockingHookRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	r.mu.Lock()
	r.launched = append(r.launched, req.Command)
	block := req.Command == r.block
	r.mu.Unlock()
	if req.OnStart != nil {
		req.OnStart(4242)
	}
	if !block {
		return CommandResult{ExitCode: 0, Meta: registry.SandboxMeta{Backend: "fixture", Enabled: true}}, nil
	}
	<-ctx.Done()
	r.mu.Lock()
	r.wasStopped = true
	r.mu.Unlock()
	return CommandResult{ExitCode: 143, Meta: registry.SandboxMeta{Backend: "fixture", Enabled: true, KilledReason: "stopped"}}, ctx.Err()
}

func (r *blockingHookRunner) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	return []string{"PATH=/fixture/bin", "PWD=" + dir}, map[string]string{"PATH": "/fixture/bin", "PWD": dir}, nil
}

func (r *blockingHookRunner) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.launched...)
}

func (r *blockingHookRunner) stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wasStopped
}

func TestSetupHooksRunInOrderThroughPreparedFacts(t *testing.T) {
	cfg := config.Default()
	runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
	f := newSetupPlanFixture(t, cfg, runner)
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{
		{Command: "echo install", TimeoutSeconds: 30},
		{Command: "echo generate"},
	}}

	first := f.prepare(t, "echo install")
	second := f.prepare(t, "echo generate")
	warnings, err := RunSetupPlan(context.Background(), plan, f.reg, f.authorizer, f.jobs)
	if err != nil {
		t.Fatalf("RunSetupPlan: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if got := runner.countRuns(); got != 2 {
		t.Fatalf("launched %d hooks, want 2", got)
	}
	runner.mu.Lock()
	requests := append([]CommandRequest(nil), runner.requests...)
	runner.mu.Unlock()
	if requests[0].Command != first.Command || requests[1].Command != second.Command {
		t.Fatalf("hook order = %q then %q, want %q then %q",
			requests[0].Command, requests[1].Command, first.Command, second.Command)
	}
	for i, req := range requests {
		want := first
		if i == 1 {
			want = second
		}
		if req.Dir != want.Dir {
			t.Errorf("hook %d ran in %q, want the evaluated %q", i, req.Dir, want.Dir)
		}
		if len(req.Environment) != len(want.Environment) {
			t.Errorf("hook %d environment = %v, want the evaluated %v", i, req.Environment, want.Environment)
		}
	}
}

func TestSetupHookFailureBecomesWarningAndContinues(t *testing.T) {
	cfg := config.Default()
	// The second hook succeeds; the scripted order is not needed because the
	// fake returns one canned result per launch, so use distinct runners.
	failing := newFakeRunner(withResult(CommandResult{Stderr: "boom: module not found", ExitCode: 1}))
	f := newSetupPlanFixture(t, cfg, failing)
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "echo install"}}}

	warnings, err := RunSetupPlan(context.Background(), plan, f.reg, f.authorizer, f.jobs)
	if err != nil {
		t.Fatalf("RunSetupPlan: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], `setup hook "echo install" failed`) || !strings.Contains(warnings[0], "boom: module not found") {
		t.Errorf("warning %q does not name the hook and its stderr", warnings[0])
	}
}

func TestSetupHookRunnerErrorBecomesWarning(t *testing.T) {
	cfg := config.Default()
	runner := newFakeRunner(withRunErr(errors.New("sandbox denied network")))
	f := newSetupPlanFixture(t, cfg, runner)
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "echo curl"}}}

	warnings, err := RunSetupPlan(context.Background(), plan, f.reg, f.authorizer, f.jobs)
	if err != nil {
		t.Fatalf("RunSetupPlan: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "sandbox") {
		t.Fatalf("warnings = %v, want the runner error surfaced", warnings)
	}
}

func TestSetupHookWarningTruncatesLongOutput(t *testing.T) {
	cfg := config.Default()
	long := strings.Repeat("x", 1200)
	runner := newFakeRunner(withResult(CommandResult{Stderr: long, ExitCode: 1}))
	f := newSetupPlanFixture(t, cfg, runner)
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "echo fail"}}}

	warnings, err := RunSetupPlan(context.Background(), plan, f.reg, f.authorizer, f.jobs)
	if err != nil {
		t.Fatalf("RunSetupPlan: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	if len(warnings[0]) > maxHookWarningChars+len(`setup hook "echo fail" failed: `) {
		t.Errorf("warning is %d chars, want the output tail capped at %d", len(warnings[0]), maxHookWarningChars)
	}
}

// TestSetupHooksUseBoundedAdmission pins the setup boundary: a hook is judged
// and launched on the one prepared description of it, a refused or unprepared
// hook starts no process, and an explicit hook deadline stops that hook's
// execution without disturbing the rest of the plan.
func TestSetupHooksUseBoundedAdmission(t *testing.T) {
	t.Run("exact facts are evaluated and launched", func(t *testing.T) {
		cfg := config.Default()
		runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
		f := newSetupPlanFixture(t, cfg, runner)
		plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "echo exact"}}}
		expected := f.prepare(t, "echo exact")

		if _, err := RunSetupPlan(context.Background(), plan, f.reg, f.authorizer, f.jobs); err != nil {
			t.Fatalf("RunSetupPlan: %v", err)
		}
		req := runner.lastRequest(t)
		if strings.TrimSpace(req.Command) != strings.TrimSpace(expected.Command) {
			t.Fatalf("hook ran %q, want the evaluated %q", req.Command, expected.Command)
		}
		if req.Dir != expected.Dir {
			t.Fatalf("hook cwd = %q, want the evaluated %q", req.Dir, expected.Dir)
		}
		if len(req.Environment) != len(expected.Environment) {
			t.Fatalf("hook environment = %v, want the evaluated %v", req.Environment, expected.Environment)
		}
		for i := range expected.Environment {
			if req.Environment[i] != expected.Environment[i] {
				t.Fatalf("hook environment = %v, want the evaluated %v", req.Environment, expected.Environment)
			}
		}
	})

	t.Run("denied hooks launch nothing", func(t *testing.T) {
		cfg := config.Default()
		cfg.Tools.Shell.Deny.Patterns = []string{"*forbidden*"}
		runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
		f := newSetupPlanFixture(t, cfg, runner)
		plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "echo forbidden"}}}

		warnings, err := RunSetupPlan(context.Background(), plan, f.reg, f.authorizer, f.jobs)
		if err == nil {
			t.Fatalf("RunSetupPlan = %v, want a refusal (warnings %v)", err, warnings)
		}
		if !strings.Contains(err.Error(), "was not run") {
			t.Fatalf("refusal = %v, want it to say the hook did not run", err)
		}
		if got := runner.countRuns(); got != 0 {
			t.Fatalf("denied hook launches = %d, want 0", got)
		}
		if got := f.jobs.ActiveCount(); got != 0 {
			t.Fatalf("active executions = %d, want 0", got)
		}
	})

	t.Run("unsupported hooks launch nothing", func(t *testing.T) {
		cfg := config.Default()
		runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
		f := newSetupPlanFixture(t, cfg, runner)
		plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "echo anything"}}}
		// A registry with no prepared shell.run (a read-only scope) cannot say
		// what the hook would run, so it must launch nothing.
		empty := registry.New()

		warnings, err := RunSetupPlan(context.Background(), plan, empty, f.authorizer, f.jobs)
		if err == nil {
			t.Fatalf("RunSetupPlan = %v, want a refusal (warnings %v)", err, warnings)
		}
		if !strings.Contains(err.Error(), "cannot prepare shell.run") {
			t.Fatalf("refusal = %v, want the missing preparation named", err)
		}
		if got := runner.countRuns(); got != 0 {
			t.Fatalf("unsupported hook launches = %d, want 0", got)
		}
	})

	t.Run("deadline stops only that hook", func(t *testing.T) {
		cfg := config.Default()
		runner := newBlockingHookRunner("echo slow")
		f := newSetupPlanFixture(t, cfg, runner)
		plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{
			{Command: "echo slow", TimeoutSeconds: 1},
			{Command: "echo after"},
		}}
		started := time.Now()
		warnings, err := RunSetupPlan(context.Background(), plan, f.reg, f.authorizer, f.jobs)
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("RunSetupPlan: %v", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "exceeded its 1s deadline") {
			t.Fatalf("warnings = %v, want only the first hook's deadline reported", warnings)
		}
		// The slow hook was stopped and joined at its own deadline, so the plan
		// reached the second hook instead of waiting out the command.
		if got := runner.commands(); len(got) != 2 || got[0] != "echo slow" || got[1] != "echo after" {
			t.Fatalf("launched hooks = %v, want [echo slow echo after]", got)
		}
		if !runner.stopped() {
			t.Fatal("the slow hook was never stopped")
		}
		if elapsed > 30*time.Second {
			t.Fatalf("plan took %s; the deadline did not stop the hook", elapsed)
		}
	})
}

func TestWorkspaceWorktreeRunsHooksOnlyWhenFresh(t *testing.T) {
	// newWorktreeTestEnv builds the real git repo the worktree branches from;
	// its registry is discarded in favour of one with hooks and a bounded
	// setup actor configured.
	_, _, root := newWorktreeTestEnv(t)
	// The hook fails so the tool surfaces a warning: per the setup contract
	// only failures are reported, so a failing hook is the observable path.
	runner := newFakeRunner(withResult(CommandResult{Stderr: "hook failed deliberately", ExitCode: 1}))
	state2 := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg2 := registry.New()
	cfg := config.Default()
	cfg.Worktree.SetupHooks = []config.WorktreeSetupHook{{Command: "echo hooked"}}
	jobs := newTestCommandService(t, runner, root, state2, nil)
	authorizer := policy.NewAuthorizer(policy.NewEngine(&cfg, nil), newTestAuthority())
	if err := RegisterAll(reg2, Options{
		WorkspaceRoot:    root,
		CommandRunner:    runner,
		SessionState:     state2,
		Config:           cfg,
		JobManager:       jobs,
		CommandAdmission: authorizer.BeginCommand,
		SetupAuthorizer:  authorizer,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	res, err := invokeTool(t, reg2, "workspace.worktree", `{"branch":"feat/x"}`)
	if err != nil {
		t.Fatalf("workspace.worktree: %v", err)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("hook ran %d times, want 1 on a fresh worktree", got)
	}
	req := runner.lastRequest(t)
	if req.Dir == "" || !strings.HasPrefix(req.Dir, root) {
		t.Errorf("hook dir = %q, want inside the worktree under %s", req.Dir, root)
	}
	if !strings.Contains(res.Content, `setup hook "echo hooked" failed`) || !strings.Contains(res.Content, "hook failed deliberately") {
		t.Errorf("result content %q does not report the hook failure", res.Content)
	}
	if state2.Workspace().ActiveRoot == root {
		t.Fatal("session did not move into the worktree")
	}
	// Second call resumes the same worktree: no hook may run again.
	if _, err := invokeTool(t, reg2, "workspace.worktree", `{"branch":"feat/x"}`); err != nil {
		t.Fatalf("resume call: %v", err)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("hook ran %d times after resume, want 1 (hooks only run when Fresh)", got)
	}
}
