//go:build !windows

package native

import (
	"os/exec"
	"syscall"
)

// setProcessGroup sets Setpgid on the command so the child runs in its own
// process group. This lets killProcessGroup terminate the entire tree
// (including grandchildren) on cancellation, rather than just the direct
// child (TOOLS-MOD-F8).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		return // already started
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGTERM to the process group of the command's
// child. ESRCH (process already gone) is treated as success.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
