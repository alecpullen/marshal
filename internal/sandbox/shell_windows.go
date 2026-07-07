//go:build windows

package sandbox

import (
	"context"
	"os/exec"
)

func shellContextCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd", "/C", command)
}
