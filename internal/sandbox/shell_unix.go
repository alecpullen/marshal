//go:build !windows

package sandbox

import "os/exec"

// shellCommand returns a shell command that is NOT bound to any
// context — executeCommand handles both start and cancellation.
// It prefers bash with pipefail so a command failing early in a
// pipeline surfaces a non-zero exit code instead of the last stage's
// success; falls back to /bin/sh when bash is unavailable.
func shellCommand(command string) *exec.Cmd {
	if bash, err := exec.LookPath("bash"); err == nil {
		return exec.Command(bash, "-o", "pipefail", "-lc", command)
	}
	return exec.Command("/bin/sh", "-lc", command)
}
