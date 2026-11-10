//go:build linux || darwin

package bridge

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureEnvStep runs a step in its own process group and kills the whole
// group when the step's time is up (decision 2), so a startup file that
// backgrounds a process cannot hold the step open. WaitDelay bounds the wait
// after the kill for any I/O that outlives the group.
func configureEnvStep(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	cmd.WaitDelay = envKillWaitDelay
}
