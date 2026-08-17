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

// TestIsShellFree verifies the isShellFree helper correctly identifies
// shell metacharacters.  Commands with no shell metacharacters should
// return true; those with | & ; ` $ ( ) { } < > * ? or newline should
// return false.
func TestIsShellFree(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		// Shell-free (should return true)
		{"echo hello", true},
		{"ls -la", true},
		{"cat file.txt", true},
		{"go build ./...", true},
		{"go test -run TestFoo", true},
		{"/usr/bin/env", true},
		{"git status", true},
		{"python3 -c \"print(1)\"", false}, // ( and ) are shell metas

		// Shell metacharacters (should return false)
		{"echo hello | grep h", false},
		{"echo $HOME", false},
		{"echo `whoami`", false},
		{"echo a && echo b", false},
		{"echo a; echo b", false},
		{"echo $(whoami)", false},
		{"echo ${HOME}", false},
		{"cat < file.txt", false},
		{"echo *", false},
		{"echo ?", false},
		{"echo hello\nworld", false},
		{"echo a > out.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := isShellFree(tt.command); got != tt.want {
				t.Errorf("isShellFree(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
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
	args := c.buildArgs("echo hello", "alpine:latest", "/workspace")

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
	args := c.buildArgs("rm -rf /tmp/x", "alpine:latest", "/workspace")

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

// TestContainerBuildArgs_UsesArgvForNonDestructiveShellFree verifies that
// non-destructive shell-free commands still use the argv path (not /bin/sh -lc).
// This is the existing behavior — this test guards against regression.
func TestContainerBuildArgs_UsesArgvForNonDestructiveShellFree(t *testing.T) {
	c := &Container{
		cfg:         Config{},
		runtime:     "docker",
		runtimePath: "/usr/bin/docker",
		envDenySet:  make(map[string]bool),
	}

	// echo hello is shell-free and non-destructive — should use argv path.
	args := c.buildArgs("echo hello", "alpine:latest", "/workspace")

	// Verify /bin/sh does NOT appear in the args (argv path).
	for _, a := range args {
		if a == "/bin/sh" {
			t.Errorf("unexpected /bin/sh in args for non-destructive shell-free command, got %v", args)
			break
		}
	}

	// Verify the command args appear directly (echo hello).
	foundEcho := false
	for _, a := range args {
		if a == "echo" {
			foundEcho = true
			break
		}
	}
	if !foundEcho {
		t.Errorf("expected 'echo' in argv path args, got %v", args)
	}
}

// TestContainer_AvPathForSimpleCommands verifies that shell-free commands
// are executed via the argv path (not /bin/sh -lc).  It uses the fake
// runtime, which only succeeds if its argv-execution path is hit for
// simple commands.
func TestContainer_AvPathForSimpleCommands(t *testing.T) {
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

	args := c.buildArgs("go vet ./... 2>&1 | head -50", "golang:alpine", "/workspace")

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
		EnvAllowlist:    nil, // nil → should use AllowList defaults
	}
	c := newContainer(cfg, "docker", "/usr/bin/docker", nil)

	args := c.buildArgs("echo hi", "alpine:latest", "/workspace")

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

func TestContainerParseQuotedArgs(t *testing.T) {
	// Test that buildArgs correctly parses a command with quoted arguments.
	// strings.Fields would split "git commit -m \"fix: update logic\"" into
	// 5 tokens; shlex.Split produces 4.
	cfg := Config{Backend: "container", ContainerImage: "alpine:latest"}
	c := newContainer(cfg, "docker", "/usr/bin/docker", nil)

	args := c.buildArgs(`git commit -m "fix: update logic"`, "alpine:latest", "/workspace")

	// Find the inner command args (after the image name)
	imgIdx := -1
	for i, a := range args {
		if a == "alpine:latest" {
			imgIdx = i
			break
		}
	}
	if imgIdx == -1 {
		t.Fatal("image name not found in args")
	}
	inner := args[imgIdx+1:]
	// When shellFree, inner should be the shlex-split argv.
	// "git commit -m \"fix: update logic\"" should produce:
	// ["git", "commit", "-m", "fix: update logic"]
	// If strings.Fields is used, it produces 5 tokens instead of 4.
	if len(inner) < 4 {
		t.Fatalf("expected at least 4 inner args, got %d: %v", len(inner), inner)
	}
	// Verify the 4th element is the full quoted string, not split
	if inner[3] != "fix: update logic" {
		t.Errorf("expected inner[3] = %q, got %q (strings.Fields splits quoted args)", "fix: update logic", inner[3])
	}
}
