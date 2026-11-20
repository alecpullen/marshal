package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/native"
)

// defaultContainerImage is the fallback image when [tools.shell.sandbox].
// container_image is empty. It is intentionally NOT in config.go's Default()
// as a second source of truth — users see the configured value; this const
// only fires when the configured value is empty after merge.
const defaultContainerImage = "alpine:latest"

// Container runs commands inside a Docker/Podman container with hard
// isolation: private mount namespace (read-only root + rw workspace bind),
// network policy (--network none when AllowNetwork=false), --memory limit,
// capability dropping, no-new-privileges, and a per-command CPU-time kill
// via the image's `timeout` utility (not --cpus, which is a core-quota).
//
// The runtime binary path is pinned at sandbox construction time (see
// container_detect.go) so a later PATH-reorder or planted shim cannot
// substitute a different binary at exec time.
type Container struct {
	cfg         Config
	runtime     string // "docker" or "podman" (user-facing)
	runtimePath string // absolute path pinned at detect time (exec uses this)
	// EnvDenylist cached as a set so buildArgs doesn't rebuild it per Run.
	// EnvDenylist is immutable after construction; this is safe to reuse
	// across calls.
	envDenySet map[string]bool
	logger     *slog.Logger
}

func (c *Container) Capabilities() Capabilities {
	return Capabilities{
		Backend:             "container",
		ResourceLimits:      true,
		NetworkIsolation:    true,
		FilesystemIsolation: true,
	}
}

// SupportsLinkedWorktrees reports false: the container mounts only the workspace
// directory (see buildArgs' -v workdir:workdir:rw), while a linked worktree's
// .git is a file pointing into the repository's common git directory outside it.
// Git commands inside the container therefore cannot resolve the worktree, so
// isolated dispatch refuses this backend instead of mounting extra metadata or
// discovering the failure after a model has started.
func (c *Container) SupportsLinkedWorktrees() bool { return false }

// newContainer constructs a Container. The denySet is precomputed once and
// reused across all Run calls; EnvDenylist / EnvAllowlist are immutable
// after construction, so this is safe across Runs as long as Config isn't
// mutated.
func newContainer(cfg Config, runtime, runtimePath string, logger *slog.Logger) *Container {
	return &Container{
		cfg:         cfg,
		runtime:     runtime,
		runtimePath: runtimePath,
		envDenySet:  denySet(cfg.EnvDenylist),
		logger:      logger,
	}
}

func (c *Container) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return native.CommandResult{Meta: metaFor(c.Capabilities(), c.cfg)}, errEmptyCommand
	}
	if strings.TrimSpace(req.Dir) == "" {
		return native.CommandResult{Meta: metaFor(c.Capabilities(), c.cfg)}, errEmptyDir
	}
	workdir, err := resolveConfinedDir(req.Dir)
	if err != nil {
		return native.CommandResult{Meta: metaFor(c.Capabilities(), c.cfg)}, err
	}

	image := c.cfg.ContainerImage
	if strings.TrimSpace(image) == "" {
		image = defaultContainerImage
	}

	args, err := c.buildArgsWithEnv(req.Command, image, workdir, req.Environment)
	if err != nil {
		return native.CommandResult{Meta: metaFor(c.Capabilities(), c.cfg)}, err
	}
	runCtx, cancel := runWithTimeout(ctx, req)
	defer cancel()

	// Invoke the container runtime via the PINNED absolute path so a
	// runtime-lookalike planted anywhere in the current PATH after
	// detection cannot run under the sandbox. We pass a minimal env and
	// no shell wrapping to avoid any argv-interpolation risk.
	// Use exec.Command (not CommandContext) — executeCommand handles ctx.
	cmd := exec.Command(c.runtimePath, args...)
	cmd.Env = c.buildContainerEnv()

	result, err := executeCommand(runCtx, cmd, req, metaFor(c.Capabilities(), c.cfg))
	// If CPUSeconds was set and the command failed, check whether it's
	// because the `timeout` utility is missing from the image. This is
	// best-effort: the not-found error may be swallowed by the shell
	// wrapping, but when it surfaces it tells the user why their CPU
	// limit isn't being enforced (TOOLS-MIN-F6).
	if c.cfg.CPUSeconds > 0 && err != nil && isTimeoutNotFound(result.ExitCode, result.Stderr) && c.logger != nil {
		c.logger.Warn("container image missing 'timeout' command; CPU limit not enforced",
			"image", image,
			"cpu_seconds", c.cfg.CPUSeconds)
	}
	return result, err
}

// isTimeoutNotFound reports whether a container run failed because the
// `timeout` command was not found in the image, so the CPU time limit was
// not enforced. It requires both a "command not found" exit code (127)
// AND captured stderr that mentions `timeout`. The stderr is inspected
// rather than err.Error() because a real *exec.ExitError from cmd.Wait()
// only reports "exit status 127" and never carries the command's stderr
// output. This is deliberately narrower than matching any "127" or "not
// found" substring, which would false-positive on unrelated errors (e.g.
// an IP address containing 127, a missing image, or a different missing
// binary inside the container) (TOOLS-MIN-F6).
func isTimeoutNotFound(exitCode int, stderr string) bool {
	if exitCode != 127 {
		return false
	}
	return strings.Contains(stderr, "timeout")
}

func (c *Container) buildContainerEnv() []string {
	// Start from the safe allowlist: no parent secrets, no dynamic-loader
	// keys, no shell-hijack keys. Only well-known safe vars survive.
	env := envutil.AllowList(os.Environ())

	// Layer in container-runtime vars that the host CLI legitimately needs
	// for auth (DOCKER_HOST, docker config), proxy (HTTP_PROXY etc.), and
	// buildkit support. These are NOT in the general AllowList but are safe
	// to pass through to the runtime subprocess because they affect only
	// docker/podman itself — not the sandboxed command.
	for _, key := range []string{
		"DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
		"DOCKER_CONFIG", "DOCKER_BUILDKIT",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
	} {
		if envutil.IsDangerousKey(key) || envutil.IsSecretKey(key) {
			continue
		}
		if v, ok := os.LookupEnv(key); ok {
			env = envutil.Set(env, key, v)
		}
	}
	return env
}

func (c *Container) buildArgs(command, image, workdir string) ([]string, error) {
	return c.buildArgsWithEnv(command, image, workdir, nil)
}

// CaptureCommandEnvironment reports the environment the runtime is asked to
// place inside the sandbox: the shell `-e` inputs, filtered by the configured
// allow/deny policy. The runtime CLI's own host environment (buildContainerEnv)
// is a different thing and is deliberately excluded — a variable that exists
// only on the host to configure docker or podman is not a value the image's
// shell has. A value only the image or its login script produces stays unknown.
func (c *Container) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	env := c.shellEnvironment()
	return env, knownCommandEnvironment(env), nil
}

// shellEnvironment is the filtered set of variables passed into the container.
// A nil allowlist keeps the safe defaults; an explicit-empty one passes
// nothing at all, not even PATH.
func (c *Container) shellEnvironment() []string {
	if c.cfg.EnvAllowlist != nil {
		env := make([]string, 0, len(c.cfg.EnvAllowlist))
		for _, key := range c.cfg.EnvAllowlist {
			if c.envDenySet[key] {
				continue
			}
			if envutil.IsDangerousKey(key) || envutil.IsSecretKey(key) {
				continue
			}
			if v, ok := os.LookupEnv(key); ok {
				env = append(env, key+"="+v)
			}
		}
		return env
	}
	return envutil.FilterKeys(envutil.AllowList(os.Environ()), c.envDenySet)
}

// buildArgsWithEnv builds the runtime argv. A non-nil shellEnv is the captured
// container environment: those entries become the `-e` inputs, and the
// runtime's own host environment is still built separately for the CLI. A nil
// shellEnv keeps the legacy derivation from the configured allowlist and the
// host process.
func (c *Container) buildArgsWithEnv(command, image, workdir string, shellEnv []string) ([]string, error) {
	args := []string{"run", "--rm"}

	// Network policy. --network none is enforced when AllowNetwork=false;
	// --network bridge when allowed. Note: this is only honored for the
	// container backend; restricted mode cannot block network.
	if !c.cfg.AllowNetwork {
		args = append(args, "--network", "none")
	} else {
		args = append(args, "--network", "bridge")
	}
	if c.cfg.MemoryLimitMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", c.cfg.MemoryLimitMB))
	}

	// Defense-in-depth: drop all capabilities and disallow new-privilege
	// escalation. The container runs as 65534:65534 on a read-only root
	// anyway; these flags harden the image's setuid helpers and any
	// container escape attempt.
	args = append(args, "--cap-drop=ALL")
	args = append(args, "--security-opt", "no-new-privileges:true")

	// read-only root filesystem + rw workspace bind mount: the command
	// may write to the workspace but not anywhere else in the container.
	args = append(args, "--read-only")
	args = append(args, "-v", fmt.Sprintf("%s:%s:rw", workdir, workdir))
	args = append(args, "-w", workdir)
	args = append(args, "--user", "65534:65534") // nobody/nogroup: a non-root uid

	// Pass through allowlisted env so the command has PATH/HOME/etc inside
	// the container.
	//   - A captured shellEnv (a prepared command) is used as-is: it is the
	//     exact list the preparation analyzed, so policy and launch judge the
	//     same values and a later ambient change cannot swap one for another.
	//   - Nil allowlist: use AllowList defaults (PATH, HOME, etc. minus
	//     secrets/dangerous keys), matching the restricted backend.
	//   - Explicit-empty allowlist (len=0 and != nil): no env vars are
	//     passed at all — not even PATH. This is stricter than the
	//     restricted backend, which inherits PATH for local dev
	//     convenience. The container backend is designed for full
	//     isolation, so an explicit-empty allowlist means no env.
	//
	// Apply the same secret/dangerous-key filtering the restricted backend
	// uses: a user-supplied allowlist must not be used to inject secrets,
	// dynamic-loader keys, or shell-hijack variables into the container.
	if shellEnv != nil {
		// Already filtered by the backend that captured it: re-applying a
		// second policy here would drop values the sandbox deliberately passes
		// (PATH is a shell-hijack key for an injected allowlist, but it is this
		// backend's own safe default).
		for _, kv := range shellEnv {
			args = append(args, "-e", kv)
		}
	} else if c.cfg.EnvAllowlist != nil {
		for _, key := range c.cfg.EnvAllowlist {
			if c.envDenySet[key] {
				continue
			}
			if envutil.IsDangerousKey(key) || envutil.IsSecretKey(key) {
				continue
			}
			if v, ok := os.LookupEnv(key); ok {
				args = append(args, "-e", fmt.Sprintf("%s=%s", key, v))
			}
		}
	} else {
		// Nil allowlist: pass through safe default vars (AllowList).
		for _, kv := range envutil.AllowList(os.Environ()) {
			key := envutil.EnvKey(kv)
			if c.envDenySet[key] {
				continue
			}
			args = append(args, "-e", kv)
		}
	}

	// Image + command. The image is the first positional arg to `run --rm`.
	// The remainder becomes the entrypoint inside the container. If the
	// user set CPUSeconds, we wrap the entrypoint in `timeout` so the
	// sandboxed command is killed after N seconds of wall time — this is
	// the CPU-RUNAWAY bound. Note that `--cpus` is NOT used here because
	// `--cpus` is a CPU core-count quota without any wall-time guarantee,
	// which misrepresents the `cpu_seconds` field.
	//
	// Every admitted command runs through one shell path. buildArgs is
	// transport construction, not a second policy evaluator: the command's
	// syntax and effect were already validated by policy/native admission
	// before launch, and re-splitting it here would re-decide what to run and
	// could change the bytes the person approved. The shell also keeps
	// globbing and expansion available for an explicitly approved destructive
	// operation.
	//
	// Best-effort pipefail: container images may ship dash or busybox sh
	// without pipefail support, so a failed `set` is swallowed and the
	// pipeline runs with last-stage semantics (same as before). On bash/ash
	// it makes failures early in a pipeline surface a non-zero exit code.
	args = append(args, image)
	inner := []string{"/bin/sh", "-lc", "set -o pipefail 2>/dev/null || true; " + command}
	if c.cfg.CPUSeconds > 0 {
		args = append(args,
			"timeout", "--preserve-status", "-s", "KILL",
			strconv.Itoa(c.cfg.CPUSeconds),
		)
	}
	args = append(args, inner...)
	return args, nil
}
