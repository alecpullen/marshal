//go:build !windows

package native

import (
	"os/exec"
	"syscall"
	"time"
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

// terminationGrace is the interval killProcessGroup waits between SIGTERM
// and SIGKILL when terminating a process group after cancellation.
const terminationGrace = 2 * time.Second

// killProcessGroup sends SIGTERM to the process group of the command's
// child, waits up to terminationGrace for it to exit, then escalates to
// SIGKILL on the group and the direct child PID. This mirrors the
// sandbox's terminateProcessTree so a child that ignores SIGTERM (or a
// grandchild that escaped the group via setpgid) cannot leave runCmd
// blocked forever on cancellation (TOOLS-MOD-F8). ESRCH (process already
// gone) is treated as success.
func killProcessGroup(cmd *exec.Cmd) error {
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
	deadline := time.Now().Add(terminationGrace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(negPID, 0); err == syscall.ESRCH {
			return nil // gone
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Step 3: grace expired — force kill the process group.
	pgidErr := syscall.Kill(negPID, syscall.SIGKILL)

	// Step 4: fallback — force kill the direct child PID. This handles
	// grandchildren (or the child itself) that may have escaped the PGID
	// via setpgid, and ensures the child is definitely dead even if the
	// PGID kill failed.
	childErr := syscall.Kill(pid, syscall.SIGKILL)
	if childErr == syscall.ESRCH {
		childErr = nil
	}
	if childErr != nil {
		return childErr
	}
	if pgidErr != nil && pgidErr != syscall.ESRCH {
		return pgidErr
	}
	return nil
}
