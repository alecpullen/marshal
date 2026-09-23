// internal/tools/native/system_access_file_test.go — file-tool system-mode
// tests: absolute paths resolve out of root only with system access on.
package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/tools/registry"
)

// systemModeToolset builds a toolset rooted at a temp dir with a tracker and
// a session whose system-access flag starts off.
func systemModeToolset(t *testing.T) (*registry.Registry, *session.State, string) {
	t.Helper()
	root := t.TempDir()

	database, err := db.Open(filepath.Join(t.TempDir(), "filetrack.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: &fakeRunner{},
		FileTracker:   filetrack.New(database.SQLDB(), "test-session"),
		SessionState:  state,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg, state, root
}

// outOfRootPath returns a symlink-resolved absolute path in a fresh temp dir
// outside the workspace root. Resolving first matters on macOS, where /var is
// a symlink to /private/var and the resolver returns the resolved form.
func outOfRootPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return filepath.Join(dir, name)
}

// TestFileReadAbsoluteSystemMode pins that file.read reaches an out-of-root
// absolute path only with system access on.
func TestFileReadAbsoluteSystemMode(t *testing.T) {
	reg, state, _ := systemModeToolset(t)

	outside := outOfRootPath(t, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside body"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	args, err := json.Marshal(map[string]string{"path": outside})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.read", string(args)); err == nil {
		t.Fatal("file.read of an out-of-root absolute path with the flag off = nil error, want rejection")
	}

	state.SetSystemAccess(true)
	res, err := invokeTool(t, reg, "file.read", string(args))
	if err != nil {
		t.Fatalf("file.read with system access: %v", err)
	}
	if !strings.Contains(res.Content, "outside body") {
		t.Fatalf("Content = %q, want the out-of-root file body", res.Content)
	}
}

// TestFileWriteAbsoluteSystemMode pins that file.write creates an out-of-root
// absolute path only with system access on, and that the write is tracked.
func TestFileWriteAbsoluteSystemMode(t *testing.T) {
	reg, state, _ := systemModeToolset(t)

	outside := outOfRootPath(t, "report.json")

	args, err := json.Marshal(map[string]string{"path": outside, "content": `{"ok":true}`})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write", string(args)); err == nil {
		t.Fatal("file.write to an out-of-root absolute path with the flag off = nil error, want rejection")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("out-of-root file exists after a rejected write: %v", err)
	}

	state.SetSystemAccess(true)
	if _, err := invokeTool(t, reg, "file.write", string(args)); err != nil {
		t.Fatalf("file.write with system access: %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("content = %q, want the written body", string(data))
	}
}

// TestFileWritePatchAbsoluteSystemMode pins that file.write_patch applies to
// an out-of-root absolute path only with system access on.
func TestFileWritePatchAbsoluteSystemMode(t *testing.T) {
	reg, state, _ := systemModeToolset(t)

	outside := outOfRootPath(t, "report.json")
	if err := os.WriteFile(outside, []byte("{\n  \"status\": \"pending\"\n}\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	patchText := "File: " + outside + "\n<<<<<<< SEARCH\n  \"status\": \"pending\"\n=======\n  \"status\": \"patched\"\n>>>>>>> REPLACE"
	args, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write_patch", string(args)); err == nil {
		t.Fatal("file.write_patch on an out-of-root absolute path with the flag off = nil error, want rejection")
	}

	state.SetSystemAccess(true)
	// The stale-file contract requires a read first.
	readArgs, err := json.Marshal(map[string]string{"path": outside})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.read", string(readArgs)); err != nil {
		t.Fatalf("file.read with system access: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write_patch", string(args)); err != nil {
		t.Fatalf("file.write_patch with system access: %v", err)
	}

	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read patched file: %v", err)
	}
	if !strings.Contains(string(data), "\"status\": \"patched\"") {
		t.Fatalf("patched content = %q, want the replacement", string(data))
	}
}
