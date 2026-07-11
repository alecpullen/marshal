//go:build windows

package sandbox

import "os/exec"

// shellCommand returns a cmd.exe command that is NOT bound to any
// context — executeCommand handles both start and cancellation.
func shellCommand(command string) *exec.Cmd {
	return exec.Command("cmd", "/C", command)
}
