package native

import (
	"context"
	"fmt"
	"strings"

	"marshal/internal/worktree"
)

// maxHookWarningChars caps the output tail quoted in a hook-failure warning.
const maxHookWarningChars = 500

// runSetupHooks executes plan.Hooks inside dir through the toolset's
// sandboxed runner, in order. Hooks come from trusted project config and run
// at worktree-creation time inside the same sandbox shell.run uses, so
// restricted/container policy applies without an additional per-hook
// approval prompt.
//
// Every failure is returned as a warning, never an error: a failed hook
// leaves the worktree usable, and setup must never block isolation.
func (t *toolSet) runSetupHooks(ctx context.Context, dir string, plan worktree.SetupPlan) []string {
	var warnings []string
	for _, h := range plan.Hooks {
		result, err := t.runner.Run(ctx, CommandRequest{
			Command:        h.Command,
			Dir:            dir,
			Timeout:        clampTimeout(h.TimeoutSeconds, defaultShellTimeout, maxShellTimeout),
			MaxOutputBytes: t.maxOutputBytes,
		})
		if err == nil && result.ExitCode == 0 {
			continue
		}
		output := result.Stderr
		if output == "" {
			output = result.Stdout
		}
		if err != nil {
			if output == "" {
				output = err.Error()
			} else {
				output = output + ": " + err.Error()
			}
		}
		output = strings.TrimSpace(output)
		if len(output) > maxHookWarningChars {
			output = output[len(output)-maxHookWarningChars:]
		}
		warnings = append(warnings, fmt.Sprintf("setup hook %q failed: %s", h.Command, output))
	}
	return warnings
}
