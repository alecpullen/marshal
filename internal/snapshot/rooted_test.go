package snapshot

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
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

// TestRootedSvcReusesServiceForSameRoot verifies that svc() returns the
// same *Service for the same active root, so concurrent Track calls share
// the same semaphore.
func TestRootedSvcReusesServiceForSameRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dataDir := t.TempDir()
	project := t.TempDir()

	r := NewRooted(dataDir, project, func() string { return project }, 1<<20, nil, testLogger())

	s1 := r.svc()
	s2 := r.svc()
	if s1 != s2 {
		t.Fatal("svc() should return the same *Service for the same root")
	}
}

// TestRootedSvcCreatesNewServiceForNewRoot verifies that svc() returns a
// different *Service when the active root changes (worktree rebind).
func TestRootedSvcCreatesNewServiceForNewRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dataDir := t.TempDir()
	project := t.TempDir()
	worktree := t.TempDir()

	root := project
	r := NewRooted(dataDir, project, func() string { return root }, 1<<20, nil, testLogger())

	s1 := r.svc()
	root = worktree
	s2 := r.svc()
	if s1 == s2 {
		t.Fatal("svc() should return a different *Service for a different root")
	}
	// First root's service should still be cached.
	s3 := r.svc()
	if s3 != s2 {
		t.Fatal("svc() should return cached *Service for worktree root")
	}
}
