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
func sudoWrongPassword(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "sorry, try again") ||
		strings.Contains(s, "incorrect password") ||
		strings.Contains(s, "authentication failure")
}

// sudoRequiresTTY reports whether sudo refused because it has no terminal.
// This is the requiretty case, which we do NOT work around by allocating a
// PTY — that would defeat the no-terminal-capture property.
func sudoRequiresTTY(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "a terminal is required") ||
		strings.Contains(s, "no tty present") ||
		strings.Contains(s, "must have a tty")
}

// sudoNeedsPassword reports whether sudo wanted a password it did not get.
// On the unlock path this is the cold-timestamp signature: the command ran
// with an empty stdin, sudo read EOF, and reported that it needed a
// password. It is the warm/cold discriminator.
func sudoNeedsPassword(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no password was provided") ||
		strings.Contains(s, "a password is required")
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
		password, denied, err := t.requestSudoPassword(ctx, command, args.Reason, unlock, attempt, lastErr)
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
// Mirrors requestApproval's protocol: set the pending slot, wait on the
// channel, clear the slot. The wait ends on a decision, on ctx cancellation,
// or on State.ResolvePendingForShutdown.
func (t *toolSet) requestSudoPassword(ctx context.Context, command, reason string, unlock bool, attempt int, prevErr string) (password []byte, denied bool, err error) {
	sa := &session.PendingSudoAuth{
		Command:      command,
		Reason:       reason,
		AllowUnlock:  unlock,
		Attempt:      attempt,
		Error:        prevErr,
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	}
	t.sessionState.SetPendingSudoAuth(sa)
	defer t.sessionState.SetPendingSudoAuth(nil)

	select {
	case d := <-sa.ResponseChan:
		if !d.OK {
			return nil, true, nil
		}
		return d.Password, false, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
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
