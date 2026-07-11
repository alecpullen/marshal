//go:build windows

package sandbox

import (
	"os/exec"
	"time"
)

// configureProcessGroup is a no-op on Windows: there is no setpgid
// analogue. terminateProcessTree falls back to Kill on the direct child.
func configureProcessGroup(cmd *exec.Cmd) {}

// terminateProcessTree kills the direct child process immediately on
// Windows. It does not sleep for the Unix grace interval; the grace
// parameter is accepted for API compatibility and ignored.
func terminateProcessTree(cmd *exec.Cmd, grace time.Duration) error {
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}
