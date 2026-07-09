package native

import (
	"bytes"
	"context"
	"os/exec"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", req.Command)
	cmd.Dir = req.Dir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
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
