package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
)

// TestRollbackPartialFailureKeepsBackup pins U-01, the worst of the audit
// findings: the backup used to be cleared before any write and the loop
// returned on the first error, so a mid-loop failure left a half-reverted
// workspace with the backup already destroyed — unrecoverable — while the
// caller reported success.
func TestRollbackPartialFailureKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	s := New(config.Config{}, dir, time.Now(), Persistence{})

	// "blocked" is a directory, so writing a file over it fails.
	if err := os.MkdirAll(filepath.Join(dir, "blocked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.StoreBackup([]BackupFile{
		{Path: "ok.go", Content: "original", Mode: 0o644, Exists: true},
		{Path: "blocked", Content: "original", Mode: 0o644, Exists: true},
	})

	err := s.RollbackBackup()
	if err == nil {
		t.Fatal("partial rollback reported success")
	}

	// The error must name what failed, not just say "error".
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error does not name the failed file: %v", err)
	}

	// Retryability is the point: the backup must survive a partial failure.
	if !s.HasBackup() {
		t.Error("backup was destroyed by a failed rollback — the rollback is now unrecoverable")
	}

	// Files that could be restored still should have been, rather than the
	// loop aborting and stranding them.
	got, readErr := os.ReadFile(filepath.Join(dir, "ok.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "original" {
		t.Errorf("restorable file left unrestored: %q", got)
	}

	// The agent must be told, or it keeps reasoning about a workspace that
	// does not exist.
	var told bool
	for _, m := range s.Messages() {
		if strings.Contains(m.Content, "FAILED") && strings.Contains(m.Content, "blocked") {
			told = true
		}
	}
	if !told {
		t.Error("no transcript notice describing the partial rollback")
	}
}

// TestRollbackRemovesNewlyCreatedFiles pins the behavior that a file created
// by a write (backed up with Exists=false) is removed on rollback rather than
// restored as empty content.
func TestRollbackRemovesNewlyCreatedFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(config.Config{}, dir, time.Now(), Persistence{})

	// A pre-existing file and a newly created file in the same backup.
	if err := os.WriteFile(filepath.Join(dir, "existing.go"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.StoreBackup([]BackupFile{
		{Path: "existing.go", Content: "original", Mode: 0o644, Exists: true},
		{Path: "new.go", Content: "", Mode: 0o644, Exists: false},
	})

	if err := s.RollbackBackup(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// The pre-existing file is restored to its original content.
	got, err := os.ReadFile(filepath.Join(dir, "existing.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("existing file = %q, want %q", got, "original")
	}

	// The newly created file is removed entirely.
	if _, err := os.Stat(filepath.Join(dir, "new.go")); !os.IsNotExist(err) {
		t.Errorf("newly created file should have been removed on rollback, stat err = %v", err)
	}
}

// TestRollbackSuccessClearsBackup pins the happy path: the backup is consumed
// only once every file is safely back.
func TestRollbackSuccessClearsBackup(t *testing.T) {
	dir := t.TempDir()
	s := New(config.Config{}, dir, time.Now(), Persistence{})

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.StoreBackup([]BackupFile{{Path: "a.go", Content: "original", Mode: 0o644, Exists: true}})

	if err := s.RollbackBackup(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if s.HasBackup() {
		t.Error("backup not cleared after a fully successful rollback")
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("file = %q, want %q", got, "original")
	}
}
