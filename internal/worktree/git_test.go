package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a git repo with one commit and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeRepoFile(t, dir, "seed.txt", "seed\n")
	commitAll(t, dir, "seed")
	return dir
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestCLIGitOpsDirtyCommitAndRange(t *testing.T) {
	repo := initRepo(t)
	g := CLIGitOps{}

	dirty, err := g.IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if dirty {
		t.Fatal("IsDirty on a clean repo = true, want false")
	}

	base, err := g.RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}

	writeRepoFile(t, repo, "new.txt", "hello\n")
	dirty, err = g.IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if !dirty {
		t.Fatal("IsDirty with an untracked file = false, want true")
	}

	head, err := g.CommitAll(repo, "task 1 — first thing")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if head == base {
		t.Fatal("CommitAll returned the old HEAD")
	}
	dirty, _ = g.IsDirty(repo)
	if dirty {
		t.Fatal("IsDirty after CommitAll = true, want false")
	}

	rng := base + ".." + head
	log, err := g.LogOneline(repo, rng)
	if err != nil {
		t.Fatalf("LogOneline: %v", err)
	}
	if !strings.Contains(log, "task 1 — first thing") {
		t.Errorf("LogOneline = %q, want the commit subject", log)
	}
	stat, err := g.DiffStat(repo, rng)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if !strings.Contains(stat, "new.txt") {
		t.Errorf("DiffStat = %q, want new.txt", stat)
	}
	diff, err := g.Diff(repo, rng, 10)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "+hello") {
		t.Errorf("Diff = %q, want the added line", diff)
	}
}

func TestCLIGitOpsCommitAllNothingToCommit(t *testing.T) {
	repo := initRepo(t)
	g := CLIGitOps{}
	if _, err := g.CommitAll(repo, "empty"); err == nil {
		t.Fatal("CommitAll with a clean tree: want error, got nil")
	}
}

// initRepoWithoutIdentity creates a git repo whose only commit was made
// with an identity supplied on the command line, leaving the repository
// with none of its own — exactly what an agent's throwaway checkout looks
// like, since the bridge makes it with `git clone` and never configures it.
func initRepoWithoutIdentity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeRepoFile(t, dir, "seed.txt", "seed\n")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "seed"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		// The identity is passed for this one commit only; it is NOT
		// written to the repository's config.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Seed", "GIT_AUTHOR_EMAIL=seed@example.com",
			"GIT_COMMITTER_NAME=Seed", "GIT_COMMITTER_EMAIL=seed@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// A bridge-made agent checkout has no git identity: the bridge clones it and
// clones do not carry one. CommitAll then fails with "Author identity
// unknown" and the agent's work can never be shipped — the exit path, the
// pipeline controller and the CI fixer all commit through here.
func TestCLIGitOpsCommitAllWithoutARepoIdentity(t *testing.T) {
	repo := initRepoWithoutIdentity(t)
	g := CLIGitOps{}

	// Guard the fixture: the repo must genuinely have no identity, or the
	// test would pass for the wrong reason.
	if out, err := exec.Command("git", "-C", repo, "config", "user.email").CombinedOutput(); err == nil {
		t.Fatalf("fixture has a repo identity (%q); it is not exercising the bug", strings.TrimSpace(string(out)))
	}

	writeRepoFile(t, repo, "new.txt", "hello\n")
	if _, err := g.CommitAll(repo, "fix: something"); err != nil {
		t.Fatalf("CommitAll without a repo identity: %v", err)
	}
	dirty, err := g.IsDirty(repo)
	if err != nil {
		t.Fatalf("IsDirty: %v", err)
	}
	if dirty {
		t.Fatal("the commit did not take")
	}
	author, err := g.run(repo, "log", "-1", "--format=%an <%ae>")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(author, "@") {
		t.Fatalf("commit author = %q, want a usable identity", author)
	}
}

func TestCLIWorktreeRemoveAndPrune(t *testing.T) {
	repo := initRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt")
	g := CLIGitOps{}
	if err := g.WorktreeAdd(repo, wtPath, "feature-x", "HEAD"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := g.WorktreeRemove(repo, wtPath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatal("worktree dir still exists after remove")
	}
	if err := g.WorktreePrune(repo); err != nil {
		t.Fatalf("WorktreePrune: %v", err)
	}
	wts, err := g.WorktreeList(repo)
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	for _, w := range wts {
		if w == wtPath {
			t.Fatal("removed worktree still listed")
		}
	}
}

func TestCLIWorktreeRemoveDirtyRefuses(t *testing.T) {
	repo := initRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt")
	g := CLIGitOps{}
	if err := g.WorktreeAdd(repo, wtPath, "feature-y", "HEAD"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("user work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.WorktreeRemove(repo, wtPath); err == nil {
		t.Fatal("WorktreeRemove on dirty worktree succeeded; git should refuse")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatal("dirty worktree must be kept")
	}
}

// TestCLIWorktreeBranch verifies WorktreeBranch against real git: it must
// report the attached branch, tolerate a caller path with a trailing slash,
// and error for a path that is not a registered worktree.
func TestCLIWorktreeBranch(t *testing.T) {
	repo := initRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt")
	g := CLIGitOps{}
	if err := g.WorktreeAdd(repo, wtPath, "feature-z", "HEAD"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	branch, err := g.WorktreeBranch(repo, wtPath)
	if err != nil {
		t.Fatalf("WorktreeBranch: %v", err)
	}
	if branch != "feature-z" {
		t.Fatalf("WorktreeBranch = %q, want feature-z", branch)
	}

	// A trailing slash must still resolve to the same worktree.
	branch, err = g.WorktreeBranch(repo, wtPath+string(filepath.Separator))
	if err != nil {
		t.Fatalf("WorktreeBranch with trailing slash: %v", err)
	}
	if branch != "feature-z" {
		t.Fatalf("WorktreeBranch (trailing slash) = %q, want feature-z", branch)
	}

	// A path that is not a registered worktree must error.
	if _, err := g.WorktreeBranch(repo, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("WorktreeBranch for an unregistered path: want error, got nil")
	}
}
