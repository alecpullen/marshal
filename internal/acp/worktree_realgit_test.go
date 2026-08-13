package acp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"marshal/internal/worktree"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	writeFile(t, dir, "a.txt", "base\n")
	gitCommitAll(t, dir, "base")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", msg)
}

// realManager isolates a session in repo and returns a manager over it.
func realManager(t *testing.T, repo string) *WorktreeManager {
	t.Helper()
	st := newWorktreeTestState(t, repo)
	if _, err := isolateSession(worktree.CLIGitOps{}, st, repo, IsolationParams{Branch: "feat/x"}, "n"); err != nil {
		t.Fatalf("isolateSession: %v", err)
	}
	return NewWorktreeManager(WorktreeManagerConfig{
		Git:    worktree.CLIGitOps{},
		Lookup: func(string) (*WorktreeRuntime, bool) { return &WorktreeRuntime{State: st, ProjectRoot: repo}, true },
	})
}

func TestRealGitCleanMergeLandsCommits(t *testing.T) {
	repo := initTestRepo(t)
	m := realManager(t, repo)

	// Agent commits a new file in its worktree.
	wt := filepath.Join(repo, ".marshal", "worktrees", "feat-x")
	writeFile(t, wt, "new.txt", "from agent\n")
	gitCommitAll(t, wt, "agent work")

	res, err := m.Merge(context.Background(), json.RawMessage(`{"sessionId":"s1","targetBranch":"main"}`))
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	got := res.(MergeResult)
	if !got.Merged {
		t.Fatalf("merge refused: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Errorf("agent's file did not land on main: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree should be gone, stat err = %v", err)
	}
}

// The behaviour FakeGitOps cannot prove: after a conflicting merge the
// project must be left usable, not mid-merge.
func TestRealGitConflictingMergeAbortsClean(t *testing.T) {
	repo := initTestRepo(t)
	m := realManager(t, repo)
	wt := filepath.Join(repo, ".marshal", "worktrees", "feat-x")

	// Both sides edit the same line.
	writeFile(t, wt, "a.txt", "agent\n")
	gitCommitAll(t, wt, "agent edit")
	writeFile(t, repo, "a.txt", "main\n")
	gitCommitAll(t, repo, "main edit")

	res, err := m.Merge(context.Background(), json.RawMessage(`{"sessionId":"s1","targetBranch":"main"}`))
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	got := res.(MergeResult)
	if got.Merged || got.Reason != ReasonConflicts {
		t.Fatalf("got %+v, want a conflicts refusal", got)
	}
	// No merge left in progress.
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Errorf("MERGE_HEAD still present — the merge was not aborted (stat err = %v)", err)
	}
	// Working tree back to main's content, no conflict markers.
	b, rerr := os.ReadFile(filepath.Join(repo, "a.txt"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(b) != "main\n" {
		t.Errorf("a.txt = %q, want main's content restored", b)
	}
}
