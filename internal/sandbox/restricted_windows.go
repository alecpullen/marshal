//go:build windows

package sandbox

import (
	"errors"
	"os/exec"
)

var (
	errEmptyCommand = errors.New("sandbox: command is required")
	errEmptyDir     = errors.New("sandbox: working directory is required")
)

// restrictedResourceLimitsSupported is false on windows: ulimit is unix-only.
func restrictedResourceLimitsSupported() bool {
	return false
}

// restrictedWrapCommand is a no-op on windows (no ulimit equivalent).
func restrictedWrapCommand(command string, cfg Config) string {
	return command
}

// applyProcmgmt is a no-op on windows (no setpgid analogue for this build).
func applyProcmgmt(cmd *exec.Cmd) {}
