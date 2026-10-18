package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

const (
	// defaultShellTimeout and maxShellTimeout still bound the tools that keep an
	// execution deadline: workspace setup hooks and shell.sudo call clampTimeout
	// directly. Ordinary shell.run and test.run no longer do — their
	// timeout_seconds is a wait budget, and nothing in the managed execution
	// path ends a process because wall-clock time passed.
	defaultShellTimeout = 120 * time.Second
	maxShellTimeout     = 300 * time.Second
)

// defaultForegroundWait is how long an ordinary shell/test call waits before
// its command continues in the background when the configured
// tools.shell.foreground_wait_seconds is not positive. Config validation
// rejects a non-positive value, so this only covers a toolset built with a
// zero-valued config (tests, embedded callers), where a zero wait would turn
// every command into an immediate continuation.
const defaultForegroundWait = 10 * time.Second

// maxWaitSeconds is the longest timeout_seconds argument that can be held in a
// time.Duration. A larger value is rejected before anything is launched:
// multiplying it out wraps around and would silently hand the caller a much
// shorter wait than it asked for.
const maxWaitSeconds = int64(math.MaxInt64) / int64(time.Second)

type shellRunArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Background     bool   `json:"background"`
}

type testRunArgs struct {
	Command string `json:"command"`
}

func (t *toolSet) shellRunTool() registry.Tool {
	tool := registry.Tool{
		Name: "shell.run",
		Description: "Run a shell command with the workspace root as the current working directory. " +
			"Prefer native tools such as repo.search, symbols, and file.read over broad filesystem commands " +
			"(e.g. find /, ls -R /, grep -r /). Commands are subject to conservative guardrails. " +
			"Never use shell.run to create or modify files (no redirection, heredocs, or tee) — use file.write or file.edit instead. " +
			"Use git.* for repository state and the workspace tools for worktree isolation and merging. " +
			"The command is not killed by elapsed time: timeout_seconds bounds how long this call waits, and a command still running when the wait ends continues in the background with a job_id for agent.await. " +
			"Pass background: true to return the job_id immediately without waiting. " +
			"agent.await (optionally with peek: true) reports the job's status, its actual exit code, its stop cause and recent output; agent.kill stops one named job explicitly.",
		Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer","description":"maximum seconds this call waits before the still-running command continues in the background; it never limits the command's lifetime and is not capped. Omit or pass 0 to use the configured foreground wait."},"background":{"type":"boolean","description":"start the command and return its job_id immediately without waiting"}},"required":["command"],"additionalProperties":false}`),
		Risk:   registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[shellRunArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		// The wait is resolved before anything is launched: a rejected value
		// must not leave a process behind.
		wait, err := resolveShellWait(args.TimeoutSeconds, t.foregroundWait())
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Background {
			// An explicit background launch does not rescan the language
			// server: the command has not run to completion yet, and this call
			// is not waiting for it.
			return t.runManagedCommand(ctx, call.ID, args.Command, wait, true)
		}
		// A foreground call waited for the command, so whatever it wrote is on
		// disk by now and open documents must be re-synced — including when
		// the settled outcome is a tool failure.
		defer t.rescanLSP()
		return t.runManagedCommand(ctx, call.ID, args.Command, wait, false)
	}
	return tool
}

func (t *toolSet) testRunTool() registry.Tool {
	tool := registry.Tool{
		Name: "test.run",
		Description: "Run the configured test command in the workspace. " +
			"Pass a `command` parameter to override the configured test command; " +
			"the override is subject to the same approval policy. " +
			"The command runs through the same managed execution as shell.run: this call waits for the configured foreground period, " +
			"and a test command still running when the wait ends continues in the background with a job_id for agent.await instead of being killed.",
		Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"additionalProperties":false}`),
		Risk:   registry.RiskCommand,
		// Internal: the general agent runs tests through shell.run; only the swarm tester (TesterView) gets this constrained tool.
		Internal: true,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[testRunArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		command := args.Command
		if command == "" {
			command = t.testCommand
		}
		res, err := t.runManagedCommand(ctx, call.ID, command, t.foregroundWait(), false)
		t.rescanLSP()
		return res, err
	}
	return tool
}

// gitHygieneNote returns a nudge line for `git merge` and `git stash`
// invocations. "" for everything else.
func gitHygieneNote(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	switch {
	case strings.HasPrefix(trimmed, "git merge ") || trimmed == "git merge":
		return "note: for session work prefer workspace.finish over hand-merging; pop or drop any stash you create before finishing."
	case strings.HasPrefix(trimmed, "git stash"):
		return "note: remember to pop or drop this stash when done."
	}
	return ""
}

// resolveShellWait turns an ordinary shell-capable tool's timeout_seconds
// argument into a foreground wait budget.
//
// The argument is a waiting budget, never an execution deadline: a negative
// value is rejected before anything is launched, omitted or zero uses the
// configured positive default, and a positive value is honoured exactly as
// asked — the old execution cap is gone, so a request for 900 seconds waits 900
// seconds. A value that cannot be represented as a time.Duration is rejected as
// well, rather than wrapped into a shorter wait.
func resolveShellWait(seconds int, defaultWait time.Duration) (time.Duration, error) {
	if seconds < 0 {
		return 0, fmt.Errorf("timeout_seconds must be >= 0: it bounds how long this call waits, not how long the command may run")
	}
	if seconds == 0 {
		return defaultWait, nil
	}
	if int64(seconds) > maxWaitSeconds {
		return 0, fmt.Errorf("timeout_seconds %d is too large: the longest wait is %d seconds", seconds, maxWaitSeconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

// runManagedCommand launches one ordinary command through the conversation's
// execution service and renders the caller's attachment to it.
//
// There is exactly one launch path for shell.run and test.run. A foreground
// call and an explicit background call differ only in the attachment they ask
// for, so a wait that expires hands the already-approved, already-running
// process to the background: no second launch, no second approval, no restart.
func (t *toolSet) runManagedCommand(ctx context.Context, callID, command string, wait time.Duration, background bool) (registry.ToolResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return registry.ToolResult{}, fmt.Errorf("command is required")
	}
	// An ordinary command has exactly one owner: the conversation's command
	// service, injected at construction. A registry built without one (a
	// filtered read-only scope, a plan-author child, a mis-wired test fixture)
	// must fail here, before the guardrail and before any launch, rather than
	// creating a private supervisor whose commands nothing can stop, count or
	// settle.
	if t.jobManager == nil {
		return registry.ToolResult{}, fmt.Errorf("shell execution is unavailable: this registry has no conversation command service")
	}
	// Guardrails are validated before admission, so a denied command never
	// reaches the service.
	if t.guardrail != nil {
		if err := t.guardrail(command, t.systemAccess()); err != nil {
			return registry.ToolResult{}, err
		}
	}

	// Everything the execution needs is captured here, before it is admitted:
	// the service resolves its own defaults later, so a config reload or a
	// workspace change must not redirect a command that has already started.
	// The active-call writer is wired in unconditionally and the service stops
	// forwarding to it the moment the execution detaches, so display ownership
	// follows attachment without this path relaunching anything.
	info, err := t.jobManager.Launch(ctx, LaunchSpec{
		Command:        command,
		Dir:            t.commandRoot(),
		Runner:         t.runner,
		MaxOutputBytes: t.maxOutputBytes,
		Stdout:         t.activeCallWriter(callID),
		Stderr:         t.activeCallWriter(callID),
		Origin:         t.executionOrigin(ctx),
		Detached:       background,
		Wait:           wait,
	})
	if err != nil {
		return registry.ToolResult{}, err
	}
	if background {
		return t.backgroundCommandResult(info), nil
	}

	// The manager indexes executions by the handle in this snapshot.
	res, err := t.jobManager.WaitForeground(ctx, info.ID, wait)
	if err != nil {
		return registry.ToolResult{}, err
	}
	if !res.Terminal {
		return t.continuationCommandResult(command, res), nil
	}
	return t.settledCommandResult(command, res)
}

// activeCallWriter returns the writer that forwards a running command's output
// to the active tool-call row, or nil when no session is wired (embedded
// callers and tests).
func (t *toolSet) activeCallWriter(callID string) io.Writer {
	if t.sessionState == nil {
		return nil
	}
	return &activeToolOutputWriter{state: t.sessionState, callID: callID}
}

// executionOrigin reads the origin the runner stamped onto the handler's
// context, so a managed command names the conversation, actor, turn and call it
// belongs to. A handler called outside a runner turn (a direct call, a test)
// has none, and the service composes an identity from what it has.
func (t *toolSet) executionOrigin(ctx context.Context) CommandOrigin {
	origin, _ := session.ExecutionOriginFromContext(ctx)
	return CommandOrigin{
		ConversationID: origin.ConversationID,
		Actor:          origin.Actor,
		TurnID:         origin.TurnID,
		StepID:         origin.StepID,
		ToolCallID:     origin.ToolCallID,
		Generation:     origin.Generation,
	}
}

// rescanLSP announces that a command may have changed files on disk, so open
// documents are re-synced.
func (t *toolSet) rescanLSP() {
	if t.lspSync != nil {
		t.lspSync.Rescan()
	}
}

// backgroundCommandResult renders an explicit background launch. The caller
// asked not to wait, so the result is the handle of the running command; the
// structured identity, status and effective wait travel in the notice.
func (t *toolSet) backgroundCommandResult(info JobInfo) registry.ToolResult {
	return registry.ToolResult{
		Summary: fmt.Sprintf("started background job %s", info.ID),
		Content: fmt.Sprintf("job_id: %s", info.ID),
		Notice:  registry.NewCommandExecutionNotice(info.Execution()),
	}
}

// continuationCommandResult renders a wait that ended while the command was
// still running. It is a normal result — not an error, not a failed command —
// because the same process continues in the background under the service's
// ownership; the handle is how the caller keeps track of it.
func (t *toolSet) continuationCommandResult(command string, res ForegroundResult) registry.ToolResult {
	info := res.Info
	var b strings.Builder
	fmt.Fprintf(&b, "Command is still running in the background.\njob_id: %s\nstatus: %s", info.ID, info.Status)
	if info.Wait > 0 {
		fmt.Fprintf(&b, "\nwait budget: %s", info.Wait)
	}
	if res.WaitEnd == WaitEndCanceled {
		b.WriteString("\nthe wait ended early; the command was not stopped")
	}
	if output := strings.TrimRight(res.Output, "\n"); output != "" {
		b.WriteString("\n\nrecent output:\n" + output)
	}
	fmt.Fprintf(&b, "\n\nWait with agent.await using job_id %q; inspect with peek: true.", info.ID)

	return registry.ToolResult{
		Summary: fmt.Sprintf("command %q is still running in the background (job %s)", command, info.ID),
		Content: b.String(),
		Notice:  registry.NewCommandExecutionNotice(info.Execution()),
	}
}

// settledCommandResult renders a command that finished while the caller was
// still attached: the actual exit code and captured output are the normal tool
// result, because that output IS the outcome.
//
// A command the runner had to terminate — an explicit stop, a backend-enforced
// resource limit, shutdown — is also a tool failure, and the observed exit and
// output travel with the error rather than being discarded. A natural non-zero
// exit is deliberately NOT an error: CommandExitCode already tells the agent the
// command failed, and the old path behaved the same way.
func (t *toolSet) settledCommandResult(command string, res ForegroundResult) (registry.ToolResult, error) {
	info := res.Info
	if info.Outcome == OutcomeStartupFailure {
		// The runner never started a process, so there is no exit code and
		// nothing was captured: the startup error is the whole result.
		if res.RunError != nil {
			return registry.ToolResult{}, res.RunError
		}
		return registry.ToolResult{}, fmt.Errorf("command %q did not start", command)
	}

	summary := fmt.Sprintf("command %q finished without a reported exit code", command)
	var runErr error
	switch {
	case info.Outcome == OutcomeStopped:
		cause := strings.TrimSpace(info.StopCause)
		if cause == "" {
			cause = strings.TrimSpace(res.Result.Meta.KilledReason)
		}
		if cause == "" {
			cause = "stopped"
		}
		summary = fmt.Sprintf("command %q stopped: %s", command, cause)
		runErr = terminatedError(res, fmt.Errorf("command %q stopped: %s", command, cause))
	case info.Outcome == OutcomeShutdown || res.Result.Meta.KilledReason != "":
		reason := strings.TrimSpace(res.Result.Meta.KilledReason)
		if reason == "" {
			reason = "cancelled"
		}
		summary = fmt.Sprintf("command %q killed: %s", command, reason)
		runErr = terminatedError(res, fmt.Errorf("command %q killed: %s", command, reason))
	case info.ExitCode != nil && *info.ExitCode != 0:
		// A natural non-zero exit carries the command's failure in
		// CommandExitCode, so it is not a tool error.
		summary = fmt.Sprintf("command %q exited with code %d", command, *info.ExitCode)
	case res.RunError != nil:
		// The runner reported a failure but no exit to go with it: there is
		// nothing in the result to explain the command's end, so the error is
		// the whole story rather than a silently successful row.
		summary = fmt.Sprintf("command %q failed: %v", command, res.RunError)
		runErr = res.RunError
	case info.ExitCode != nil:
		summary = fmt.Sprintf("command %q exited with code %d", command, *info.ExitCode)
	}

	content := limitOutput(res.Output, t.maxOutputBytes)
	if note := gitHygieneNote(command); note != "" {
		content += "\n\n" + note
	}
	return registry.ToolResult{
		Summary:         summary,
		Content:         content,
		CommandExitCode: info.ExitCode,
		Sandbox:         res.Result.Meta,
		Notice:          registry.NewCommandExecutionNotice(info.Execution()),
	}, runErr
}

// terminatedError returns the runner's own error for a terminated command, or
// fallback when it reported none: a command terminated before it finished must
// still be a tool failure, even if the runner could report no reason.
func terminatedError(res ForegroundResult, fallback error) error {
	if res.RunError != nil {
		return res.RunError
	}
	return fallback
}

// activeToolOutputWriter forwards streamed bytes into the active tool-call
// transcript row. It is safe for concurrent stdout/stderr writes.
type activeToolOutputWriter struct {
	state  *session.State
	callID string
}

func (w *activeToolOutputWriter) Write(p []byte) (int, error) {
	if w.state == nil || len(p) == 0 {
		return len(p), nil
	}
	w.state.AppendActiveToolCallOutput(w.callID, string(p))
	return len(p), nil
}

func clampTimeout(seconds int, defaultTimeout time.Duration, maxTimeout time.Duration) time.Duration {
	if seconds <= 0 {
		return defaultTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

// formatCommandOutput labels the streams only when both have content, and
// trims blank lines at the edges, so a quiet command does not show an empty
// "stderr:" block to the user or the model.
func formatCommandOutput(stdout string, stderr string) string {
	stdout = strings.Trim(stdout, "\n")
	stderr = strings.Trim(stderr, "\n")
	switch {
	case stdout != "" && stderr != "":
		return "stdout:\n" + stdout + "\n\nstderr:\n" + stderr
	case stderr != "":
		return "stderr:\n" + stderr
	}
	return stdout
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
