package sandbox

import (
	"bytes"
	"context"
	"log/slog"

	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

// Passthrough runs commands with no isolation, matching the pre-Milestone-Q
// execRunner behavior. Selected only when configured explicitly via
// backend = "passthrough".
type Passthrough struct {
	logger *slog.Logger
}

func (p *Passthrough) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	runCtx, cancel := runWithTimeout(ctx, req)
	defer cancel()

	cmd := shellContextCommand(runCtx, req.Command)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := nowFn()
	err := cmd.Run()
	result := native.CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	meta := registry.SandboxMeta{
		Enabled:    true,
		Backend:    "passthrough",
		DurationMS: elapsedMS(start),
	}
	if err != nil {
		if reason := killedReason(err, runCtx); reason != "" {
			meta.KilledReason = reason
		}
	}
	result.Meta = meta
	return result, err
}

func (p *Passthrough) Capabilities() Capabilities {
	return Capabilities{Backend: "passthrough"}
}
