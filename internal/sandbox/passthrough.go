package sandbox

import (
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

	cmd := shellCommand(req.Command)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	meta := registry.SandboxMeta{
		Enabled: true,
		Backend: "passthrough",
	}
	return executeCommand(runCtx, cmd, req, meta)
}

func (p *Passthrough) Capabilities() Capabilities {
	return Capabilities{Backend: "passthrough"}
}
