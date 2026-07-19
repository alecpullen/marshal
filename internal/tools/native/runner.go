package native

import (
	"context"
	"os/exec"
	"strings"

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
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = req.Dir
		return runCmd(ctx, cmd, req)
	}

	// Fall through to shell path: commands with shell metacharacters,
	// destructive commands, or commands that failed to parse.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", req.Command)
	cmd.Dir = req.Dir
	return runCmd(ctx, cmd, req)
}

// runCmd wires stdout/stderr observers, starts, waits, and returns the result.
func runCmd(ctx context.Context, cmd *exec.Cmd, req CommandRequest) (CommandResult, error) {
	stdout := NewBoundedOutput(OutputLimit(req.MaxOutputBytes), req.Stdout)
	stderr := NewBoundedOutput(OutputLimit(req.MaxOutputBytes), req.Stderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}
	if req.OnStart != nil {
		req.OnStart(cmd.Process.Pid)
	}

	err := cmd.Wait()
	result := CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Meta: registry.SandboxMeta{
			OutputTruncated: stdout.Truncated() || stderr.Truncated(),
		},
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, err
}

// isShellFree reports whether command s contains shell metacharacters.
// Shell-free commands can be invoked as argv directly rather than wrapped
// in /bin/sh -lc, reducing overhead and shell-injection surface area.
func isShellFree(s string) bool {
	shellMetas := "|&;`$<>(){}*?\n"
	return !strings.ContainsAny(s, shellMetas)
}
