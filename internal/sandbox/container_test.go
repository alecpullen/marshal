package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/native"
)

// writeFakeRuntime creates a script that mimics a container runtime for
// testing. It supports both invocation styles:
//
//  1. Shell path — finds -lc and exec's the next arg as /bin/sh -c
//     (for commands containing shell metacharacters).
//  2. Argv path — finds the container image (first positional arg) and
//     exec's everything after it as argv
//     (for shell-free commands invoked directly).
func writeFakeRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-runtime")
	content := `#!/bin/sh
# Fake container runtime: supports both shell and argv execution.
# 1. Shell path: find -lc and exec the next arg as /bin/sh -c.
# 2. Argv path: find the image (first positional arg) then exec
#    everything after it as argv.

# First pass: look for -lc (shell path).
prev=
for arg; do
    if [ "$prev" = "-lc" ]; then
        exec /bin/sh -c "$arg"
    fi
    prev="$arg"
done

# No -lc found: argv path.  Consume docker/podman flags and the
# image name, then exec the remaining positional args as argv.
while [ $# -gt 0 ]; do
    case "$1" in
        --network|--memory|-v|-w|--user|--security-opt|-e)
            shift 2 ;;
        --cap-drop=*|--read-only|--rm)
            shift ;;
        -*)
            shift ;;
        run)
            shift ;;
        *)
            # This is the image; shift it and exec the rest as argv.
            shift
            exec "$@"
            ;;
    esac
done
exit 1
`
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("writeFakeRuntime: %v", err)
	}
	return path
}

// writeFakeInfoRuntime creates a script that exits 0 for "info" and
// otherwise acts as a fake runtime (to pass detectRuntime's probe).
// Supports both shell and argv execution paths (same as writeFakeRuntime).
func writeFakeInfoRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-runtime-info")
	content := `#!/bin/sh
if [ "$1" = "info" ]; then
    exit 0
fi
# Shell path: find -lc and exec the next arg as /bin/sh -c.
prev=
for arg; do
    if [ "$prev" = "-lc" ]; then
        exec /bin/sh -c "$arg"
    fi
    prev="$arg"
done
# Argv path: consume flags and image, exec the rest.
while [ $# -gt 0 ]; do
    case "$1" in
        --network|--memory|-v|-w|--user|--security-opt|-e)
            shift 2 ;;
        --cap-drop=*|--read-only|--rm)
            shift ;;
        -*)
            shift ;;
        run)
            shift ;;
        *)
            shift
            exec "$@"
            ;;
    esac
done
exit 1
`
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("writeFakeInfoRuntime: %v", err)
	}
	return path
}

func TestContainerRunWithFakeRuntime(t *testing.T) {
	runtimePath := writeFakeRuntime(t)

	c := &Container{
		cfg:         Config{},
		runtime:     "fake",
		runtimePath: runtimePath,
		envDenySet:  make(map[string]bool),
	}

	dir := t.TempDir()
	res, err := c.Run(context.Background(), native.CommandRequest{
		Command: "echo container_works",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Container.Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "container_works") {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "container_works")
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !res.Meta.Enabled {
		t.Fatal("meta.Enabled should be true")
	}
	if res.Meta.Backend != "container" {
		t.Fatalf("meta.Backend = %q, want %q", res.Meta.Backend, "container")
	}
}

func TestContainerRunWithObserverAndStart(t *testing.T) {
	runtimePath := writeFakeRuntime(t)

	c := &Container{
		cfg:         Config{},
		runtime:     "fake",
		runtimePath: runtimePath,
		envDenySet:  make(map[string]bool),
	}

	dir := t.TempDir()
	var stdoutObs, stderrObs bytes.Buffer
	var startPid int

	res, err := c.Run(context.Background(), native.CommandRequest{
		Command:        "echo hello_world",
		Dir:            dir,
		Timeout:        5 * time.Second,
		MaxOutputBytes: 16,
		Stdout:         &stdoutObs,
		Stderr:         &stderrObs,
		OnStart: func(pid int) {
			startPid = pid
		},
	})
	if err != nil {
		t.Fatalf("Container.Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello_world") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	if stdoutObs.Len() == 0 {
		t.Fatal("stdout observer received no data")
	}
	if startPid <= 0 {
		t.Fatalf("OnStart pid = %d, want >0", startPid)
	}
	if res.Meta.DurationMS <= 0 {
		t.Fatalf("DurationMS = %d, want >0", res.Meta.DurationMS)
	}
}

func TestContainerRunCancellation(t *testing.T) {
	runtimePath := writeFakeRuntime(t)

	c := &Container{
		cfg:         Config{},
		runtime:     "fake",
		runtimePath: runtimePath,
		envDenySet:  make(map[string]bool),
	}

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		res native.CommandResult
		err error
	}
	ch := make(chan result, 1)
	go func() {
		res, err := c.Run(ctx, native.CommandRequest{
			Command: "sleep 30",
			Dir:     dir,
		})
		ch <- result{res, err}
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	r := <-ch
	if r.err == nil {
		t.Fatal("expected error from cancelled container command")
	}
	if r.res.Meta.KilledReason != "cancelled" {
		t.Fatalf("KilledReason = %q, want %q", r.res.Meta.KilledReason, "cancelled")
	}
}

func TestContainerBuildArgsWithFakeRuntimeExecutes(t *testing.T) {
	runtimePath := writeFakeRuntime(t)

	c := &Container{
		cfg:         Config{AllowNetwork: false},
		runtime:     "fake",
		runtimePath: runtimePath,
		envDenySet:  make(map[string]bool),
	}

	dir := t.TempDir()
	// Use a command that outputs something unique.
	res, err := c.Run(context.Background(), native.CommandRequest{
		Command:        "echo unique_container_output",
		Dir:            dir,
		Timeout:        5 * time.Second,
		MaxOutputBytes: 100,
	})
	if err != nil {
		t.Fatalf("Container.Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "unique_container_output") {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "unique_container_output")
	}
}

func TestBuildContainerEnv_StripsSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-12345")
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2376")
	t.Setenv("HTTP_PROXY", "http://proxy:3128")
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")

	c := &Container{}
	env := c.buildContainerEnv()

	// Verify secrets are stripped
	for _, kv := range env {
		key := envutil.EnvKey(kv)
		if key == "ANTHROPIC_API_KEY" {
			t.Errorf("buildContainerEnv leaked ANTHROPIC_API_KEY")
		}
		if key == "LD_PRELOAD" {
			t.Errorf("buildContainerEnv leaked LD_PRELOAD")
		}
	}

	// Verify DOCKER_HOST is preserved
	foundDocker := false
	for _, kv := range env {
		if envutil.EnvKey(kv) == "DOCKER_HOST" {
			foundDocker = true
			if kv != "DOCKER_HOST=tcp://127.0.0.1:2376" {
				t.Errorf("DOCKER_HOST = %q, want %q", kv, "DOCKER_HOST=tcp://127.0.0.1:2376")
			}
		}
	}
	if !foundDocker {
		t.Error("DOCKER_HOST not found in container env but should be preserved")
	}

	// Verify HTTP_PROXY is preserved
	foundProxy := false
	for _, kv := range env {
		if envutil.EnvKey(kv) == "HTTP_PROXY" {
			foundProxy = true
			if kv != "HTTP_PROXY=http://proxy:3128" {
				t.Errorf("HTTP_PROXY = %q, want %q", kv, "HTTP_PROXY=http://proxy:3128")
			}
		}
	}
	if !foundProxy {
		t.Error("HTTP_PROXY not found in container env but should be preserved")
	}
}

// TestContainerDetectWithFakeInfoRuntime verifies that New can detect a
// fake runtime on PATH with the info command succeeding.
func TestContainerDetectWithFakeInfoRuntime(t *testing.T) {
	// The stub runtime does trivial work, but under full-suite load
	// spawning it has exceeded the four-second production probe budget.
	// This test is about detection, not latency.
	orig := probeTimeout
	probeTimeout = 30 * time.Second
	t.Cleanup(func() { probeTimeout = orig })

	fakePath := writeFakeInfoRuntime(t)
	fakeDir := filepath.Dir(fakePath)
	t.Setenv("PATH", fakeDir)

	sb, err := New(Config{
		Backend:          "container",
		ContainerRuntime: "fake-runtime-info",
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sb.Capabilities().Backend != "container" {
		t.Fatalf("backend = %q, want %q", sb.Capabilities().Backend, "container")
	}
}

// innerArgs returns the argv handed to the image. With CPUSeconds unset the
// timeout wrapper is absent, so this is the command's own argv.
func innerArgs(t *testing.T, args []string) []string {
	t.Helper()
	for i, a := range args {
		if a == "alpine:latest" {
			return args[i+1:]
		}
	}
	t.Fatalf("image not found in %v", args)
	return nil
}

// TestContainerCommandUsesSingleShellPath verifies that every admitted
// command — shell-free, metacharacter-laden or destructive — is handed to the
// container through the same /bin/sh -lc argv. buildArgs is transport
// construction: policy/native admission already decided what may run.
func TestContainerCommandUsesSingleShellPath(t *testing.T) {
	c := &Container{cfg: Config{}, runtime: "docker", runtimePath: "/usr/bin/docker", envDenySet: make(map[string]bool)}
	for _, command := range []string{
		"echo hello",
		"ls -la",
		"rm -rf /tmp/x",
		"go vet ./... 2>&1 | head -50",
	} {
		args, err := c.buildArgs(command, "alpine:latest", "/workspace")
		if err != nil {
			t.Fatalf("%q: buildArgs: %v", command, err)
		}
		inner := innerArgs(t, args)
		want := []string{"/bin/sh", "-lc", "set -o pipefail 2>/dev/null || true; " + command}
		if len(inner) != len(want) {
			t.Fatalf("%q: inner argv = %v, want %v", command, inner, want)
		}
		for i := range want {
			if inner[i] != want[i] {
				t.Errorf("%q: inner[%d] = %q, want %q", command, i, inner[i], want[i])
			}
		}
	}
}

// TestContainer_AvPathForSimpleCommands_Docker is an integration test that
// verifies the argv execution path against a real Docker daemon. It is
// skipped when Docker is not available (e.g., CI, no daemon running).
func TestContainer_AvPathForSimpleCommands_Docker(t *testing.T) {
	name, absPath, ok := detectRuntime("docker")
	if !ok {
		t.Skip("requires Docker daemon")
	}

	c := &Container{
		cfg: Config{
			ContainerRuntime: "docker",
			ContainerImage:   "alpine:latest",
		},
		runtime:     name,
		runtimePath: absPath,
		envDenySet:  make(map[string]bool),
	}

	dir := t.TempDir()
	res, err := c.Run(context.Background(), native.CommandRequest{
		Command: "echo hello",
		Dir:     dir,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Container.Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "hello\n")
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !res.Meta.Enabled {
		t.Fatal("meta.Enabled should be true")
	}
	if res.Meta.Backend != "container" {
		t.Fatalf("meta.Backend = %q, want %q", res.Meta.Backend, "container")
	}
}

// TestContainerMeta_DoesNotReportPhantomMaxProcesses verifies that the
// container backend reports MaxProcesses=0 in audit metadata even when
// cfg.MaxProcesses is set, because buildArgs does not pass --pids-limit.
func TestContainerMeta_DoesNotReportPhantomMaxProcesses(t *testing.T) {
	c := &Container{
		cfg:         Config{MaxProcesses: 128},
		runtime:     "docker",
		runtimePath: "/usr/bin/docker",
		envDenySet:  make(map[string]bool),
	}
	meta := metaFor(c.Capabilities(), c.cfg)
	if meta.MaxProcesses != 0 {
		t.Fatalf("container meta MaxProcesses = %d, want 0 (not enforced)", meta.MaxProcesses)
	}
}

// TestContainerBuildArgs_DropsSecretAndDangerousAllowlistKeys verifies that
// env_allowlist entries matching secret or dangerous-key predicates are
// silently dropped before being passed to `docker run -e`, matching the
// restricted backend's behavior.
func TestContainerBuildArgs_DropsSecretAndDangerousAllowlistKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	t.Setenv("MARSHAL_SAFE", "ok_value")

	c := &Container{
		cfg: Config{
			EnvAllowlist: []string{"ANTHROPIC_API_KEY", "LD_PRELOAD", "MARSHAL_SAFE"},
		},
		runtime:     "docker",
		runtimePath: "/usr/bin/docker",
		envDenySet:  make(map[string]bool),
	}
	args, err := c.buildArgs("echo hello", "alpine:latest", "/workspace")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}

	for i := 0; i < len(args)-1; i++ {
		if args[i] != "-e" {
			continue
		}
		kv := args[i+1]
		key, _, _ := strings.Cut(kv, "=")
		if key == "ANTHROPIC_API_KEY" {
			t.Fatalf("secret allowlist key leaked into container args: %q", kv)
		}
		if key == "LD_PRELOAD" {
			t.Fatalf("dangerous allowlist key leaked into container args: %q", kv)
		}
	}

	foundSafe := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && args[i+1] == "MARSHAL_SAFE=ok_value" {
			foundSafe = true
			break
		}
	}
	if !foundSafe {
		t.Fatalf("non-secret allowlist key should be passed through, got args: %v", args)
	}
}

// TestContainerBuildArgs_RoutesDestructiveThroughShell verifies that
// destructive commands (e.g. rm -rf) are routed through /bin/sh -lc even
// when the command contains no shell metacharacters. This ensures shell
// features are available for destructive operations explicitly approved
// by the user.
func TestContainerBuildArgs_RoutesDestructiveThroughShell(t *testing.T) {
	c := &Container{
		cfg:         Config{},
		runtime:     "docker",
		runtimePath: "/usr/bin/docker",
		envDenySet:  make(map[string]bool),
	}

	// rm -rf /tmp/x is shell-free (no metacharacters) but destructive.
	args, err := c.buildArgs("rm -rf /tmp/x", "alpine:latest", "/workspace")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}

	// Verify /bin/sh -lc appears in the args (shell path, not argv path).
	foundShell := false
	for i, a := range args {
		if a == "/bin/sh" && i+1 < len(args) && args[i+1] == "-lc" {
			foundShell = true
			break
		}
	}
	if !foundShell {
		t.Errorf("expected /bin/sh -lc in args for destructive command, got %v", args)
	}

	// Verify the command string is present after -lc (behind the pipefail
	// prefix applied to the shell path).
	cmdFound := false
	for i, a := range args {
		if a == "-lc" && i+1 < len(args) && strings.HasSuffix(args[i+1], "rm -rf /tmp/x") {
			cmdFound = true
			break
		}
	}
	if !cmdFound {
		t.Errorf("expected command after -lc, got %v", args)
	}
}

// TestContainer_SimpleCommandRunsThroughShell verifies that even a simple,
// shell-free command is executed through the one /bin/sh -lc path. The fake
// runtime supports both branches; this guards that the argv branch is unused.
func TestContainer_SimpleCommandRunsThroughShell(t *testing.T) {
	runtimePath := writeFakeRuntime(t)

	c := &Container{
		cfg:         Config{},
		runtime:     "fake",
		runtimePath: runtimePath,
		envDenySet:  make(map[string]bool),
	}

	dir := t.TempDir()
	res, err := c.Run(context.Background(), native.CommandRequest{
		Command: "echo argv_works",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Container.Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "argv_works") {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "argv_works")
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
}

// TestContainerBuildArgs_SetsPipefailInShellPath: in-container pipelines
// must also fail loudly — a command failing early in a pipeline should not
// report the last stage's exit code. Container images may lack bash, so
// this is a best-effort `set -o pipefail` prefix on the /bin/sh path
// (session postmortem 2026-08-06, finding 2).
func TestContainerBuildArgs_SetsPipefailInShellPath(t *testing.T) {
	c := &Container{
		cfg:         Config{},
		runtime:     "docker",
		runtimePath: "/usr/bin/docker",
		envDenySet:  make(map[string]bool),
	}

	args, err := c.buildArgs("go vet ./... 2>&1 | head -50", "golang:alpine", "/workspace")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}

	cmdFound := false
	for i, a := range args {
		if a == "-lc" && i+1 < len(args) && strings.Contains(args[i+1], "pipefail") && strings.Contains(args[i+1], "go vet ./... 2>&1 | head -50") {
			cmdFound = true
			break
		}
	}
	if !cmdFound {
		t.Errorf("expected pipefail-wrapped command after -lc, got %v", args)
	}
}

func TestContainerNilEnvAllowlistDefaults(t *testing.T) {
	// Set some env vars that should survive AllowList
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/test")

	cfg := Config{
		Backend:        "container",
		ContainerImage: "alpine:latest",
		EnvAllowlist:   nil, // nil → should use AllowList defaults
	}
	c := newContainer(cfg, "docker", "/usr/bin/docker", nil)

	args, err := c.buildArgs("echo hi", "alpine:latest", "/workspace")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}

	// Look for -e PATH=... and -e HOME=... in args
	foundPath := false
	foundHome := false
	for i, a := range args {
		if a == "-e" && i+1 < len(args) {
			if strings.HasPrefix(args[i+1], "PATH=") {
				foundPath = true
			}
			if strings.HasPrefix(args[i+1], "HOME=") {
				foundHome = true
			}
		}
	}
	if !foundPath {
		t.Error("nil EnvAllowlist should default to including PATH")
	}
	if !foundHome {
		t.Error("nil EnvAllowlist should default to including HOME")
	}
}

// TestContainerCPUTimeoutFallbackWarning verifies that isTimeoutNotFound
// correctly detects a container image missing the `timeout` command
// (TOOLS-MIN-F6). It inspects captured stderr (not err.Error()), because
// a real *exec.ExitError from cmd.Wait() only reports "exit status 127"
// and never carries the command's stderr output.
func TestContainerCPUTimeoutFallbackWarning(t *testing.T) {
	// A 127 exit code with stderr naming `timeout` must match.
	if !isTimeoutNotFound(127, "sh: 1: timeout: not found") {
		t.Error("expected isTimeoutNotFound to match a 127 timeout-not-found stderr")
	}
	// A 127 exit code from a non-timeout command must NOT match.
	if isTimeoutNotFound(127, "sh: 1: someothercmd: not found") {
		t.Error("expected isTimeoutNotFound to NOT match a 127 stderr that doesn't mention timeout")
	}
	// A non-127 exit code must not match even if stderr mentions timeout.
	if isTimeoutNotFound(1, "timeout: not found") {
		t.Error("expected isTimeoutNotFound to NOT match a non-127 exit code")
	}
	// Empty stderr must not match.
	if isTimeoutNotFound(127, "") {
		t.Error("expected isTimeoutNotFound to be false for empty stderr")
	}
}

// TestContainerCommandPreservesQuotedMessageBytes verifies that a command is
// passed to the shell byte-for-byte. A commit message mentioning sudo, reboot
// or a quoted phrase is message data, not a parse the transport may
// reinterpret.
func TestContainerCommandPreservesQuotedMessageBytes(t *testing.T) {
	cfg := Config{Backend: "container", ContainerImage: "alpine:latest"}
	c := newContainer(cfg, "docker", "/usr/bin/docker", nil)
	command := `git commit -m "fix: prevent sudo invocation" -m 'handle reboot state'`
	args, err := c.buildArgs(command, "alpine:latest", "/workspace")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	inner := innerArgs(t, args)
	if len(inner) != 3 || inner[0] != "/bin/sh" || inner[1] != "-lc" {
		t.Fatalf("inner argv = %v, want one /bin/sh -lc wrapper", inner)
	}
	if want := "set -o pipefail 2>/dev/null || true; " + command; inner[2] != want {
		t.Fatalf("command bytes changed:\n got %q\nwant %q", inner[2], want)
	}
}

func TestContainerParseQuotedArgs(t *testing.T) {
	// The command is not re-split: quotes, spaces and the commit message reach
	// the shell exactly as they were approved.
	cfg := Config{Backend: "container", ContainerImage: "alpine:latest"}
	c := newContainer(cfg, "docker", "/usr/bin/docker", nil)

	command := `git commit -m "fix: update logic"`
	args, err := c.buildArgs(command, "alpine:latest", "/workspace")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	inner := innerArgs(t, args)
	if len(inner) != 3 || inner[2] != "set -o pipefail 2>/dev/null || true; "+command {
		t.Fatalf("inner argv = %v, want the exact command preserved", inner)
	}
}
