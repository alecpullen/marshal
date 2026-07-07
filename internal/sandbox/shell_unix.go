//go:build !windows

package sandbox

import (
	"context"
	"os/exec"
)

// shellContextCommand returns a unix shell command bound to ctx so that
// context cancellation/deadline triggers the command's kill path.
// Default kill (via CommandContext) only targets the direct child; backends
// that need to kill descendants use cmd.Cancel (see restricted_unix.go).
func shellContextCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-lc", command)
}
