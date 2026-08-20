package sandbox

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/tools/native"
)

// newTestSandbox builds a sandbox with the given resolved Config but a logger
// nil (tests don't need logs).
func newTestSandbox(t *testing.T, cfg Config) Sandbox {
	t.Helper()
	sb, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return sb
}

func TestNewUnknownBackendErrors(t *testing.T) {
	if _, err := New(Config{Backend: "bogus"}, nil); err == nil {
		t.Fatal("expected error for unknown backend, got nil")
	}
}

func TestNewEmptyBackendDefaultsToRestricted(t *testing.T) {
	t.Setenv("MARSHAL_TEST", "v")
	sb := newTestSandbox(t, Config{Backend: "", EnvAllowlist: []string{"MARSHAL_TEST"}})
	if sb.Capabilities().Backend != "restricted" {
		t.Fatalf("backend = %q, want restricted", sb.Capabilities().Backend)
	}
}

func TestNewPassthroughBackend(t *testing.T) {
	sb := newTestSandbox(t, Config{Backend: "passthrough", UnsafePassthrough: true})
	caps := sb.Capabilities()
	if caps.Backend != "passthrough" || caps.ResourceLimits || caps.NetworkIsolation {
		t.Fatalf("passthrough caps wrong: %+v", caps)
	}
}

func TestNewPassthroughBackend_RequiresFlag(t *testing.T) {
	// Without the flag it must error.
	_, err := New(Config{Backend: "passthrough"}, nil)
	if err == nil {
		t.Fatal("expected error for passthrough without UnsafePassthrough=true")
	}
	if !strings.Contains(err.Error(), "unsafe_passthrough") {
		t.Fatalf("error message should mention unsafe_passthrough, got: %v", err)
	}

	// With the flag it must succeed.
	sb, err := New(Config{Backend: "passthrough", UnsafePassthrough: true}, nil)
	if err != nil {
		t.Fatalf("expected ok with UnsafePassthrough=true, got: %v", err)
	}
	if sb.Capabilities().Backend != "passthrough" {
		t.Fatalf("backend = %q, want passthrough", sb.Capabilities().Backend)
	}
}

func TestNewContainerBackendFallsBackToRestrictedWhenNoRuntime(t *testing.T) {
	// Force a runtime choice that will never be found on the test host.
	t.Setenv("PATH", "/nonexistent")
	sb := newTestSandbox(t, Config{Backend: "container", ContainerRuntime: "definitely-not-a-runtime", AllowFallback: true})
	if sb.Capabilities().Backend != "restricted" {
		t.Fatalf("container with no runtime should fall back to restricted, got %q", sb.Capabilities().Backend)
	}
}

func TestNewContainerBackendHardFailsWithoutAllowFallback(t *testing.T) {
	// Force a runtime choice that will never be found on the test host.
	t.Setenv("PATH", "/nonexistent")
	_, err := New(Config{Backend: "container"}, nil)
	if err == nil {
		t.Fatal("expected hard error when container backend requested without allow_fallback, got nil")
	}
	if !strings.Contains(err.Error(), "container backend requested") {
		t.Fatalf("error message wrong: %v", err)
	}
}

func TestRestrictedRunExecutesAndRecordsMeta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell echo differs on windows")
	}
	sb := newTestSandbox(t, Config{Backend: "restricted"})
	dir := t.TempDir()
	res, err := sb.Run(context.Background(), native.CommandRequest{
		Command: "echo hello",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if res.Meta.Backend != "restricted" || !res.Meta.Enabled {
		t.Fatalf("meta = %+v", res.Meta)
	}
	if res.Meta.DurationMS < 0 {
		t.Fatalf("negative duration: %d", res.Meta.DurationMS)
	}
}

func TestRestrictedRejectsEmptyCommand(t *testing.T) {
	sb := newTestSandbox(t, Config{Backend: "restricted"})
	if _, err := sb.Run(context.Background(), native.CommandRequest{Command: "   ", Dir: t.TempDir()}); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestRestrictedRejectsEmptyDir(t *testing.T) {
	sb := newTestSandbox(t, Config{Backend: "restricted"})
	if _, err := sb.Run(context.Background(), native.CommandRequest{Command: "echo hi", Dir: ""}); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestRestrictedEnvScrubbingDropsNotAllowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("env scrub test relies on sh -c 'printenv'")
	}
	t.Setenv("MARSHAL_KEEP", "kept")
	t.Setenv("MARSHAL_DROP", "dropped")
	sb := newTestSandbox(t, Config{
		Backend:      "restricted",
		EnvAllowlist: []string{"MARSHAL_KEEP", "PATH"},
		EnvDenylist:  []string{"MARSHAL_DROP"},
	})
	dir := t.TempDir()
	// printenv prints allowed var; ensure MARSHAL_DROP is absent.
	res, err := sb.Run(context.Background(), native.CommandRequest{
		Command: "printenv MARSHAL_DROP || echo absent",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "absent") {
		t.Fatalf("denylisted var leaked into child env: %q", res.Stdout)
	}
	res2, err := sb.Run(context.Background(), native.CommandRequest{
		Command: "printenv MARSHAL_KEEP",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run keep: %v", err)
	}
	if !strings.Contains(res2.Stdout, "kept") {
		t.Fatalf("allowlisted var not present: %q", res2.Stdout)
	}
}

func TestRestrictedEmptyAllowlistDeniesEnvInversion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("env scrub test relies on sh")
	}
	// Set a secret-looking var on the parent. With an explicit-empty
	// allowlist the user is saying "deny all env" — it must not invert
	// to full parent-env leak.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leaked")
	t.Setenv("MARSHAL_RANDOM", "random")
	sb := newTestSandbox(t, Config{
		Backend:      "restricted",
		EnvAllowlist: []string{}, // explicit empty: deny all
	})
	dir := t.TempDir()
	res, err := sb.Run(context.Background(), native.CommandRequest{
		Command: "printenv MARSHAL_RANDOM || echo absent; printenv AWS_SECRET_ACCESS_KEY || echo aws_absent",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "absent") {
		t.Fatalf("explicit-empty allowlist still leaked parent env: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "aws_absent") {
		t.Fatalf("AWS_SECRET_ACCESS_KEY leaked with empty allowlist: %q", res.Stdout)
	}
}

func TestRestrictedNilAllowlistUsesSafeDefaultsAndScrubsSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("env scrub test relies on sh")
	}
	// With nil allowlist, buildEnv now defaults to AllowList, so:
	// - PATH should pass (it's in allowlistKeys)
	// - ANTHROPIC_API_KEY and AWS_SECRET_ACCESS_KEY should be scrubbed
	// - MARSHAL_RANDOM should be scrubbed (not in allowlistKeys)
	t.Setenv("ANTHROPIC_API_KEY", "sk-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-leak")
	t.Setenv("MARSHAL_RANDOM", "random")
	sb := newTestSandbox(t, Config{
		Backend:      "restricted",
		EnvAllowlist: nil, // nil → use AllowList
	})
	dir := t.TempDir()
	res, err := sb.Run(context.Background(), native.CommandRequest{
		Command: `printenv ANTHROPIC_API_KEY || echo anthro_scrubbed; printenv AWS_SECRET_ACCESS_KEY || echo aws_scrubbed; echo PATH=$(printenv PATH || echo no_path)`,
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "anthro_scrubbed") {
		t.Fatalf("nil allowlist should scrub ANTHROPIC_API_KEY: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "aws_scrubbed") {
		t.Fatalf("nil allowlist should scrub AWS_SECRET_ACCESS_KEY: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "PATH=/") {
		t.Fatalf("nil allowlist should preserve PATH: %q", res.Stdout)
	}
	// MARSHAL_RANDOM is NOT in allowlistKeys — must be scrubbed in new code.
	res2, err2 := sb.Run(context.Background(), native.CommandRequest{
		Command: "printenv MARSHAL_RANDOM || echo not_present",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err2 != nil {
		t.Fatalf("Run: %v", err2)
	}
	if !strings.Contains(res2.Stdout, "not_present") {
		t.Fatalf("nil allowlist should scrub vars not in allowlistKeys (MARSHAL_RANDOM): %q", res2.Stdout)
	}
}

func TestRestrictedNonEmptyAllowlistRejectsDangerousKeys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("env scrub test relies on sh")
	}
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	t.Setenv("MARSHAL_SAFE", "ok_value")
	sb := newTestSandbox(t, Config{
		Backend:      "restricted",
		EnvAllowlist: []string{"LD_PRELOAD", "MARSHAL_SAFE", "PATH"},
		EnvDenylist:  nil,
	})
	dir := t.TempDir()
	res, err := sb.Run(context.Background(), native.CommandRequest{
		Command: `printenv MARSHAL_SAFE || echo safe_lost; printenv LD_PRELOAD || echo dangerous_scrubbed`,
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "ok_value") {
		t.Fatalf("non-empty allowlist should preserve MARSHAL_SAFE: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "dangerous_scrubbed") {
		t.Fatalf("non-empty allowlist should reject LD_PRELOAD (dangerous): %q", res.Stdout)
	}
}

func TestRestrictedTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pgroup kill is unix-only")
	}
	sb := newTestSandbox(t, Config{Backend: "restricted"})
	dir := t.TempDir()
	// Spawn a child (`sleep 30`) that would outlive its parent if not killed
	// as a group. If the group kill works, Run returns within the timeout
	// instead of hanging ~30s waiting on `wait`.
	start := time.Now()
	_, _ = sb.Run(context.Background(), native.CommandRequest{
		Command: `( sleep 30 ) & echo started; wait`,
		Dir:     dir,
		Timeout: 400 * time.Millisecond,
	})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run did not kill process group; elapsed = %v", elapsed)
	}
}

func TestRestrictedWrapCommandAppliesUlimitsOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ulimit is unix-only")
	}
	wrapped := restrictedWrapCommand("echo hi", Config{CPUSeconds: 10, MaxProcesses: 64, FileSizeLimitMB: 16})
	if !strings.Contains(wrapped, "ulimit -t 10") {
		t.Fatalf("cpu ulimit missing: %q", wrapped)
	}
	if !strings.Contains(wrapped, "ulimit -u 64") {
		t.Fatalf("max-procs ulimit missing: %q", wrapped)
	}
	if !strings.Contains(wrapped, "ulimit -f ") {
		t.Fatalf("file-size ulimit missing: %q", wrapped)
	}
	// 16 MB * 1024 * 1024 / blockSize per ulimitBlockSize().
	var wantBlocks int
	if runtime.GOOS == "linux" {
		wantBlocks = (16 * 1024 * 1024) / 1024 // 16384 on Linux (1K-block)
	} else {
		wantBlocks = (16 * 1024 * 1024) / 512 // 32768 on darwin/BSD (512B block)
	}
	expected := fmt.Sprintf("ulimit -f %d", wantBlocks)
	if !strings.Contains(wrapped, expected) {
		t.Fatalf("expected %q in wrapped command: %q", expected, wrapped)
	}
	// The command must be appended verbatim, with no `exec` prefix: `exec`
	// binds tighter than the shell's list operators and would truncate any
	// compound command. See TestRestrictedRunsCompoundCommands.
	if !strings.HasSuffix(wrapped, "; echo hi") {
		t.Fatalf("command tail not appended verbatim: %q", wrapped)
	}
	if strings.Contains(wrapped, "exec ") {
		t.Fatalf("wrapper must not prefix the command with exec: %q", wrapped)
	}
}

// TestRestrictedRunsCompoundCommands is a regression test for the `exec`
// prefix the ulimit wrapper used to emit. Because config.Default() sets
// MaxProcesses = 2048, the wrapper is active out of the box, so `exec`
// silently truncated ordinary compound commands: `echo one; echo two`
// returned only "one" with exit 0, and any command opening with a shell
// builtin died with "exec: cd: not found".
func TestRestrictedRunsCompoundCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ulimit wrapper is unix-only")
	}
	// Mirror the shipped default so the ulimit wrapper is engaged.
	sb := newTestSandbox(t, Config{Backend: "restricted", MaxProcesses: 2048})
	dir := t.TempDir()

	cases := []struct{ name, command, want string }{
		{"sequence", "echo one; echo two", "one\ntwo"},
		{"and_list", "echo A && echo B", "A\nB"},
		{"builtin_lead", "cd / && pwd", "/"},
		{"assignment_lead", "X=5; echo $X", "5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := sb.Run(context.Background(), native.CommandRequest{
				Command: tc.command,
				Dir:     dir,
			})
			if err != nil {
				t.Fatalf("Run(%q): %v (stderr %q)", tc.command, err, res.Stderr)
			}
			if res.ExitCode != 0 {
				t.Fatalf("Run(%q) exit = %d, want 0 (stderr %q)", tc.command, res.ExitCode, res.Stderr)
			}
			if got := strings.TrimSpace(res.Stdout); got != tc.want {
				t.Fatalf("Run(%q) stdout = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestExplicitEmptyAllowlistDocumented verifies that both backends
// document the explicit-empty allowlist behavior (TOOLS-MOD-F26).
func TestExplicitEmptyAllowlistDocumented(t *testing.T) {
	restrictedSrc, err := os.ReadFile("restricted.go")
	if err != nil {
		t.Skip("cannot read restricted.go: " + err.Error())
	}
	if !strings.Contains(string(restrictedSrc), "Explicit-empty allowlist") {
		t.Error("restricted.go should document explicit-empty allowlist behavior")
	}

	containerSrc, err := os.ReadFile("container.go")
	if err != nil {
		t.Skip("cannot read container.go: " + err.Error())
	}
	if !strings.Contains(string(containerSrc), "Explicit-empty allowlist") {
		t.Error("container.go should document explicit-empty allowlist behavior")
	}
}

// TestClampUlimit verifies that clampUlimit clamps ulimit values to
// reasonable bounds (TOOLS-MIN-F20): values below 1 are raised to 1, and
// values above the system RLIMIT_NOFILE hard limit are lowered to it.
func TestClampUlimit(t *testing.T) {
	// Lower bound: values < 1 are clamped to 1.
	if got := clampUlimit(0, "cpu_seconds"); got != 1 {
		t.Errorf("clampUlimit(0) = %d, want 1", got)
	}
	if got := clampUlimit(-5, "max_processes"); got != 1 {
		t.Errorf("clampUlimit(-5) = %d, want 1", got)
	}

	// Upper bound: a value above the RLIMIT_NOFILE hard limit is clamped.
	var rlimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlimit); err != nil {
		t.Skip("cannot read RLIMIT_NOFILE: " + err.Error())
	}
	max := int(rlimit.Max)
	// RLIM_INFINITY (or a value at/near the int max) means "unlimited" —
	// there is no finite upper bound to clamp against, so the upper-bound
	// clamp is untestable here.
	if max <= 0 || max >= int(^uint(0)>>1) {
		t.Skip("RLIMIT_NOFILE hard limit is effectively unlimited; upper-bound clamp untestable")
	}
	over := max + 1
	if got := clampUlimit(over, "max_processes"); got != max {
		t.Errorf("clampUlimit(%d) = %d, want clamped to %d", over, got, max)
	}

	// In-range values pass through unchanged.
	if got := clampUlimit(1, "cpu_seconds"); got != 1 {
		t.Errorf("clampUlimit(1) = %d, want 1", got)
	}
}

func TestRestrictedWrapCommandNoOpWithoutLimits(t *testing.T) {
	got := restrictedWrapCommand("echo hi", Config{})
	if got != "echo hi" {
		t.Fatalf("expected unchanged, got %q", got)
	}
}

func TestContainerBuildArgsNetworkDisabled(t *testing.T) {
	c := &Container{cfg: Config{AllowNetwork: false, MemoryLimitMB: 1024, CPUSeconds: 2, ContainerImage: "alpine:latest"}, runtime: "docker", runtimePath: "/usr/local/bin/docker"}
	args, err := c.buildArgs("go test ./...", "alpine:latest", "/work")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network none") {
		t.Fatalf("network none missing: %s", joined)
	}
	if !strings.Contains(joined, "--memory 1024m") {
		t.Fatalf("memory limit missing: %s", joined)
	}
	// CPU-seconds now maps to a wall-time timeout wrapper (not --cpus,
	// which is a core quota). Check that the argv includes the timeout
	// invocation with the right seconds and --preserve-status.
	if strings.Contains(joined, "--cpus") {
		t.Fatalf("--cpus should no longer appear in container argv: %s", joined)
	}
	if !strings.Contains(joined, "timeout --preserve-status -s KILL 2") {
		t.Fatalf("timeout wrap missing: %s", joined)
	}
	if !strings.Contains(joined, "--read-only") {
		t.Fatalf("read-only missing: %s", joined)
	}
	if !strings.Contains(joined, "--cap-drop=ALL") {
		t.Fatalf("cap-drop=ALL missing: %s", joined)
	}
	if !strings.Contains(joined, "--security-opt no-new-privileges:true") {
		t.Fatalf("no-new-privileges missing: %s", joined)
	}
	if !strings.Contains(joined, "-v /work:/work:rw") {
		t.Fatalf("workspace mount missing: %s", joined)
	}
	if !strings.Contains(joined, "--user 65534:65534") {
		t.Fatalf("non-root user missing: %s", joined)
	}
}

func TestContainerBuildArgsNetworkAllowedUsesBridge(t *testing.T) {
	c := &Container{cfg: Config{AllowNetwork: true, ContainerImage: "alpine:latest"}, runtime: "podman", runtimePath: "/usr/local/bin/podman"}
	args, err := c.buildArgs("ping example.com", "alpine:latest", "/work")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--network bridge") {
		t.Fatalf("network bridge missing when allowed: %s", joined)
	}
	if strings.Contains(joined, "--network none") {
		t.Fatalf("network none should not appear when allowed: %s", joined)
	}
}

func TestContainerCapabilitiesIsolateAll(t *testing.T) {
	c := &Container{}
	caps := c.Capabilities()
	if !caps.NetworkIsolation || !caps.FilesystemIsolation || !caps.ResourceLimits {
		t.Fatalf("container caps should all be true: %+v", caps)
	}
}

func TestContainerRejectsEmptyCommandAndDir(t *testing.T) {
	c := &Container{cfg: Config{ContainerRuntime: "docker", ContainerImage: "alpine:latest"}, runtime: "docker", runtimePath: "/usr/local/bin/docker"}
	if _, err := c.Run(context.Background(), native.CommandRequest{Command: "", Dir: t.TempDir()}); err == nil {
		t.Fatal("expected error for empty command")
	}
	if _, err := c.Run(context.Background(), native.CommandRequest{Command: "echo hi", Dir: ""}); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestFromConfigCarriesAllowNetwork(t *testing.T) {
	sb := config.ShellToolConfig{
		AllowNetwork: true,
		Sandbox: config.SandboxConfig{
			Backend:       "container",
			MemoryLimitMB: 512,
			CPUSeconds:    4,
			MaxProcesses:  128,
			AllowFallback: true,
			EnvAllowlist:  []string{"PATH"},
		},
	}
	cfg := FromConfig(sb)
	if !cfg.AllowNetwork || cfg.Backend != "container" || cfg.MemoryLimitMB != 512 || cfg.CPUSeconds != 4 || !cfg.AllowFallback {
		t.Fatalf("FromConfig wrong: %+v", cfg)
	}
	if len(cfg.EnvAllowlist) != 1 || cfg.EnvAllowlist[0] != "PATH" {
		t.Fatalf("EnvAllowlist = %#v", cfg.EnvAllowlist)
	}
}

// TestMetaForReportsMemoryOnlyWhenEnforced: on platforms where the
// restricted backend cannot apply ulimit -v (darwin, windows), the audit
// metadata must report 0 rather than a phantom limit.
func TestMetaForReportsMemoryOnlyWhenEnforced(t *testing.T) {
	cfg := Config{Backend: "restricted", MemoryLimitMB: 64, CPUSeconds: 10}
	caps := Capabilities{Backend: "restricted"}
	meta := metaFor(caps, cfg)
	if ulimitSupportsMem() {
		if meta.MemoryLimitBytes != 64*1024*1024 {
			t.Fatalf("MemoryLimitBytes = %d, want 64MiB", meta.MemoryLimitBytes)
		}
		return
	}
	if meta.MemoryLimitBytes != 0 {
		t.Fatalf("MemoryLimitBytes = %d, want 0 (ulimit -v unsupported on this platform)", meta.MemoryLimitBytes)
	}
}

func TestSandboxMetaPassthroughRunsAndCapturesMeta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell echo differs on windows")
	}
	sb := newTestSandbox(t, Config{Backend: "passthrough", UnsafePassthrough: true})
	dir := t.TempDir()
	res, err := sb.Run(context.Background(), native.CommandRequest{
		Command: "echo hi",
		Dir:     dir,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hi") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if res.Meta.Backend != "passthrough" || !res.Meta.Enabled {
		t.Fatalf("meta = %+v", res.Meta)
	}
}
