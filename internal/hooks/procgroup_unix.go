//go:build !windows

package hooks

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setHookProcessGroup puts the hook in its own process group so that a
// timeout or cancellation can terminate the whole tree, not just the
// immediate shell. Mirrors web/bridge/projectenv_unix.go and
// internal/tools/native/runner_unix.go.
func setHookProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killHookProcessGroup SIGKILLs the hook's whole process group, then the
// direct child as a fallback for a descendant that escaped the group with
// its own setpgid. A group that is already gone is not an error: the
// function returns os.ErrProcessDone, which os/exec treats as "the process
// had already finished" rather than as a cancellation failure.
//
// SIGKILL, with no TERM-then-grace escalation: a grace period would be
// added to the hook's timeout budget, which is exactly what AUD-05 is
// about. internal/pipeline/verify.go and web/bridge/projectenv_unix.go both
// kill the group immediately for the same reason.
func killHookProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	pgErr := syscall.Kill(-pid, syscall.SIGKILL)
	childErr := syscall.Kill(pid, syscall.SIGKILL)
	if errors.Is(pgErr, syscall.ESRCH) && errors.Is(childErr, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	if errors.Is(childErr, syscall.ESRCH) {
		childErr = nil
	}
	if errors.Is(pgErr, syscall.ESRCH) {
		pgErr = nil
	}
	if childErr != nil {
		return childErr
	}
	return pgErr
}
