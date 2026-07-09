//go:build windows

package native

import (
	"os/exec"
)

type runningCmd struct {
	cmd    *exec.Cmd
	stdout safeBuffer
	stderr safeBuffer
}

func startWithProcessGroup(cmd *exec.Cmd) error {
	return cmd.Start()
}

func killProcessTree(rc *runningCmd) error {
	if rc == nil || rc.cmd == nil || rc.cmd.Process == nil {
		return nil
	}
	return rc.cmd.Process.Kill()
}
