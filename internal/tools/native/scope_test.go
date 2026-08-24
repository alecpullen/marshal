package native

import (
	"context"
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

// newToolSetForTest builds a toolSet through the real constructor with a
// throwaway workspace root, so tests exercise the production wiring
// (defaults, wsState fallback) rather than a hand-rolled struct.
func newToolSetForTest(t *testing.T, opts Options) *toolSet {
	t.Helper()
	if opts.WorkspaceRoot == "" {
		opts.WorkspaceRoot = t.TempDir()
	}
	if opts.CommandRunner == nil {
		opts.CommandRunner = &fakeRunner{}
	}
	ts, err := newToolSet(opts)
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	return ts
}

// WorkspaceState defaults to SessionState so every existing caller — the
// parent registry, the pipeline, the plan author — is unchanged.
func TestWorkspaceStateDefaultsToSessionState(t *testing.T) {
	st := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	ts := newToolSetForTest(t, Options{SessionState: st})
	if ts.wsState() != st {
		t.Fatal("WorkspaceState must default to SessionState when unset")
	}
}

func TestWorkspaceStateOverrideIsHonoured(t *testing.T) {
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	parent := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	ts := newToolSetForTest(t, Options{SessionState: child, WorkspaceState: parent})
	if ts.wsState() != parent {
		t.Fatal("WorkspaceState must win when set")
	}
	if ts.sessionState != child {
		t.Fatal("SessionState must remain the per-agent binding")
	}
}

// Backups are workspace scope: a child's edits must land where /undo looks.
func TestBackupsGoToWorkspaceState(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "app.go")
	orig := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filePath, []byte(orig), 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "filetrack.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ft := filetrack.New(database.SQLDB(), "test-session")

	child := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	parent := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  &fakeRunner{},
		FileTracker:    ft,
		SessionState:   child,
		WorkspaceState: parent,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// Read first to satisfy the stale-file contract.
	if _, err := invokeTool(t, reg, "file.read", `{"path":"app.go"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}

	newContent := "package main\n\nfunc main() { println(\"new\") }\n"
	argsJSON, err := json.Marshal(map[string]string{"path": "app.go", "content": newContent})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	if _, err := invokeTool(t, reg, "file.write", string(argsJSON)); err != nil {
		t.Fatalf("file.write: %v", err)
	}

	// The backup must land on the workspace state (parent), not the child.
	if !parent.HasBackup() {
		t.Fatal("expected a backup on the workspace state after file.write")
	}
	backup := parent.Backup()
	if len(backup) != 1 || backup[0].Path != "app.go" || backup[0].Content != orig || backup[0].Mode != 0755 {
		t.Fatalf("unexpected workspace backup: %#v", backup)
	}
	if child.HasBackup() {
		t.Fatal("child (per-agent) state must not hold the workspace backup")
	}
}

// Streamed shell output is per-agent: it must reach the child's active
// tool call, never the parent's. This is the reported bug.
func TestShellOutputStreamsToSessionStateNotWorkspace(t *testing.T) {
	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	parent := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	child.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "echo hi", StartedAt: time.Now()})
	parent.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run", Args: "echo hi", StartedAt: time.Now()})

	runner := &fakeRunner{
		result: CommandResult{ExitCode: 0, Stdout: "ok\n"},
	}
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  root,
		CommandRunner:  runner,
		Guardrail:      func(string) error { return nil },
		MaxOutputBytes: 100,
		SessionState:   child,
		WorkspaceState: parent,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"command":"echo ok"}`)})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if res.CommandExitCode == nil || *res.CommandExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", res.CommandExitCode)
	}

	childATC, _ := child.ActiveToolCall()
	if !strings.Contains(childATC.Output, "ok") {
		t.Fatalf("child active tool call output missing stream: %q", childATC.Output)
	}
	parentATC, _ := parent.ActiveToolCall()
	if strings.Contains(parentATC.Output, "ok") {
		t.Fatalf("parent active tool call must not receive the child's stream: %q", parentATC.Output)
	}
}
