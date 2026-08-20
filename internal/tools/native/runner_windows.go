//go:build windows

package native

import "os/exec"

// setProcessGroup is a no-op on Windows: there is no setpgid analogue.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills the direct child process on Windows. It does not
// attempt to kill grandchildren.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
