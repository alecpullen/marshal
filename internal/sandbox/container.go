package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/google/shlex"

	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
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

	args := c.buildArgs(req.Command, image, workdir)
	runCtx, cancel := runWithTimeout(ctx, req)
	defer cancel()

	// Invoke the container runtime via the PINNED absolute path so a
	// runtime-lookalike planted anywhere in the current PATH after
	// detection cannot run under the sandbox. We pass a minimal env and
	// no shell wrapping to avoid any argv-interpolation risk.
	// Use exec.Command (not CommandContext) — executeCommand handles ctx.
	cmd := exec.Command(c.runtimePath, args...)
	cmd.Env = c.buildContainerEnv()

	return executeCommand(runCtx, cmd, req, metaFor(c.Capabilities(), c.cfg))
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

func (c *Container) buildArgs(command, image, workdir string) []string {
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
	// the container. Empty allowlist = no env (consistent with restricted
	// mode's behavior for explicit-empty allowlist).
	//
	// Apply the same secret/dangerous-key filtering the restricted backend
	// uses: a user-supplied allowlist must not be used to inject secrets,
	// dynamic-loader keys, or shell-hijack variables into the container.
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

	// Image + command. The image is the first positional arg to `run --rm`.
	// The remainder becomes the entrypoint inside the container. If the
	// user set CPUSeconds, we wrap the entrypoint in `timeout` so the
	// sandboxed command is killed after N seconds of wall time — this is
	// the CPU-RUNAWAY bound. Note that `--cpus` is NOT used here because
	// `--cpus` is a CPU core-count quota without any wall-time guarantee,
	// which misrepresents the `cpu_seconds` field.
	//
	// For shell-free commands (no pipes, redirects, variable expansion, etc.)
	// we invoke the command directly as argv to avoid shell-wrapping overhead
	// and potential shell-injection surface. Commands containing shell
	// metacharacters still go through /bin/sh -lc.
	//
	// Destructive commands (e.g. rm -rf, git clean -f) always go through
	// /bin/sh -lc so that shell features (globbing, variable expansion) are
	// available when the user has explicitly approved a destructive operation.
	// This is enforced via ClassifyCommand, not just isShellFree.
	args = append(args, image)
	shellFree := isShellFree(command)
	if shellFree {
		cls, _ := policy.ClassifyCommand(command)
		if cls.Risk == registry.RiskDestructive {
			shellFree = false
		}
	}
	var inner []string
	if shellFree {
		parsed, err := shlex.Split(command)
		if err != nil {
			parsed = strings.Fields(command)
		}
		inner = parsed
	} else {
		// Best-effort pipefail: container images may ship dash or
		// busybox sh without pipefail support, so a failed `set` is
		// swallowed and the pipeline runs with last-stage semantics
		// (same as before). On bash/ash it makes failures early in a
		// pipeline surface a non-zero exit code.
		inner = []string{"/bin/sh", "-lc", "set -o pipefail 2>/dev/null || true; " + command}
	}
	if c.cfg.CPUSeconds > 0 {
		args = append(args,
			"timeout", "--preserve-status", "-s", "KILL",
			strconv.Itoa(c.cfg.CPUSeconds),
		)
	}
	args = append(args, inner...)
	return args
}

// isShellFree reports whether command s contains no shell metacharacters.
// Shell-free commands can be invoked as argv directly rather than wrapped
// in /bin/sh -lc, reducing overhead and shell-injection surface area.
//
// Metacharacters that trigger shell wrapping:
//   - | & ; ` $ ( ) { } < > * ? \n
func isShellFree(s string) bool {
	shellMetas := "|&;`$<>(){}*?\n"
	return !strings.ContainsAny(s, shellMetas)
}
