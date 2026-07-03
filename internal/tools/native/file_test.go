package native

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
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

func TestFileWritePatchTool(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	args := `{"patch": "File: app.go\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"patched\")\n>>>>>>> REPLACE"}`
	res, err := invokeTool(t, reg, "file.write_patch", args)
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	if res.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !reflect.DeepEqual(res.FilesChanged, []string{"app.go"}) {
		t.Fatalf("FilesChanged = %#v, want %#v", res.FilesChanged, []string{"app.go"})
	}

	// Verify file was patched
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if !strings.Contains(string(data), "println(\"patched\")") {
		t.Fatalf("file content not patched: %s", string(data))
	}
}

func TestFileWritePatchRollbackIntegration(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\r\n\r\nfunc main() {\r\n\tprintln(\"hello\")\r\n}\r\n"
	if err := os.WriteFile(filePath, []byte(orig), 0755); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	args := `{"patch": "File: app.go\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"patched\")\n>>>>>>> REPLACE"}`
	_, err := invokeTool(t, reg, "file.write_patch", args)
	if err != nil {
		t.Fatalf("write_patch failed: %v", err)
	}

	// 1. Verify backup was saved in session state
	if !state.HasBackup() {
		t.Fatal("expected session state to contain backup after patch")
	}

	backup := state.Backup()
	if len(backup) != 1 || backup[0].Path != "app.go" || backup[0].Content != orig || backup[0].Mode != 0755 {
		t.Fatalf("unexpected backup contents/mode: %#v", backup)
	}

	// 2. Verify line endings were preserved as CRLF
	patchedData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read patched file: %v", err)
	}
	if !strings.Contains(string(patchedData), "\r\n") {
		t.Fatal("expected CRLF line endings to be preserved in patched file")
	}

	// 3. Perform Rollback
	err = state.RollbackBackup()
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	// 4. Verify file reverted completely including permissions and CRLF
	revertedData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read reverted file: %v", err)
	}
	if string(revertedData) != orig {
		t.Fatalf("reverted file content mismatch: got %q, want %q", string(revertedData), orig)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed to stat reverted file: %v", err)
	}
	if info.Mode() != 0755 {
		t.Fatalf("expected reverted permissions to be 0755, got %v", info.Mode())
	}
}
