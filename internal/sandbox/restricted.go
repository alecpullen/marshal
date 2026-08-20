package sandbox

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/native"
)

// Restricted is the default in-process execution backend. Hardening applied
// on all platforms:
//
//   - cwd confinement to req.Dir (resolved to absolute; rejected if empty)
//   - env scrubbing: child env built only from envAllowlist minus envDenylist
//   - context timeout, with process-group kill on deadline/cancel (unix)
//   - output capture
//
// On unix, resource caps (cpu/file-size/max-procs) additionally wrap the
// command in a `ulimit ...; exec <cmd>` script (see restricted_unix.go). On
// darwin, address-space (`ulimit -v`) is unsupported, so memory caps are
// best-effort/no-op; ResourceLimits support reflects this per platform.
//
// Network isolation is NOT enforced here — restricted cannot block network
// cross-platform. Only the container backend reports NetworkIsolated=true.
type Restricted struct {
	cfg Config
	// Cached, immutable-after-construction env filter set. buildEnv uses
	// this on every Run without rebuild; EnvDenylist is immutable once
	// Config is copied into the struct.
	denySet map[string]bool
	logger  *slog.Logger
}

// newRestricted constructs a Restricted backend with precomputed env filter.
func newRestricted(cfg Config, logger *slog.Logger) *Restricted {
	return &Restricted{
		cfg:     cfg,
		denySet: denySet(cfg.EnvDenylist),
		logger:  logger,
	}
}

// Capabilities reports what restricted mode actually enforces here.
func (r *Restricted) Capabilities() Capabilities {
	return Capabilities{
		Backend:             "restricted",
		ResourceLimits:      restrictedResourceLimitsSupported(),
		NetworkIsolation:    false,
		FilesystemIsolation: false,
	}
}

func (r *Restricted) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return native.CommandResult{Meta: metaFor(r.Capabilities(), r.cfg)}, errEmptyCommand
	}
	dir, err := resolveConfinedDir(req.Dir)
	if err != nil {
		return native.CommandResult{Meta: metaFor(r.Capabilities(), r.cfg)}, err
	}
	req.Dir = dir

	runCtx, cancel := runWithTimeout(ctx, req)
	defer cancel()

	cmd := shellCommand(restrictedWrapCommand(req.Command, r.cfg))
	cmd.Dir = req.Dir
	cmd.Env = r.buildEnv()

	return executeCommand(runCtx, cmd, req, metaFor(r.Capabilities(), r.cfg))
}

// buildEnv constructs the child environment.
//
// Three cases by config state:
//   - Nil EnvAllowlist ("nothing configured"): use envutil.AllowList to
//     inherit only a curated set of safe env vars (PATH, HOME, USER, etc.),
//     minus EnvDenylist. This is the security-hardened default: instead of
//     passing the full parent env with a best-effort secret scrub, we only
//     pass vars that are known-safe. (F-SAFE-23)
//   - Explicit-empty EnvAllowlist (len=0 and != nil): pass a minimal env
//     (PATH only, when present on the host). This honors the user's
//     intent to deny all env. It is NOT treated as "no allowlist
//     configured"; conflating nil and empty was the previous bug.
//   - Non-empty EnvAllowlist: start from the safe defaults (AllowList),
//     then layer the user's explicitly allowlisted vars on top. Any user
//     entry rejected by IsDangerousKey or IsSecretKey is silently dropped.
//
// EnvDenylist (r.denySet) is always applied to the final result.
func (r *Restricted) buildEnv() []string {
	parent := os.Environ()
	// Explicit-empty allowlist (len=0 and != nil): the user has
	// intentionally denied all environment variables. We pass only
	// PATH (when present and not denied) so the child can find
	// executables. This is the restricted-backend behavior: local
	// dev convenience means PATH is still inherited. The container
	// backend passes nothing for an explicit-empty allowlist.
	if r.cfg.EnvAllowlist != nil && len(r.cfg.EnvAllowlist) == 0 {
		if v, ok := os.LookupEnv("PATH"); ok && !r.denySet["PATH"] {
			return []string{"PATH=" + v}
		}
		return nil
	}
	// Nil: use AllowList — only pass safe vars minus denylist.
	if r.cfg.EnvAllowlist == nil {
		return envutil.FilterKeys(envutil.AllowList(parent), r.denySet)
	}
	// Non-empty allowlist: start from AllowList then layer user entries.
	out := envutil.AllowList(parent)

	// Build parent env lookup for user-supplied keys.
	parentVars := make(map[string]string, len(parent))
	for _, kv := range parent {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			parentVars[k] = v
		}
	}

	for _, key := range r.cfg.EnvAllowlist {
		if r.denySet[key] {
			continue
		}
		if envutil.IsDangerousKey(key) {
			continue
		}
		if envutil.IsSecretKey(key) {
			continue
		}
		v, ok := parentVars[key]
		if !ok {
			continue
		}
		out = envutil.Set(out, key, v)
	}

	// Apply denylist to the final result.
	return envutil.FilterKeys(out, r.denySet)
}

func denySet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// resolveConfinedDir returns an absolute path for dir, rejecting empty or
// resolvable-to-relative working directories. The caller's dir is the
// workspace root for tool execution and must be absolute to prevent the
// sandboxed command from writing outside the workspace.
func resolveConfinedDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errEmptyDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, nil
}
