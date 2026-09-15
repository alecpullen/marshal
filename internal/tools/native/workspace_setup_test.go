package native

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

// scriptedRunner returns a canned result per Run call, in the order the
// results were configured. It records every request so tests can assert
// order, dir, and timeout.
type scriptedRunner struct {
	results  []CommandResult
	errs     []error
	requests []CommandRequest
}

func (f *scriptedRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	f.requests = append(f.requests, req)
	var result CommandResult
	var err error
	if len(f.results) > 0 {
		result = f.results[0]
		f.results = f.results[1:]
	}
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	return result, err
}

func TestRunSetupHooksRunsInOrderInDir(t *testing.T) {
	runner := &scriptedRunner{results: []CommandResult{{ExitCode: 0}, {ExitCode: 0}}}
	tools, err := newToolSet(Options{WorkspaceRoot: t.TempDir(), CommandRunner: runner})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{
		{Command: "npm install", TimeoutSeconds: 30},
		{Command: "go generate ./..."},
	}}
	warnings := tools.runSetupHooks(context.Background(), "/wt", plan)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("ran %d hooks, want 2", len(runner.requests))
	}
	if runner.requests[0].Command != "npm install" || runner.requests[1].Command != "go generate ./..." {
		t.Errorf("hook order = %q then %q", runner.requests[0].Command, runner.requests[1].Command)
	}
	for i, req := range runner.requests {
		if req.Dir != "/wt" {
			t.Errorf("hook %d dir = %q, want /wt", i, req.Dir)
		}
	}
	if runner.requests[0].Timeout != 30*time.Second {
		t.Errorf("hook 0 timeout = %s, want 30s", runner.requests[0].Timeout)
	}
	// A hook with no timeout_seconds gets the shell.run default.
	if runner.requests[1].Timeout != defaultShellTimeout {
		t.Errorf("hook 1 timeout = %s, want default %s", runner.requests[1].Timeout, defaultShellTimeout)
	}
}

func TestRunSetupHooksFailureBecomesWarningAndContinues(t *testing.T) {
	runner := &scriptedRunner{
		results: []CommandResult{
			{Stderr: "boom: module not found", ExitCode: 1},
			{Stdout: "generated", ExitCode: 0},
		},
	}
	tools, err := newToolSet(Options{WorkspaceRoot: t.TempDir(), CommandRunner: runner})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{
		{Command: "npm install"},
		{Command: "go generate ./..."},
	}}
	warnings := tools.runSetupHooks(context.Background(), "/wt", plan)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], `setup hook "npm install" failed`) || !strings.Contains(warnings[0], "boom: module not found") {
		t.Errorf("warning %q does not name the hook and its stderr", warnings[0])
	}
	if len(runner.requests) != 2 {
		t.Fatalf("ran %d hooks, want 2 — a failure must not abort the plan", len(runner.requests))
	}
}

func TestRunSetupHooksRunnerErrorBecomesWarning(t *testing.T) {
	runner := &scriptedRunner{errs: []error{errors.New("sandbox denied network")}}
	tools, err := newToolSet(Options{WorkspaceRoot: t.TempDir(), CommandRunner: runner})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "curl example.com"}}}
	warnings := tools.runSetupHooks(context.Background(), "/wt", plan)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "sandbox") {
		t.Fatalf("warnings = %v, want the runner error surfaced", warnings)
	}
}

func TestRunSetupHooksTruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("x", 1200)
	runner := &scriptedRunner{results: []CommandResult{{Stderr: long, ExitCode: 1}}}
	tools, err := newToolSet(Options{WorkspaceRoot: t.TempDir(), CommandRunner: runner})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	plan := worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{{Command: "fail"}}}
	warnings := tools.runSetupHooks(context.Background(), "/wt", plan)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	if len(warnings[0]) > 500+len(`setup hook "fail" failed: `) {
		t.Errorf("warning is %d chars, want the output tail capped at 500", len(warnings[0]))
	}
}

func TestWorkspaceWorktreeRunsHooksOnlyWhenFresh(t *testing.T) {
	// newWorktreeTestEnv builds the real git repo the worktree branches from;
	// its registry is discarded in favour of one with hooks configured.
	_, _, root := newWorktreeTestEnv(t)
	// The hook fails so the tool surfaces a warning: per the setup contract
	// only failures are reported, so a failing hook is the observable path.
	runner := &scriptedRunner{results: []CommandResult{{Stderr: "hook failed deliberately", ExitCode: 1}}}
	state2 := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg2 := registry.New()
	cfg := config.Default()
	cfg.Worktree.SetupHooks = []config.WorktreeSetupHook{{Command: "echo hooked"}}
	if err := RegisterAll(reg2, Options{WorkspaceRoot: root, CommandRunner: runner, SessionState: state2, Config: cfg}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	res, err := invokeTool(t, reg2, "workspace.worktree", `{"branch":"feat/x"}`)
	if err != nil {
		t.Fatalf("workspace.worktree: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("hook ran %d times, want 1 on a fresh worktree", len(runner.requests))
	}
	if runner.requests[0].Dir == "" || !strings.HasPrefix(runner.requests[0].Dir, root) {
		t.Errorf("hook dir = %q, want inside the worktree under %s", runner.requests[0].Dir, root)
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
	if len(runner.requests) != 1 {
		t.Fatalf("hook ran %d times after resume, want 1 (hooks only run when Fresh)", len(runner.requests))
	}
}
