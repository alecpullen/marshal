package native

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/google/shlex"

	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	// Honour the caller's timeout on both execution paths; previously only
	// the sandbox runners applied req.Timeout, making shell.run's
	// timeout_seconds a silent no-op on this default runner.
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	// Attempt argv path for shell-free, non-destructive commands.
	// This avoids shell-wrapping overhead and shell-injection surface.
	argv, splitErr := shlex.Split(req.Command)
	cls, clsErr := policy.ClassifyCommand(req.Command)

	if splitErr == nil && clsErr == nil && len(argv) > 0 &&
		cls.Risk != registry.RiskDestructive && isShellFree(req.Command) {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = req.Dir
		return runCmd(ctx, cmd, req)
	}

	// Fall through to shell path: commands with shell metacharacters,
	// destructive commands, or commands that failed to parse.
	cmd := shellCommand(req.Command)
	cmd.Dir = req.Dir
	return runCmd(ctx, cmd, req)
}

// shellCommand builds the shell-wrapped command for the shell path. It
// prefers bash with pipefail so a command failing early in a pipeline
// (e.g. `go vet ./... 2>&1 | head -50`) surfaces a non-zero exit code
// instead of the last stage's success — otherwise piped build/test
// failures are invisible to the agent. Falls back to /bin/sh when bash
// is unavailable.
func shellCommand(command string) *exec.Cmd {
	if bash, err := exec.LookPath("bash"); err == nil {
		return exec.Command(bash, "-o", "pipefail", "-lc", command)
	}
	return exec.Command("/bin/sh", "-lc", command)
}

// runCmd wires stdout/stderr observers, starts, waits, and returns the result.
// It runs the child in its own process group so that cancellation kills the
// entire tree (including grandchildren), not just the direct child
// (TOOLS-MOD-F8).
func runCmd(ctx context.Context, cmd *exec.Cmd, req CommandRequest) (CommandResult, error) {
	stdout := NewBoundedOutput(OutputLimit(req.MaxOutputBytes), req.Stdout)
	stderr := NewBoundedOutput(OutputLimit(req.MaxOutputBytes), req.Stderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}
	if req.OnStart != nil {
		req.OnStart(cmd.Process.Pid)
	}

	// Context-aware wait: kill the process group on cancel/timeout.
	start := time.Now()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		_ = killProcessGroup(cmd)
		waitErr = <-waitCh
	}

	meta := registry.SandboxMeta{
		OutputTruncated: stdout.Truncated() || stderr.Truncated(),
		DurationMS:      time.Since(start).Milliseconds(),
	}
	if waitErr != nil && ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			meta.KilledReason = "timeout"
		} else {
			meta.KilledReason = "cancelled"
		}
	}
	result := CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Meta:   meta,
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, waitErr
}

// isShellFree reports whether command s contains shell metacharacters.
// Shell-free commands can be invoked as argv directly rather than wrapped
// in /bin/sh -lc, reducing overhead and shell-injection surface area.
func isShellFree(s string) bool {
	shellMetas := "|&;`$<>(){}*?\n"
	return !strings.ContainsAny(s, shellMetas)
}
