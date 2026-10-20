package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/progress"
	"marshal/internal/tools/policy"
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
	Command        string           `json:"command"`
	TimeoutSeconds int              `json:"timeout_seconds"`
	Background     bool             `json:"background"`
	Progress       *progress.Config `json:"progress,omitempty"`
}

type testRunArgs struct {
	Command  string           `json:"command"`
	Progress *progress.Config `json:"progress,omitempty"`
}

func (t *toolSet) shellRunTool() registry.Tool {
	tool := registry.Tool{
		Name: "shell.run",
		Description: "Run a shell command with the workspace root as the current working directory. " +
			"Prefer native tools such as repo.search, symbols, and file.read over broad filesystem commands " +
			"(e.g. find /, ls -R /, grep -r /). Commands are subject to conservative guardrails. " +
			"Never use shell.run to create or modify files (no redirection, heredocs, or tee) — use file.write or file.edit instead. " +
			"Use git.* for repository state and the workspace tools for worktree isolation and merging. " +
			"The command is not killed by elapsed time: one still running when the wait ends continues in the background with a job_id. " +
			"agent.await reports its status, exit code, stop cause and recent output; agent.kill stops one named job.",
		Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"progress":{"type":"object","properties":{"profile":{"type":"string","enum":["generic","tests","download","transfer","custom"]},"source":{"type":"string","enum":["none","go-test-json","events-v1"]},"title":{"type":"string","maxLength":80}},"additionalProperties":false},"timeout_seconds":{"type":"integer","description":"maximum seconds this call waits before the still-running command continues in the background; it never limits the command's lifetime. Omit or pass 0 for the foreground wait."},"background":{"type":"boolean","description":"start the command and return its job_id immediately"}},"required":["command"],"additionalProperties":false}`),
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
			return t.runManagedCommandWithProgress(ctx, "shell.run", call.ID, args.Command, wait, true, args.Progress)
		}
		// A foreground call waited for the command, so whatever it wrote is on
		// disk by now and open documents must be re-synced — including when
		// the settled outcome is a tool failure.
		defer t.rescanLSP()
		return t.runManagedCommandWithProgress(ctx, "shell.run", call.ID, args.Command, wait, false, args.Progress)
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
		Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"progress":{"type":"object","properties":{"profile":{"type":"string","enum":["generic","tests","download","transfer","custom"]},"source":{"type":"string","enum":["none","go-test-json","events-v1"]},"title":{"type":"string","maxLength":80}},"additionalProperties":false}},"additionalProperties":false}`),
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
			command = t.configuredTestCommand()
		}
		res, err := t.runManagedCommandWithProgress(ctx, "test.run", call.ID, command, t.foregroundWait(), false, args.Progress)
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
// execution service and renders the caller's attachment to it, without opting
// into progress observation.
func (t *toolSet) runManagedCommand(ctx context.Context, toolName, callID, command string, wait time.Duration, background bool) (registry.ToolResult, error) {
	return t.runManagedCommandWithProgress(ctx, toolName, callID, command, wait, background, nil)
}

// configuredTestCommand returns the test command to run when the caller passes
// no override. It is read from the owner's CURRENT policy snapshot, so a config
// reload reaches an already-built toolset — including an old child toolset that
// captured its creation config — instead of silently running the superseded
// command. A toolset with no session (tests, embedded callers) falls back to the
// value captured at construction.
func (t *toolSet) configuredTestCommand() string {
	if state := t.sessionState; state != nil {
		cfg, _, _ := state.PolicySnapshot()
		if command := strings.TrimSpace(cfg.Commands.Test); command != "" {
			return command
		}
	}
	return t.testCommand
}

// commandAttempt captures the exact execution facts one managed command is
// admitted against: the command bytes, the working directory it will run in,
// the workspace it belongs to, the home and additional read roots it may reach,
// and the environment the analysis reads. Everything is resolved here, before
// the authorizer evaluates the command, so a workspace or config change after
// admission cannot redirect work that was already approved.
func (t *toolSet) commandAttempt(ctx context.Context, toolName, invocationID, command string) policy.CommandAttempt {
	// A proof attached by dispatch already captured the shell facts it
	// evaluated: admit exactly those, under that proof's logical identity,
	// rather than re-deriving a second description of the same command. The
	// captured command bytes are the ones that were approved.
	if input, ok := policy.ShellInputFromContext(ctx); ok {
		attempt := policy.CommandAttempt{
			InvocationID: policy.InvocationID(ctx),
			ToolName:     toolName,
			Input:        input,
		}
		if attempt.InvocationID == "" {
			attempt.InvocationID = invocationID
		}
		return attempt
	}
	dir := t.commandRoot()
	if dir == "" {
		// A managed command always runs somewhere: the construction root is
		// absolute and non-empty, so this only guards a future caller that
		// bypasses it. An empty cwd would make every relative target resolve
		// against the process's own directory.
		dir = t.root
	}
	workspace := t.activeRoot()
	if workspace == "" {
		workspace = t.root
	}
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = h
	}
	return policy.CommandAttempt{
		InvocationID: invocationID,
		ToolName:     toolName,
		Input: policy.ShellInput{
			Command:       command,
			Dir:           dir,
			WorkspaceRoot: workspace,
			HomeDir:       home,
			ReadRoots:     append([]string(nil), t.additionalRoots...),
			Env:           knownEnvironment(),
		},
	}
}

// knownEnvironment is the environment map the shell analysis expands against.
// Only the variables a command can be classified by travel with the attempt: a
// variable the analysis cannot see must be treated as unknown rather than
// guessed, and copying the whole process environment would make the admission
// fingerprint depend on unrelated ambient state.
func knownEnvironment() map[string]string {
	env := make(map[string]string, 8)
	for _, name := range []string{"HOME", "PATH", "PWD", "SHELL", "TMPDIR", "USER"} {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	return env
}

// runManagedCommandWithProgress launches one ordinary command through the
// conversation's execution service and renders the caller's attachment to it.
//
// There is exactly one launch path for shell.run and test.run. A foreground
// call and an explicit background call differ only in the attachment they ask
// for, so a wait that expires hands the already-approved, already-running
// process to the background: no second launch, no second approval, no restart.
//
// A non-nil cfg opts this one execution into progress observation. It is
// normalized and rejected here, BEFORE the guardrail and before admission, so
// an invalid opt-in never prompts for approval and never reserves a slot; the
// normalized copy is what the execution captures. A nil cfg stays nil: no
// observer is allocated and the ordinary tool result is left unfiltered.
func (t *toolSet) runManagedCommandWithProgress(ctx context.Context, toolName, callID, command string, wait time.Duration, background bool, cfg *progress.Config) (registry.ToolResult, error) {
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
	// The optional progress profile is validated before the guardrail: a
	// rejected opt-in is a malformed call, not a command to approve, so it must
	// not reach the approval boundary or reserve an execution nothing can
	// observe. The normalized copy is what the execution keeps for its whole
	// life.
	var progressCfg *progress.Config
	if cfg != nil {
		normalized, err := progress.NormalizeConfig(*cfg)
		if err != nil {
			return registry.ToolResult{}, err
		}
		progressCfg = &normalized
	}
	// Guardrails are validated before admission, so a denied command never
	// reaches the service.
	if t.guardrail != nil {
		if err := t.guardrail(command, t.systemAccess()); err != nil {
			return registry.ToolResult{}, err
		}
	}
	// A command whose operation was already interrupted must not launch: the
	// interruption has told the model not to expect its result, and admitting it
	// would start work nothing can report on.
	if err := session.CheckSteering(ctx); err != nil {
		return registry.ToolResult{}, err
	}

	// Everything the execution needs is captured here, before it is admitted:
	// the service resolves its own defaults later, so a config reload or a
	// workspace change must not redirect a command that has already started.
	// The active-call writer is wired in unconditionally and the service stops
	// forwarding to it the moment the execution detaches, so display ownership
	// follows attachment without this path relaunching anything.
	// The admission seam. The attempt is captured ONCE, before the authorizer
	// evaluates it: the exact command bytes, the directory it will run in and
	// the workspace it belongs to. Launch hands that closure to the authorizer
	// at accepted admission, so a workspace or config change between approval
	// and insertion cannot redirect the command that was approved. Without a
	// wired authorizer the launch keeps the legacy guardrail-only path.
	var admit func(context.Context) (*policy.Admission, error)
	if t.commandAdmission != nil {
		attempt := t.commandAttempt(ctx, toolName, callID, command)
		admit = func(admitCtx context.Context) (*policy.Admission, error) {
			return t.commandAdmission(admitCtx, attempt)
		}
	}

	operation, _ := session.OperationFromContext(ctx)
	info, err := t.jobManager.Launch(ctx, LaunchSpec{
		Command:        command,
		Dir:            t.commandRoot(),
		Runner:         t.runner,
		MaxOutputBytes: t.maxOutputBytes,
		Stdout:         t.activeCallWriter(callID),
		Stderr:         t.activeCallWriter(callID),
		Origin:         t.executionOrigin(ctx),
		Operation:      operation,
		Detached:       background,
		Wait:           wait,
		Progress:       progressCfg,
		Admit:          admit,
	})
	if err != nil {
		return registry.ToolResult{}, err
	}
	if background {
		return t.backgroundCommandResult(info), nil
	}

	// The manager indexes executions by the handle in this snapshot. The wait is
	// tied to the turn's own context, not the operation's: a steering
	// interruption cancels the operation context, and a wait built on it would
	// abandon a command the service is still stopping rather than reporting its
	// real outcome.
	waitCtx := session.SteeringTurnContext(ctx)
	res, err := t.jobManager.WaitForeground(waitCtx, info.ID, wait)
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

	// A terminal result keeps the command's ordinary output. An execution that
	// opted into progress observation reports the observer's filtered diagnostics
	// instead — the same text with valid control records removed, so a display
	// protocol line never reaches the model as command output. The final
	// measurement rides in the existing execution notice, and a capture that hit
	// its cap still discloses the truncation.
	content := limitOutput(res.Output, t.maxOutputBytes)
	if cfg := info.ProgressConfig; cfg != nil && cfg.Source != "none" {
		content = limitOutput(info.DisplayTail, t.maxOutputBytes)
		if info.OutputTruncated && !strings.Contains(content, truncationMarker) {
			content += truncationMarker
		}
	}
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
