package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// allowedCommands is the fixed set of executables run_command may invoke.
// Never use sh/bash to avoid shell injection.
var allowedCommands = map[string]bool{
	"go":      true,
	"make":    true,
	"npm":     true,
	"npx":     true,
	"python":  true,
	"python3": true,
	"pytest":  true,
	"cargo":   true,
	"rg":      true,
}

func runCommand(ctx context.Context, repoRoot string, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("run_command: args must not be empty")
	}
	exe := args[0]
	if !allowedCommands[exe] {
		allowed := make([]string, 0, len(allowedCommands))
		for k := range allowedCommands {
			allowed = append(allowed, k)
		}
		return "", fmt.Errorf("run_command: %q is not in the allowed command list (%s)", exe, strings.Join(allowed, ", "))
	}

	cmd := exec.CommandContext(ctx, exe, args[1:]...)
	cmd.Dir = repoRoot

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		// Return stdout+stderr as content so the model can see what went wrong.
		return out.String(), fmt.Errorf("run_command: %w", err)
	}
	return out.String(), nil
}
