package sdd

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepoDir creates a minimal git repo in a temp dir with one commit.
func initGitRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("git", args...).Output(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", dir)
	run("-C", dir, "config", "user.email", "test@test.com")
	run("-C", dir, "config", "user.name", "Test")
	run("-C", dir, "checkout", "-b", "main")
	// Create a file and commit so HEAD exists.
	if err := exec.Command("sh", "-c", "echo hello > "+filepath.Join(dir, "README.md")).Run(); err != nil {
		t.Fatal(err)
	}
	run("-C", dir, "add", "-A")
	run("-C", dir, "commit", "-m", "init")
	return dir
}

func TestCreateWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping git worktree test in short mode")
	}
	dir := initGitRepoDir(t)
	wt, err := CreateWorktree(dir, "sdd/test-plan")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if wt.Path == "" || wt.Branch == "" {
		t.Fatalf("Worktree = %+v, want non-empty path and branch", wt)
	}
	// Verify the worktree dir exists.
	if _, err := exec.Command("git", "-C", wt.Path, "status").Output(); err != nil {
		t.Fatalf("worktree git status: %v", err)
	}
	// Cleanup.
	if err := wt.Remove(); err != nil {
		t.Logf("cleanup worktree: %v", err)
	}
}
