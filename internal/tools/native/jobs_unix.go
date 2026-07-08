//go:build !windows

package native

import (
	"os/exec"
	"syscall"
)

type runningCmd struct {
	cmd    *exec.Cmd
	stdout safeBuffer
	stderr safeBuffer
}

func startWithProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func killProcessTree(rc *runningCmd) error {
	if rc == nil || rc.cmd == nil || rc.cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(rc.cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	}
	_ = rc.cmd.Process.Signal(syscall.SIGTERM)
	return nil
}
