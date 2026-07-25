package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectServersOnPath(t *testing.T) {
	dir := t.TempDir()
	// Create a stub "gopls" executable on a temp PATH.
	stub := filepath.Join(dir, "gopls")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got := DetectServers(map[string]ServerSpec{}, map[string]bool{})
	if _, ok := got["go"]; !ok {
		t.Fatalf("expected go server detected on PATH, got %#v", got)
	}

	// Disabled language is excluded even when present.
	got = DetectServers(map[string]ServerSpec{}, map[string]bool{"go": true})
	if _, ok := got["go"]; ok {
		t.Fatal("disabled go should not be detected")
	}

	// Explicit config override for a language whose binary is not on PATH is
	// still included (user asked for it).
	got = DetectServers(map[string]ServerSpec{"python": {Command: "pyright-langserver", Args: []string{"--stdio"}}}, map[string]bool{})
	if _, ok := got["python"]; !ok {
		t.Fatal("configured python server should be included")
	}
}

// TestRunStartsServersInParallel verifies that Manager.Run starts each server
// in its own goroutine so a hung Initialize does not block other languages.
func TestRunStartsServersInParallel(t *testing.T) {
	dir := t.TempDir()

	// A stub that sleeps for 60s then exits — simulates a hung server.
	hung := filepath.Join(dir, "hung.sh")
	if err := os.WriteFile(hung, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A stub that exits immediately — Initialize will fail quickly.
	quick := filepath.Join(dir, "quick.sh")
	if err := os.WriteFile(quick, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	servers := map[string]ServerSpec{
		"hung":  {Command: hung},
		"quick": {Command: quick},
	}
	m := NewManager(dir, servers, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run should return within ~3s (2s ctx timeout + small overhead).
	// If servers were started sequentially, the hung server's 60s sleep
	// would block the quick server and Run would not return in time.
	start := time.Now()
	err := m.Run(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v, expected <5s (sequential start would block)", elapsed)
	}

	// Neither server should be ready (both fail Initialize for different reasons).
	if _, ok := m.ServerFor("hung"); ok {
		t.Error("hung server should not be ready")
	}
	if _, ok := m.ServerFor("quick"); ok {
		t.Error("quick server should not be ready")
	}
}
