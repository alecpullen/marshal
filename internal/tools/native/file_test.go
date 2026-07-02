package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestFileReadReadsWholeFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "one\ntwo\nthree\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"README.md"}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Content != "one\ntwo\nthree\n" {
		t.Fatalf("Content = %q", result.Content)
	}
	if !strings.Contains(result.Summary, "README.md") {
		t.Fatalf("Summary = %q, want path", result.Summary)
	}
}

func TestFileReadReadsLineRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\nfour\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"notes.txt","start_line":2,"end_line":3}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Content != "two\nthree" {
		t.Fatalf("Content = %q, want selected lines", result.Content)
	}
}

func TestFileReadRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.read", `{"path":"../secret.txt"}`)
	if err == nil {
		t.Fatal("file.read traversal returned nil error")
	}
}

func TestFileReadRejectsInvalidRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.read", `{"path":"notes.txt","start_line":3,"end_line":2}`)
	if err == nil {
		t.Fatal("file.read invalid range returned nil error")
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
