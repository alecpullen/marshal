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
