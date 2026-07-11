package native

import (
	"context"
	"os/exec"

	"marshal/internal/tools/registry"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", req.Command)
	cmd.Dir = req.Dir

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

func (execRunner) Start(req CommandRequest) (*runningCmd, error) {
	cmd := exec.Command("/bin/sh", "-lc", req.Command)
	cmd.Dir = req.Dir

	rc := &runningCmd{cmd: cmd}
	cmd.Stdout = &rc.stdout
	cmd.Stderr = &rc.stderr

	if err := startWithProcessGroup(cmd); err != nil {
		return nil, err
	}
	return rc, nil
}
