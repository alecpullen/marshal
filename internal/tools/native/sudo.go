//go:build !windows

package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

const (
	defaultSudoTimeout = 300 * time.Second
	maxSudoTimeout     = 900 * time.Second
	maxSudoAttempts    = 3
)

type sudoArgs struct {
	Command        string `json:"command"`
	Reason         string `json:"reason"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// sudoRunner executes a privileged command through sudo, delivering the
// password on stdin. It deliberately does NOT go through the sandbox: the
// restricted backend's ulimit caps are meaningless for root and actively
// harmful (a file-size cap breaks apt install). It reuses runCmd for bounded
// output, process-group kill, and timeout.
type sudoRunner struct {
	// fresh adds -k, invalidating any cached sudo grant so the next command
	// prompts unconditionally. False is the opt-in unlock path, which relies
	// on sudo's own timestamp record.
	fresh bool
}

func (r sudoRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	// execRunner installs the deadline; this runner bypasses it, so it must
	// install its own. Without this the advertised timeout_seconds is a no-op
	// and a hung privileged command holds the session-wide single-flight lock
	// until the turn is cancelled.
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	argv := sudoArgv(req.Command, r.fresh)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = sudoEnv()
	result, err := runCmd(ctx, cmd, req)
	// runCmd reports a non-zero exit as an error. For a privileged command
	// that is not a tool failure: the exit code and stderr ARE the result,
	// and the caller must inspect them to tell a wrong password from a cold
	// sudo timestamp from a genuine command failure. Only a killed process
	// (timeout/cancel) or a startup failure stays an error.
	if err != nil && result.ExitCode != 0 && result.Meta.KilledReason == "" {
		err = nil
	}
	return result, err
}

// sudoArgv builds the privileged command line:
//
//	sudo [-k] -S -p <empty> -- <shell> -o pipefail -c 'exec </dev/null; <command>'
//
// -S reads the password from stdin; -p with an empty argument suppresses
// sudo's own prompt text; -- stops option parsing so a command starting with
// '-' cannot become a sudo flag. command is passed as a SINGLE argv element
// to the shell, so there is no argv-injection path. The shell is NOT a login
// shell: a login shell as root would source root's profiles, an unnecessary
// surprise.
//
// The payload is prefixed with `exec </dev/null;` because sudo and the
// command it execs share fd 0. sudo reads the password from that fd, but if
// it never consumes it (NOPASSWD, or a warm unlock grant) the password is
// still readable and the command can print it — `shell.sudo {"command":"cat"}`
// would echo the password into the transcript, the audit ledger and the
// model's context. Redirecting the payload's stdin to /dev/null closes that
// channel. It is consistent with the documented limitation that privileged
// commands see EOF on stdin.
func sudoArgv(command string, fresh bool) []string {
	argv := []string{"sudo"}
	if fresh {
		argv = append(argv, "-k")
	}
	argv = append(argv, "-S", "-p", "", "--")
	payload := "exec </dev/null; " + command
	if bash, err := exec.LookPath("bash"); err == nil {
		return append(argv, bash, "-o", "pipefail", "-c", payload)
	}
	return append(argv, "/bin/sh", "-c", payload)
}

// sudoEnv is the child environment: the sandbox allowlist (never Marshal's
// full environment, which could carry an LD_PRELOAD into a root process),
// with the locale pinned to C so sudo's own messages are not localized and
// the signature matching below is reliable.
func sudoEnv() []string {
	env := envutil.AllowList(os.Environ())
	env = envutil.Set(env, "LC_ALL", "C")
	env = envutil.Set(env, "LANG", "C")
	return env
}

// sudoWrongPassword reports whether sudo rejected the password we supplied.
//
// The signatures are deliberately narrow. runCmd merges sudo's stderr with the
// privileged command's own stderr (the child inherits fd 2), so a broad
// substring like "authentication failure" would misread a database client's
// or ssh's own output as a sudo rejection — re-prompting the user three times
// and discarding the real output. sudo prefixes its own diagnostics with
// "sudo:", so requiring that prefix keeps the discrimination reliable.
func sudoWrongPassword(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "sudo: sorry, try again") ||
		strings.Contains(s, "sudo: incorrect password") ||
		strings.Contains(s, "sudo: authentication failure")
}

// sudoRequiresTTY reports whether sudo refused because it has no terminal.
// This is the requiretty case, which we do NOT work around by allocating a
// PTY — that would defeat the no-terminal-capture property.
func sudoRequiresTTY(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "sudo: a terminal is required") ||
		strings.Contains(s, "sudo: no tty present") ||
		strings.Contains(s, "sudo: must have a tty")
}

// sudoNeedsPassword reports whether sudo wanted a password it did not get.
// On the unlock path this is the cold-timestamp signature: the command ran
// with an empty stdin, sudo read EOF, and reported that it needed a
// password. It is the warm/cold discriminator.
func sudoNeedsPassword(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "sudo: no password was provided") ||
		strings.Contains(s, "sudo: a password is required")
}

// sudoTool builds the shell.sudo native tool. It is Deferred so its full
// contract is disclosed on demand via tools.select, and RiskDestructive so
// the TUI never offers always-allow/session-allow for it.
func (t *toolSet) sudoTool() registry.Tool {
	tool := registry.Tool{
		Name: "shell.sudo",
		Description: "Run a single command as root via sudo, with the user's password read from a masked prompt. " +
			"Pass the BARE command: Marshal adds the leading sudo itself, and a command that invokes sudo/su/doas/pkexec/run0 is refused. " +
			"Requires system access. The password is delivered to sudo over stdin and is never stored, logged, or placed in argv. " +
			"stdin is consumed by the password, so interactive commands see EOF — pass non-interactive flags (apt-get -y, --non-interactive) instead of relying on a prompt. " +
			"Available only in the TUI; other clients cannot prompt for a password.",
		Schema:   json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The bare command to run as root, without a leading sudo."},"reason":{"type":"string","description":"Why this command needs root; shown in the password prompt."},"timeout_seconds":{"type":"integer","description":"Timeout in seconds (default 300, max 900)."}},"required":["command"],"additionalProperties":false}`),
		Risk:     registry.RiskDestructive,
		Deferred: true,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[sudoArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		return t.runSudo(ctx, call.ID, args)
	}
	return tool
}

func (t *toolSet) runSudo(ctx context.Context, callID string, args sudoArgs) (registry.ToolResult, error) {
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return registry.ToolResult{}, errors.New("command is required")
	}
	if !t.systemAccess() {
		return registry.ToolResult{}, errors.New("shell.sudo requires system access; enable it with /system or mode.request")
	}
	if t.sessionState == nil {
		return registry.ToolResult{}, errors.New("session state not available")
	}
	if !t.sessionState.SecretPromptCapable() {
		return registry.ToolResult{}, errors.New("this client cannot prompt for a password; shell.sudo is available only in the TUI")
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return registry.ToolResult{}, errors.New("sudo not found on PATH")
	}

	// Single-flight, session-wide: the pending-auth slot is single-valued, so
	// a second concurrent call would clobber the first. Held across prompt +
	// execute; released by defer on every path (deny, error, cancel, timeout).
	lockState := t.wsState()
	if lockState == nil {
		lockState = t.sessionState
	}
	if !lockState.TryLockSudo() {
		return registry.ToolResult{}, errors.New("another privileged command is awaiting authentication; retry when it completes")
	}
	defer lockState.UnlockSudo()

	// The admitted path evaluates the bare payload through the shared shell
	// analysis and admits each attempt itself. The legacy path keeps the
	// app-wide adapters only until every runner wires a CommandAuthorizer.
	if t.commandAuthorizer != nil {
		return t.runSudoAdmitted(ctx, callID, command, args)
	}
	if t.sudoPolicy == nil {
		return registry.ToolResult{}, errors.New("privileged execution is not configured")
	}
	if err := t.sudoPolicy(command); err != nil {
		return registry.ToolResult{}, err
	}
	// The catastrophic floor applies INSIDE the payload too. CheckPrivileged
	// deliberately does not reject floor commands (it only rejects a command
	// that invokes a privilege wrapper), so without this the tool would be a
	// way to run `rm -rf /etc` as root past the floor that shell.run obeys.
	// The two checks are independent and both must pass.
	if t.guardrail != nil {
		if err := t.guardrail(command, true); err != nil {
			return registry.ToolResult{}, err
		}
	}

	timeout := clampTimeout(args.TimeoutSeconds, defaultSudoTimeout, maxSudoTimeout)
	unlock := t.sessionState.SudoUnlockEnabled()
	runner := sudoRunner{fresh: !unlock}

	// Unlock path: try the command FIRST, with an EMPTY stdin and no prompt.
	// A warm grant means sudo never reads stdin and the command runs — one
	// process, no probe, no probe/execute race. A cold grant means sudo reads
	// EOF and reports that it needed a password; only then do we prompt.
	// Prompting before this attempt would make the unlock feature a no-op.
	if unlock {
		result, runErr := t.runSudoOnce(ctx, callID, runner, command, nil, timeout)
		if runErr != nil {
			return registry.ToolResult{}, runErr
		}
		switch {
		case result.ExitCode == 0:
			return t.sudoResult(command, result), nil
		case sudoRequiresTTY(result.Stderr):
			return registry.ToolResult{}, errSudoRequiresTTY
		case sudoNeedsPassword(result.Stderr):
			// Cold timestamp: fall through to the prompt loop below. -k stays
			// absent, so the grant this establishes is reusable.
		default:
			// A genuine command failure: the output IS the result.
			return t.sudoResult(command, result), nil
		}
	}

	var lastErr string
	for attempt := 1; attempt <= maxSudoAttempts; attempt++ {
		password, _, denied, err := t.requestSudoPassword(ctx, command, args.Reason, unlock, attempt, lastErr, false)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if denied {
			return registry.ToolResult{}, errors.New("denied by user")
		}

		stdin := append(password, '\n')
		result, runErr := t.runSudoOnce(ctx, callID, runner, command, stdin, timeout)
		zeroBytes(password)
		zeroBytes(stdin)
		if runErr != nil {
			return registry.ToolResult{}, runErr
		}

		switch {
		case result.ExitCode == 0:
			return t.sudoResult(command, result), nil
		case sudoWrongPassword(result.Stderr):
			lastErr = "incorrect password"
			continue
		case sudoRequiresTTY(result.Stderr):
			return registry.ToolResult{}, errSudoRequiresTTY
		default:
			// A genuine command failure: the output IS the result.
			return t.sudoResult(command, result), nil
		}
	}
	return registry.ToolResult{}, fmt.Errorf("authentication failed after %d attempts: %s", maxSudoAttempts, lastErr)
}

// errSudoRequiresTTY is the requiretty diagnostic. We do NOT work around it by
// allocating a PTY — that would defeat the no-terminal-capture property.
var errSudoRequiresTTY = errors.New("sudo requires a terminal (requiretty); set `Defaults !requiretty` for this user in sudoers")

// runSudoOnce runs one attempt with the given stdin. A nil stdin means the
// child sees EOF immediately, which is how the unlock path's warm probe is
// expressed: sudo either never reads it (warm grant) or reports that it
// needed a password (cold grant).
func (t *toolSet) runSudoOnce(ctx context.Context, callID string, runner sudoRunner, command string, stdin []byte, timeout time.Duration) (CommandResult, error) {
	var stdoutObs, stderrObs io.Writer
	if t.sessionState != nil {
		stdoutObs = &activeToolOutputWriter{state: t.sessionState, callID: callID}
		stderrObs = stdoutObs
	}
	return runner.Run(ctx, CommandRequest{
		Command:        command,
		Dir:            t.activeRoot(),
		Timeout:        timeout,
		MaxOutputBytes: t.maxOutputBytes,
		Stdin:          stdin,
		Stdout:         stdoutObs,
		Stderr:         stderrObs,
	})
}

// requestSudoPassword raises the masked prompt and blocks for the decision.
// The prompt is registered through the session's canonical registry, so a
// steering interruption of the owning operation can settle it once, with a
// distinct cause, instead of the handler clearing a slot another request may
// already own.
//
// A canceled prompt yields no password bytes: the decision the registry
// delivers on cancellation is an empty denial, and any buffer this function
// obtained is zeroed before it returns. The wait ends on a decision, on ctx
// cancellation, or on State.ResolvePendingForShutdown.
func (t *toolSet) requestSudoPassword(ctx context.Context, command, reason string, unlock bool, attempt int, prevErr string, commandApproval bool) (password []byte, promptKey session.PromptKey, denied bool, err error) {
	sa := &session.PendingSudoAuth{
		Command:         command,
		Reason:          reason,
		AllowUnlock:     unlock,
		Attempt:         attempt,
		Error:           prevErr,
		CommandApproval: commandApproval,
		ResponseChan:    make(chan session.SudoAuthDecision, 1),
	}
	key, regErr := t.sessionState.RegisterSudoAuth(ctx, sa)
	if regErr != nil {
		return nil, session.PromptKey{}, false, regErr
	}
	defer t.sessionState.ClearPrompt(key)

	select {
	case d := <-sa.ResponseChan:
		if !d.OK {
			// A canceled prompt delivers an empty denial that is channel-wise
			// indistinguishable from a user denial. The recorded cause is what
			// tells them apart: an interruption must not be reported as
			// "denied by user", which would count an intentional stop as a tool
			// failure and feed the failed-repeat ladder.
			if cause := t.sessionState.PromptCause(key); cause != nil {
				return nil, key, false, cause
			}
			return nil, key, true, nil
		}
		return d.Password, key, false, nil
	case <-ctx.Done():
		// Settle canonically, then accept whatever the settlement delivered: a
		// password the user had already submitted (whose claim won) is returned
		// so the privileged dispatch can proceed, while a cancellation delivers
		// an empty denial with no bytes.
		t.sessionState.CancelPrompt(key, session.ErrSendNow)
		if d, ok := <-sa.ResponseChan; ok && d.OK {
			return d.Password, key, false, nil
		}
		if cause := t.sessionState.PromptCause(key); cause != nil {
			return nil, key, false, cause
		}
		return nil, key, false, ctx.Err()
	}
}

// runSudoAdmitted is the privileged path under the shared admission contract.
//
// The bare payload goes through the SAME shell analysis ordinary commands use,
// so the catastrophic floor, the publishing rules and the required capabilities
// apply to root work exactly as they do to user work, and authentication is not
// a bypass of those decisions. A Deny refuses before any prompt is raised. A
// Confirm is approved BY the combined password prompt — one dialog that shows
// the exact command and the mandatory reasons and reads the password — and that
// prompt's canonical identity is what approves the proof. A warm unlock grant is
// a credential, never consent.
func (t *toolSet) runSudoAdmitted(ctx context.Context, callID, command string, args sudoArgs) (registry.ToolResult, error) {
	timeout := clampTimeout(args.TimeoutSeconds, defaultSudoTimeout, maxSudoTimeout)
	unlock := t.sessionState.SudoUnlockEnabled()
	runner := sudoRunner{fresh: !unlock}

	// Capture the attempt once: the exact command, the directory it will run in
	// and the workspace it belongs to. Every attempt below is admitted against
	// this identity, so nothing can retarget the command after approval.
	attempt := t.commandAttempt(ctx, "shell.sudo", callID, command)
	dir := attempt.Input.Dir
	if dir == "" {
		return registry.ToolResult{}, errors.New("privileged execution has no working directory")
	}

	proof, evaluation, err := t.evaluateSudoAttempt(ctx, attempt)
	if err != nil {
		return registry.ToolResult{}, err
	}
	if evaluation.Decision == policy.DecisionDeny {
		return registry.ToolResult{}, sudoDenialError(evaluation)
	}

	// Unlock path: a warm grant runs the command with no prompt, but only when
	// nothing needs command approval. When the analysis requires a Confirm the
	// prompt comes first, because a warm credential is not consent.
	if unlock && evaluation.Decision != policy.DecisionConfirm {
		result, runErr := t.runAdmittedSudoAttempt(ctx, callID, runner, dir, command, nil, timeout, attempt, proof)
		if runErr != nil {
			return registry.ToolResult{}, runErr
		}
		switch {
		case result.ExitCode == 0:
			return t.sudoResult(command, result), nil
		case sudoRequiresTTY(result.Stderr):
			return registry.ToolResult{}, errSudoRequiresTTY
		case sudoNeedsPassword(result.Stderr):
			// Cold timestamp: the prompt loop below collects the password.
		default:
			// A genuine command failure: the output IS the result.
			return t.sudoResult(command, result), nil
		}
	}

	var lastErr string
	for attemptNo := 1; attemptNo <= maxSudoAttempts; attemptNo++ {
		// Every retry is evaluated again against the live authority and the live
		// configuration. A proof is never renewed silently: a mode, config or
		// workspace change is reflected in this decision before the user is
		// asked, and a Confirm asks again instead of reusing earlier consent.
		proof, evaluation, evalErr := t.evaluateSudoAttempt(ctx, attempt)
		if evalErr != nil {
			return registry.ToolResult{}, evalErr
		}
		if evaluation.Decision == policy.DecisionDeny {
			return registry.ToolResult{}, sudoDenialError(evaluation)
		}
		needsConsent := evaluation.Decision == policy.DecisionConfirm

		password, promptKey, denied, promptErr := t.requestSudoPassword(ctx, command, sudoPromptReason(args.Reason, evaluation), unlock, attemptNo, lastErr, needsConsent)
		if promptErr != nil {
			return registry.ToolResult{}, promptErr
		}
		if denied {
			return registry.ToolResult{}, errors.New("denied by user")
		}
		if needsConsent {
			// The combined prompt IS the approval: bind its canonical identity
			// to this exact proof before admission.
			approved, approveErr := t.commandAuthorizer.ApproveExact(proof, promptKey.ID)
			if approveErr != nil {
				zeroBytes(password)
				return registry.ToolResult{}, approveErr
			}
			proof = approved
		}

		stdin := append(password, '\n')
		result, runErr := t.runAdmittedSudoAttempt(ctx, callID, runner, dir, command, stdin, timeout, attempt, proof)
		zeroBytes(password)
		zeroBytes(stdin)
		if runErr != nil {
			return registry.ToolResult{}, runErr
		}

		switch {
		case result.ExitCode == 0:
			return t.sudoResult(command, result), nil
		case sudoWrongPassword(result.Stderr):
			lastErr = "incorrect password"
			continue
		case sudoRequiresTTY(result.Stderr):
			return registry.ToolResult{}, errSudoRequiresTTY
		default:
			// A genuine command failure: the output IS the result.
			return t.sudoResult(command, result), nil
		}
	}
	return registry.ToolResult{}, fmt.Errorf("authentication failed after %d attempts: %s", maxSudoAttempts, lastErr)
}

// evaluateSudoAttempt evaluates one captured privileged attempt against the
// live policy and authority.
func (t *toolSet) evaluateSudoAttempt(ctx context.Context, attempt policy.CommandAttempt) (*policy.Authorization, policy.Evaluation, error) {
	call := policy.Call{InvocationID: attempt.InvocationID, ToolName: attempt.ToolName, Shell: &attempt.Input}
	return t.commandAuthorizer.Evaluate(ctx, call)
}

// runAdmittedSudoAttempt revalidates and admits exactly this attempt
// immediately before the process starts, and releases the gate before it runs:
// the decision is already bound to the exact command, and holding the owner's
// admission gate for the whole privileged command would block every other
// admission in the conversation.
func (t *toolSet) runAdmittedSudoAttempt(ctx context.Context, callID string, runner sudoRunner, dir, command string, stdin []byte, timeout time.Duration, attempt policy.CommandAttempt, proof *policy.Authorization) (CommandResult, error) {
	admitted, err := t.commandAuthorizer.BeginCommand(policy.WithAuthorization(ctx, proof), attempt)
	if err != nil {
		return CommandResult{}, err
	}
	if commitErr := admitted.Commit(); commitErr != nil {
		admitted.Release()
		return CommandResult{}, commitErr
	}
	admitted.Release()

	var stdoutObs, stderrObs io.Writer
	if t.sessionState != nil {
		stdoutObs = &activeToolOutputWriter{state: t.sessionState, callID: callID}
		stderrObs = stdoutObs
	}
	return runner.Run(ctx, CommandRequest{
		Command:        command,
		Dir:            dir,
		Timeout:        timeout,
		MaxOutputBytes: t.maxOutputBytes,
		Stdin:          stdin,
		Stdout:         stdoutObs,
		Stderr:         stderrObs,
	})
}

// sudoDenialError renders a policy refusal from the shared analysis.
func sudoDenialError(evaluation policy.Evaluation) error {
	if reason := strings.Join(sudoReasons(evaluation), "; "); reason != "" {
		return fmt.Errorf("privileged command denied by policy: %s", reason)
	}
	return errors.New("privileged command denied by policy")
}

// sudoPromptReason is what the combined prompt discloses: why the caller asked
// for root, plus every mandatory policy reason that made the exact command need
// approval.
func sudoPromptReason(reason string, evaluation policy.Evaluation) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if reasons := sudoReasons(evaluation); len(reasons) > 0 {
		parts = append(parts, "policy requires approval: "+strings.Join(reasons, "; "))
	}
	return strings.Join(parts, "\n")
}

func sudoReasons(evaluation policy.Evaluation) []string {
	reasons := make([]string, 0, len(evaluation.Reasons))
	for _, r := range evaluation.Reasons {
		message := strings.TrimSpace(r.Message)
		if message == "" {
			message = string(r.Code)
		}
		if message != "" {
			reasons = append(reasons, message)
		}
	}
	return reasons
}

func (t *toolSet) sudoResult(command string, result CommandResult) registry.ToolResult {
	exitCode := result.ExitCode
	content := limitOutput(formatCommandOutput(result.Stdout, result.Stderr), t.maxOutputBytes)
	summary := fmt.Sprintf("sudo command %q exited with code %d", command, result.ExitCode)
	if result.Meta.Enabled && result.Meta.KilledReason != "" {
		summary = fmt.Sprintf("sudo command %q killed: %s", command, result.Meta.KilledReason)
	}
	return registry.ToolResult{
		Summary:         summary,
		Content:         content,
		CommandExitCode: &exitCode,
		Sandbox:         result.Meta,
	}
}

// zeroBytes overwrites a password buffer. Best effort: the TUI's own string
// copy of the typed text is immutable and cannot be zeroed (spec §3.4).
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
