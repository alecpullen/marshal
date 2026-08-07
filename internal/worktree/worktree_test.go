package worktree

import (
	"path/filepath"
	"testing"
)

func TestEnsureWorktreeCreates(t *testing.T) {
	g := NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"

	wt, err := EnsureWorktree(g, "/repo", "/run/worktrees", "pipeline/my-plan", "main")
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	if wt.Branch != "pipeline/my-plan" {
		t.Errorf("Branch = %q", wt.Branch)
	}
	if want := filepath.Join("/run/worktrees", "pipeline-my-plan"); wt.Path != want {
		t.Errorf("Path = %q, want %q", wt.Path, want)
	}
	if wt.Base != g.Refs["main"] {
		t.Errorf("Base = %q, want the start ref SHA", wt.Base)
	}
	if len(g.Added) != 1 {
		t.Fatalf("WorktreeAdd called %d times, want 1", len(g.Added))
	}
}

func TestEnsureWorktreeReusesExistingBranch(t *testing.T) {
	g := NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	branch := "pipeline/my-plan"
	g.Branches[branch] = true
	g.Worktrees = append(g.Worktrees, filepath.Join("/run/worktrees", "pipeline-my-plan"))
	g.Refs[branch] = "2222222222222222222222222222222222222222"

	wt, err := EnsureWorktree(g, "/repo", "/run/worktrees", branch, "main")
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	if len(g.Added) != 0 {
		t.Errorf("WorktreeAdd called on a resume: %v", g.Added)
	}
	if wt.Base != g.Refs[branch] {
		t.Errorf("Base = %q, want %q", wt.Base, g.Refs[branch])
	}
}

func TestEnsureWorktreeBranchWithoutWorktree(t *testing.T) {
	g := NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"
	g.Branches["pipeline/my-plan"] = true

	if _, err := EnsureWorktree(g, "/repo", "/run/worktrees", "pipeline/my-plan", "main"); err == nil {
		t.Fatal("branch exists but no worktree: want error, got nil")
	}
}

func TestFakeGitOpsRecordsCalls(t *testing.T) {
	g := NewFakeGitOps()
	g.Refs["main"] = "1111111111111111111111111111111111111111"

	if _, err := EnsureWorktree(g, "/repo", "/run/worktrees", "pipeline/my-plan", "main"); err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	got := g.Calls()
	want := []string{"BranchExists", "RevParse", "WorktreeAdd"}
	if len(got) != len(want) {
		t.Fatalf("Calls() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Calls()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
