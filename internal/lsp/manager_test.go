package lsp

import (
	"os"
	"path/filepath"
	"testing"
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
