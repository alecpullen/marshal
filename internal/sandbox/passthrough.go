package sandbox

import (
	"context"
	"log/slog"
	"os"

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
	// A prepared command pins its captured environment; nil keeps the legacy
	// behavior of inheriting this process's environment.
	if req.Environment != nil {
		cmd.Env = cloneEnvStrings(req.Environment)
	}
	meta := registry.SandboxMeta{
		Enabled: true,
		Backend: "passthrough",
	}
	return executeCommand(runCtx, cmd, req, meta)
}

// CaptureCommandEnvironment reports the environment passthrough gives a child:
// the ambient environment unchanged, because the backend inherits it. PWD names
// the directory the child actually runs in, so a local PWD fact corresponds to
// the captured cwd rather than the parent process's.
func (p *Passthrough) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	env, known := localCommandEnvironment(os.Environ(), dir)
	return env, known, nil
}

func (p *Passthrough) Capabilities() Capabilities {
	return Capabilities{Backend: "passthrough"}
}

// SupportsLinkedWorktrees reports that commands run unconfined on the host, so a
// linked worktree's pointer into the repository's common git directory resolves.
func (p *Passthrough) SupportsLinkedWorktrees() bool { return true }
