//go:build !windows

package sandbox

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup sets the child's process group so that
// terminateProcessTree can kill the entire tree (including
// grandchildren) rather than just the direct child.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		return // already started
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcessTree sends SIGTERM to the process group, polls at
// 50 ms intervals until the grace deadline, and sends SIGKILL if the
// group still exists. ESRCH (process already gone) is treated as success.
func terminateProcessTree(cmd *exec.Cmd, grace time.Duration) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	negPID := -pid

	// Step 1: attempt graceful SIGTERM.
	if err := syscall.Kill(negPID, syscall.SIGTERM); err == syscall.ESRCH {
		return nil // already gone
	}

	// Step 2: poll for process-group exit until the grace deadline.
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(negPID, 0); err == syscall.ESRCH {
			return nil // gone
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Step 3: grace expired — force kill.
	err := syscall.Kill(negPID, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
