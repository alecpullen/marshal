package sdd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceEnsureCreatesTree(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	sddDir, err := ws.Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, sub := range []string{"state", "contracts", "reports", "diffs", "checkpoints", "worktrees", "archive"} {
		p := filepath.Join(sddDir, sub)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", sub, err)
		}
	}
	gitignore := filepath.Join(sddDir, ".gitignore")
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(data) != "*\n" {
		t.Fatalf(".gitignore = %q, want *\\n", data)
	}
}

func TestWorkspaceEnsureIdempotent(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	// Second Ensure must not error and must not wipe the tree.
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
}

func TestWorkspaceResetArchivesAndClears(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	// Seed a fake dag + report.
	dagPath := filepath.Join(ws.Dir(), "dag.json")
	if err := os.WriteFile(dagPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(ws.Dir(), "reports", "T1.md")
	if err := os.WriteFile(reportPath, []byte("status: DONE"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ws.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if _, err := os.Stat(dagPath); !os.IsNotExist(err) {
		t.Fatalf("dag.json should be removed, got %v", err)
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("reports/T1.md should be removed, got %v", err)
	}
	// Archive should contain one timestamped dir.
	entries, err := os.ReadDir(filepath.Join(ws.Dir(), "archive"))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
}

func TestWorkspaceResumeDetectsExisting(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	if _, err := ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	// No dag.json -> not resumable.
	resumable, err := ws.Resume()
	if err != nil {
		t.Fatalf("Resume (empty): %v", err)
	}
	if resumable {
		t.Fatal("empty workspace should not be resumable")
	}
	// Seed a dag.json -> resumable.
	if err := os.WriteFile(filepath.Join(ws.Dir(), "dag.json"), []byte(`{"tasks":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	resumable, err = ws.Resume()
	if err != nil {
		t.Fatalf("Resume (seeded): %v", err)
	}
	if !resumable {
		t.Fatal("seeded workspace should be resumable")
	}
}

func TestWorkspaceWorktreesDir(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	want := filepath.Join(dir, ".marshal", "sdd", "worktrees")
	if got := ws.WorktreesDir(); got != want {
		t.Fatalf("WorktreesDir = %q, want %q", got, want)
	}
}
