package native

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"

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

	// There is exactly one execution form: the shell path. A second, argv-based
	// path classified the command string and then ran a differently-interpreted
	// argv, so one string could be approved as one thing and execute as another.
	// pipefail, output capture, cancellation and OnStart all live in runCmd and
	// are unchanged.
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
	// Managed commands retain a bounded tail and keep forwarding every chunk
	// to the observer; other callers keep prefix capture.
	stdout := NewCommandOutput(OutputLimit(req.MaxOutputBytes), req.Stdout, req.RetainOutputTail)
	stderr := NewCommandOutput(OutputLimit(req.MaxOutputBytes), req.Stderr, req.RetainOutputTail)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if req.Stdin != nil {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

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
