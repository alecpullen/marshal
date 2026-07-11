package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/tools/native"
)

// writeFakeRuntime creates a script that mimics a container runtime for
// testing. It ignores all docker/podman flags and executes the argument
// that follows "-lc" as a shell command.
func writeFakeRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-runtime")
	content := `#!/bin/sh
# Fake container runtime: find -lc and exec the next argument.
found=0
for arg; do
    if [ "$found" = "1" ]; then
        exec /bin/sh -c "$arg"
    fi
    if [ "$arg" = "-lc" ]; then
        found=1
    fi
done
# If we get here, no -lc found; exit with error.
exit 1
`
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("writeFakeRuntime: %v", err)
	}
	return path
}

// writeFakeInfoRuntime creates a script that exits 0 for "info" and
// otherwise acts as a fake runtime (to pass detectRuntime's probe).
func writeFakeInfoRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-runtime-info")
	content := `#!/bin/sh
if [ "$1" = "info" ]; then
    exit 0
fi
# Otherwise, find -lc and exec the next argument.
found=0
for arg; do
    if [ "$found" = "1" ]; then
        exec /bin/sh -c "$arg"
    fi
    if [ "$arg" = "-lc" ]; then
        found=1
    fi
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
