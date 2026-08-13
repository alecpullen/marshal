package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionWorkspaceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d, err := Open(filepath.Join(dir, ".marshal", "marshal.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject("/tmp/proj", "proj")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := d.CreateSession("s1", pid, "t", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	row, err := d.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "" || row.WorktreeBranch != "" || row.WorktreeTargetBranch != "" {
		t.Fatalf("fresh session workspace = %q/%q/%q, want empty", row.ActiveRoot, row.WorktreeBranch, row.WorktreeTargetBranch)
	}

	if err := d.UpdateSessionWorkspace("s1", "/tmp/proj/.marshal/worktrees/feat-x", "feat/x", "main"); err != nil {
		t.Fatalf("UpdateSessionWorkspace: %v", err)
	}
	row, err = d.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "/tmp/proj/.marshal/worktrees/feat-x" || row.WorktreeBranch != "feat/x" || row.WorktreeTargetBranch != "main" {
		t.Fatalf("workspace = %q/%q/%q, want worktree path, feat/x, main", row.ActiveRoot, row.WorktreeBranch, row.WorktreeTargetBranch)
	}

	if err := d.UpdateSessionWorkspace("s1", "", "", ""); err != nil {
		t.Fatalf("UpdateSessionWorkspace clear: %v", err)
	}
	row, err = d.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.ActiveRoot != "" || row.WorktreeBranch != "" || row.WorktreeTargetBranch != "" {
		t.Fatalf("cleared workspace = %q/%q/%q, want empty", row.ActiveRoot, row.WorktreeBranch, row.WorktreeTargetBranch)
	}
}
