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
	// Seed fake dag, report, and state/repo.json.
	dagPath := filepath.Join(ws.Dir(), "dag.json")
	if err := os.WriteFile(dagPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(ws.Dir(), "reports", "T1.md")
	if err := os.WriteFile(reportPath, []byte("status: DONE"), 0644); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(ws.Dir(), "state", "repo.json")
	if err := os.WriteFile(repoPath, []byte(`{"branch":"main"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Also seed a stray file in state/ that should be cleared.
	strayPath := filepath.Join(ws.Dir(), "state", "stray.txt")
	if err := os.WriteFile(strayPath, []byte("leftover"), 0644); err != nil {
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
	archiveDir := filepath.Join(ws.Dir(), "archive", entries[0].Name())
	// Verify archived dag.json exists.
	if _, err := os.Stat(filepath.Join(archiveDir, "dag.json")); err != nil {
		t.Fatalf("archived dag.json missing: %v", err)
	}
	// Verify archived reports/T1.md exists with correct content.
	archivedReport, err := os.ReadFile(filepath.Join(archiveDir, "reports", "T1.md"))
	if err != nil {
		t.Fatalf("archived reports/T1.md missing: %v", err)
	}
	if string(archivedReport) != "status: DONE" {
		t.Fatalf("archived reports/T1.md = %q, want %q", archivedReport, "status: DONE")
	}
	// Verify archived repo.json exists.
	if _, err := os.Stat(filepath.Join(archiveDir, "repo.json")); err != nil {
		t.Fatalf("archived repo.json missing: %v", err)
	}
	// Verify stray file in state/ is gone.
	if _, err := os.Stat(strayPath); !os.IsNotExist(err) {
		t.Fatalf("state/stray.txt should be removed, got %v", err)
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

func TestWorkspaceRepoPath(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	want := filepath.Join(dir, ".marshal", "sdd", "state", "repo.json")
	if got := ws.RepoPath(); got != want {
		t.Fatalf("RepoPath = %q, want %q", got, want)
	}
}

func TestWorkspaceProgressPath(t *testing.T) {
	dir := t.TempDir()
	ws, _ := NewWorkspace(dir)
	want := filepath.Join(dir, ".marshal", "sdd", "progress.md")
	if got := ws.ProgressPath(); got != want {
		t.Fatalf("ProgressPath = %q, want %q", got, want)
	}
}
