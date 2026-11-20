package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// TestBackupStateFallsBackToWorkspaceState pins the default: every caller with
// one working tree keeps recording undo evidence where /undo looks for it, with
// no option to set.
func TestBackupStateFallsBackToWorkspaceState(t *testing.T) {
	ws := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	ts := newToolSetForTest(t, Options{WorkspaceState: ws})
	if got := ts.backupState(); got != ws {
		t.Fatal("backupState() must fall back to the workspace state when no backup owner is set")
	}
}

// TestBackupStatePrefersItsOwnOwner pins the one caller that needs a different
// owner: a shared-mode subagent child works in its own bound root but must leave
// its rollback evidence in the conversation's undo slot.
func TestBackupStatePrefersItsOwnOwner(t *testing.T) {
	workspace := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	owner := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	ts := newToolSetForTest(t, Options{WorkspaceState: workspace, BackupState: owner})
	if got := ts.backupState(); got != owner {
		t.Fatal("BackupState must win over the workspace state when set")
	}
	if ts.wsState() != workspace {
		t.Fatal("the workspace state must remain the tool binding when a backup owner differs")
	}
}

// TestFileWriteStoresUndoEvidenceOnTheBackupOwner is the end-to-end form of the
// same rule: a write committed by a toolset whose backup owner differs from its
// workspace state leaves /undo evidence on the backup owner and none on the
// workspace state.
func TestFileWriteStoresUndoEvidenceOnTheBackupOwner(t *testing.T) {
	dir := t.TempDir()

	workspace := session.New(config.Config{}, dir, time.Now(), session.Persistence{})
	owner := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  dir,
		SessionState:   workspace,
		WorkspaceState: workspace,
		BackupState:    owner,
		Config:         config.Config{},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := reg.Lookup("file.write")
	if !ok {
		t.Fatal("file.write not registered")
	}
	// A NEW file: committed evidence is still recorded, and no pre-existing
	// content needs the read-before-write ledger this fixture does not wire.
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "file.write",
		Args: []byte(`{"path":"probe.txt","content":"after"}`),
	}); err != nil {
		t.Fatalf("file.write: %v", err)
	}

	if !owner.HasBackup() {
		t.Fatal("the backup owner received no /undo evidence")
	}
	if workspace.HasBackup() {
		t.Fatal("the workspace state recorded /undo evidence that belongs to the backup owner")
	}
	backups := owner.Backup()
	if len(backups) != 1 || backups[0].Path != "probe.txt" {
		t.Fatalf("backup owner evidence = %+v, want one probe.txt entry", backups)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "probe.txt")); string(data) != "after" {
		t.Fatalf("probe.txt = %q, want after", data)
	}
}
