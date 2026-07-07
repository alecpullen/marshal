package sandbox

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
	cfg    Config
	// Cached, immutable-after-construction env filter sets. buildEnv uses
	// these on every Run without rebuild; EnvAllowlist/EnvDenylist are
	// immutable once Config is copied into the struct.
	denySet  map[string]bool
	allowSet map[string]bool
	logger   *slog.Logger
}

// newRestricted constructs a Restricted backend with precomputed env sets.
func newRestricted(cfg Config, logger *slog.Logger) *Restricted {
	return &Restricted{
		cfg:      cfg,
		denySet:  denySet(cfg.EnvDenylist),
		allowSet: allowSet(cfg.EnvAllowlist),
		logger:   logger,
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

	cmd := shellContextCommand(runCtx, restrictedWrapCommand(req.Command, r.cfg))
	cmd.Dir = req.Dir
	cmd.Env = r.buildEnv()
	applyProcmgmt(cmd) // unix: Setpgid for group kill on timeout

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := nowFn()
	err = cmd.Run()
	result := native.CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	meta := metaFor(r.Capabilities(), r.cfg)
	meta.DurationMS = elapsedMS(start)
	if err != nil {
		if reason := killedReason(err, runCtx); reason != "" {
			meta.KilledReason = reason
		}
	}
	result.Meta = meta
	return result, err
}

// buildEnv constructs the child environment.
//
// Three cases by config state:
//   - Nil EnvAllowlist ("nothing configured"): pass the parent env minus
//     EnvDenylist minus a hardcoded scrub of well-known secret-bearing
//     var prefixes (AWS_*, *_KEY, *_TOKEN, *_SECRET, etc). This is the
//     "no opinion" path — the user lets the parent env through but we
//     still redact the obvious leaks.
//   - Explicit-empty EnvAllowlist (len=0 and != nil): pass a minimal env
//     (PATH only, when present on the host). This honors the user's
//     intent to deny all env. It is NOT treated as "no allowlist
//     configured"; conflating nil and empty was the previous bug.
//   - Non-empty EnvAllowlist: pass only allowlisted vars minus denylisted.
//
// EnvDenylist (r.denySet) is always applied, including in the
// explicit-empty branch (a no-op there, since only PATH is passed).
func (r *Restricted) buildEnv() []string {
	parent := os.Environ()
	// Explicit-empty: user explicitly denied all env. Honor it.
	if r.cfg.EnvAllowlist != nil && len(r.cfg.EnvAllowlist) == 0 {
		if v, ok := os.LookupEnv("PATH"); ok && !r.denySet["PATH"] {
			return []string{"PATH=" + v}
		}
		return nil
	}
	// Nil: no allowlist configured, pass parent env with secret scrub.
	if r.cfg.EnvAllowlist == nil {
		out := make([]string, 0, len(parent))
		for _, kv := range parent {
			key := envKey(kv)
			if r.denySet[key] {
				continue
			}
			if isSecretBearer(key) {
				continue
			}
			out = append(out, kv)
		}
		return out
	}
	// Non-empty allowlist: allowlist minus denylist.
	seen := make(map[string]bool, len(parent))
	out := make([]string, 0, len(r.cfg.EnvAllowlist))
	for _, kv := range parent {
		key := envKey(kv)
		if !r.allowSet[key] {
			continue
		}
		if r.denySet[key] {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, kv)
	}
	return out
}

// isSecretBearer reports whether an env key is commonly secret-bearing.
// Used in the "no explicit allowlist" path so parent env leaks don't carry
// obvious API keys / tokens / credentials to untrusted commands. This is
// a best-effort denylist; users who want stronger isolation must set an
// explicit allowlist.
func isSecretBearer(key string) bool {
	// Exact-name denylist (common credential env vars).
	switch key {
	case "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_ACCESS_KEY_ID":
		return true
	case "GITHUB_TOKEN", "GITHUB_TOKEN_LEGACY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY":
		return true
	case "OPENROUTER_API_KEY", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO":
		return true
	case "DATABASE_URL", "REDIS_URL", "MONGODB_URI", "POSTGRES_PASSWORD":
		return true
	}
	// Prefix/suffix rules for *_KEY, *_TOKEN, *_SECRET, etc.
	k := strings.ToUpper(key)
	for _, p := range []string{"AWS_", "GITHUB_", "OPENAI_", "ANTHROPIC_", "OPENROUTER_", "HUGGINGFACE_", "COHERE_", "GOOGLE_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	for _, s := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIALS", "_API_KEY", "_PRIVATE_KEY", "_ACCESS_KEY", "_CREDENTIAL"} {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

func allowSet(keys []string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func denySet(keys []string) map[string]bool {
	return allowSet(keys)
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
