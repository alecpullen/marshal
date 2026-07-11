package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

// terminationGrace is the interval the Unix backend waits between
// SIGTERM and SIGKILL when terminating a process tree after context
// cancellation. Windows bypasses the grace and kills immediately.
const terminationGrace = 2 * time.Second

// executeCommand owns the full execution lifecycle for a prepared
// *exec.Cmd:
//
//  1. Creates BoundedOutput writers for stdout/stderr using the
//     request's MaxOutputBytes and observer writers (req.Stdout/Stderr).
//  2. Calls configureProcessGroup on the cmd.
//  3. Starts the command, records start time, invokes req.OnStart once.
//  4. Waits for completion in a goroutine and selects between that
//     result and ctx.Done().
//  5. On context cancellation, calls terminateProcessTree with the
//     terminationGrace, then drains the wait result before returning.
//  6. Fills exit code, duration, output strings, OutputTruncated flag,
//     and KilledReason ("timeout" or "cancelled").
//
// executeCommand does NOT set cmd.Dir or cmd.Env — those are the
// caller's responsibility. It only attaches output writers, starts the
// command, waits, and handles termination.
func executeCommand(ctx context.Context, cmd *exec.Cmd, req native.CommandRequest, meta registry.SandboxMeta) (native.CommandResult, error) {
	stdout := native.NewBoundedOutput(native.OutputLimit(req.MaxOutputBytes), req.Stdout)
	stderr := native.NewBoundedOutput(native.OutputLimit(req.MaxOutputBytes), req.Stderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	configureProcessGroup(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		meta.DurationMS = time.Since(start).Milliseconds()
		return native.CommandResult{Meta: meta}, err
	}

	if req.OnStart != nil {
		req.OnStart(cmd.Process.Pid)
	}

	// Buffered channel (capacity 1) so the goroutine always completes
	// even if the select picks ctx.Done() first.
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case waitErr := <-waitCh:
		// Command completed before the context was cancelled.
		meta.DurationMS = time.Since(start).Milliseconds()
		meta.OutputTruncated = stdout.Truncated() || stderr.Truncated()
		result := native.CommandResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Meta:     meta,
			ExitCode: exitCodeFromState(cmd.ProcessState),
		}
		return result, waitErr

	case <-ctx.Done():
		// Context cancelled or deadline exceeded. Terminate the
		// process tree, then drain the wait result.
		_ = terminateProcessTree(cmd, terminationGrace)
		waitErr := <-waitCh

		meta.DurationMS = time.Since(start).Milliseconds()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			meta.KilledReason = "timeout"
		} else {
			meta.KilledReason = "cancelled"
		}
		meta.OutputTruncated = stdout.Truncated() || stderr.Truncated()
		result := native.CommandResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Meta:     meta,
			ExitCode: exitCodeFromState(cmd.ProcessState),
		}
		return result, waitErr
	}
}

// exitCodeFromState returns the exit code from a process state, or 0
// when the state is nil.
func exitCodeFromState(ps *os.ProcessState) int {
	if ps == nil {
		return 0
	}
	return ps.ExitCode()
}
