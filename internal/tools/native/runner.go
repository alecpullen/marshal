package native

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"marshal/internal/tools/registry"
)

type execRunner struct{}

// LinkedWorktreeRunner is a CommandRunner that can execute commands inside a
// linked git worktree. A linked worktree's .git is a file pointing into the
// repository's common git directory, which lives outside the worktree root, so
// a backend that mounts or confines only the working directory cannot host one.
//
// Support is a property of the backend, never a model-supplied argument:
// callers derive it and refuse an unsupported isolated dispatch before
// allocating anything.
type LinkedWorktreeRunner interface {
	SupportsLinkedWorktrees() bool
}

// SupportsLinkedWorktrees reports whether runner can host a linked git
// worktree. A runner that does not implement LinkedWorktreeRunner supports
// nothing: an absent capability is not a yes.
func SupportsLinkedWorktrees(runner CommandRunner) bool {
	linked, ok := runner.(LinkedWorktreeRunner)
	return ok && linked.SupportsLinkedWorktrees()
}

// SupportsLinkedWorktrees reports that the default runner executes on the host,
// where a linked worktree's common git directory is reachable.
func (execRunner) SupportsLinkedWorktrees() bool { return true }

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
	// A prepared command pins the environment it was approved with. Nil is the
	// legacy direct call, which inherits this process's environment.
	if req.Environment != nil {
		cmd.Env = cloneEnv(req.Environment)
	}
	return runCmd(ctx, cmd, req)
}

// CaptureCommandEnvironment reports the environment this runner gives a child:
// the whole ambient environment it inherits, with PWD naming the directory the
// child actually runs in rather than whichever directory this process was
// started from.
func (execRunner) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	env := cloneEnv(os.Environ())
	if dir != "" {
		env = setEnvValue(env, "PWD", dir)
	}
	if env == nil {
		env = []string{}
	}
	return env, knownEnvironmentFrom(env), nil
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
