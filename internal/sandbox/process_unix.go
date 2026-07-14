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
// group still exists. After the PGID SIGKILL, it also sends SIGKILL to
// the direct child PID as a fallback against grandchildren (or the child
// itself) that may have escaped the PGID via setpgid. ESRCH (process
// already gone) is treated as success.
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

	// Step 3: grace expired — force kill the process group.
	pgidErr := syscall.Kill(negPID, syscall.SIGKILL)

	// Step 4: fallback — also SIGKILL the direct child PID. This handles
	// grandchildren (or the child itself) that may have escaped the PGID
	// via setpgid, and ensures the child process is definitely dead even
	// if the PGID kill failed.
	childErr := syscall.Kill(pid, syscall.SIGKILL)
	if childErr != nil && childErr != syscall.ESRCH {
		return childErr
	}

	// ESRCH on either kill is fine (process already gone). Return the
	// PGID error if it was something other than ESRCH.
	if pgidErr != nil && pgidErr != syscall.ESRCH {
		return pgidErr
	}
	return nil
}
