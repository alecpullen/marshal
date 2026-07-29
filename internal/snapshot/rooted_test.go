package snapshot

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestRootedSnapshotsFollowActiveRoot(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	worktree := t.TempDir()

	root := project
	r := NewRooted(dataDir, project, func() string { return root }, 1<<20, nil, slog.Default())
	ctx := context.Background()

	// Snapshot and restore at the project root.
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h1, err := r.Track(ctx)
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.Restore(ctx, h1); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "f.txt")); string(data) != "v1" {
		t.Fatalf("project f.txt = %q, want v1", data)
	}

	// Rebind to the worktree: snapshots and restores land there, and the
	// project root's files are untouched by worktree operations.
	root = worktree
	if err := os.WriteFile(filepath.Join(worktree, "f.txt"), []byte("w1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h2, err := r.Track(ctx)
	if err != nil {
		t.Fatalf("Track worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "f.txt"), []byte("w2"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.Restore(ctx, h2); err != nil {
		t.Fatalf("Restore worktree: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(worktree, "f.txt")); string(data) != "w1" {
		t.Fatalf("worktree f.txt = %q, want w1", data)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "f.txt")); string(data) != "v1" {
		t.Fatalf("project f.txt = %q after worktree ops, want v1", data)
	}
}
