package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alec/marshal/internal/git"
)

func TestNew_NotARepo(t *testing.T) {
	dir := t.TempDir()
	_, err := git.New(dir, git.RepoConfig{})
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestHeadSHA(t *testing.T) {
	repo := newTestRepo(t)
	sha, err := repo.HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Errorf("expected 40-char SHA, got %q", sha)
	}
}

func TestCurrentBranch(t *testing.T) {
	repo := newTestRepo(t)
	branch, err := repo.CurrentBranch()
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Errorf("expected main, got %q", branch)
	}
}

func TestIsDirty_Clean(t *testing.T) {
	repo := newTestRepo(t)
	dirty, err := repo.IsDirty()
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Error("fresh repo should not be dirty")
	}
}

func TestIsDirty_Untracked(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo.Root(), "newfile.txt", "hello")
	dirty, err := repo.IsDirty()
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Error("repo with untracked file should be dirty")
	}
}

func TestDirtyFiles(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo.Root(), "foo.txt", "hello")

	files, err := repo.DirtyFiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if strings.Contains(f, "foo.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("foo.txt not in dirty files: %v", files)
	}
}

func TestTrackedFiles(t *testing.T) {
	repo := newTestRepo(t)
	files, err := repo.TrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("expected at least one tracked file")
	}
	found := false
	for _, f := range files {
		if f == "README.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("README.md not in tracked files: %v", files)
	}
}

func TestCreateBranch(t *testing.T) {
	repo := newTestRepo(t)
	sha, _ := repo.HeadSHA()

	if err := repo.CreateBranch("feature/x", sha); err != nil {
		t.Fatal(err)
	}
	assertBranchExists(t, repo.Root(), "feature/x")
	// Must still be on main.
	assertCurrentBranch(t, repo.Root(), "main")
}

func TestCheckoutNewBranch(t *testing.T) {
	repo := newTestRepo(t)
	sha, _ := repo.HeadSHA()

	if err := repo.CheckoutNewBranch("feature/y", sha); err != nil {
		t.Fatal(err)
	}
	assertCurrentBranch(t, repo.Root(), "feature/y")
}

func TestDeleteBranch(t *testing.T) {
	repo := newTestRepo(t)
	sha, _ := repo.HeadSHA()
	_ = repo.CreateBranch("to-delete", sha)
	assertBranchExists(t, repo.Root(), "to-delete")

	if err := repo.DeleteBranch("to-delete"); err != nil {
		t.Fatal(err)
	}
	assertBranchGone(t, repo.Root(), "to-delete")
}

func TestDeleteBranch_Missing(t *testing.T) {
	repo := newTestRepo(t)
	// Deleting a non-existent branch should not error.
	if err := repo.DeleteBranch("nonexistent"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestCommitAll(t *testing.T) {
	repo := newTestRepo(t)
	before := headSHA(t, repo.Root())

	writeFile(t, repo.Root(), "hello.txt", "world")
	if err := repo.CommitAll("add hello.txt"); err != nil {
		t.Fatal(err)
	}

	after := headSHA(t, repo.Root())
	if before == after {
		t.Error("HEAD should have advanced after commit")
	}
}

func TestCommitAll_NothingToCommit(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.CommitAll("empty commit")
	if err != git.ErrNothingToCommit {
		t.Errorf("expected ErrNothingToCommit, got %v", err)
	}
}

func TestCommitAll_CoAuthoredByTrailer(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo.Root(), "x.txt", "x")
	if err := repo.CommitAll("test co-author"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "log", "-1", "--format=%B")
	cmd.Dir = repo.Root()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Co-authored-by: marshal (test-model)") {
		t.Errorf("co-authored-by trailer missing:\n%s", out)
	}
}

func TestDiff(t *testing.T) {
	repo := newTestRepo(t)
	base, _ := repo.HeadSHA()

	writeFile(t, repo.Root(), "patch.txt", "new content\n")
	if err := repo.CommitAll("add patch"); err != nil {
		t.Fatal(err)
	}

	diff, err := repo.Diff(git.DiffOpts{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "patch.txt") {
		t.Errorf("diff does not mention patch.txt:\n%s", diff)
	}
	if !strings.Contains(diff, "new content") {
		t.Errorf("diff does not contain added content:\n%s", diff)
	}
}

func TestDiff_DefaultContextIsOne(t *testing.T) {
	repo := newTestRepo(t)
	// Write a file with multiple lines so we can check context.
	writeFile(t, repo.Root(), "ctx.txt", "line1\nline2\nline3\nline4\nline5\n")
	if err := repo.CommitAll("base"); err != nil {
		t.Fatal(err)
	}
	base, _ := repo.HeadSHA()

	// Modify the middle line.
	writeFile(t, repo.Root(), "ctx.txt", "line1\nline2\nCHANGED\nline4\nline5\n")
	if err := repo.CommitAll("change"); err != nil {
		t.Fatal(err)
	}

	diff, err := repo.Diff(git.DiffOpts{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	// With -U1, we should see 1 context line either side, not 3.
	// Count context lines (lines starting with a space inside the hunk).
	hunkLines := 0
	for _, line := range strings.Split(diff, "\n") {
		if len(line) > 0 && line[0] == ' ' {
			hunkLines++
		}
	}
	if hunkLines > 2 {
		t.Errorf("expected ≤2 context lines (U1), got %d\ndiff:\n%s", hunkLines, diff)
	}
}

func TestMergeSquash(t *testing.T) {
	repo := newTestRepo(t)
	mainSHA, _ := repo.HeadSHA()

	// Feature branch with one commit.
	_ = repo.CheckoutNewBranch("feature/sq", mainSHA)
	writeFile(t, repo.Root(), "sq.txt", "squashed content\n")
	_ = repo.CommitAll("feature commit")

	// Squash-merge onto main.
	_ = repo.Checkout("main")
	if err := repo.MergeSquash("feature/sq", "squashed"); err != nil {
		t.Fatal(err)
	}

	// main: initial + squash = 2 commits.
	assertCommitCount(t, repo.Root(), "main", 2)
	if _, err := os.Stat(filepath.Join(repo.Root(), "sq.txt")); err != nil {
		t.Error("sq.txt missing after squash merge")
	}
}

func TestMergeSquash_AlreadyUpToDate(t *testing.T) {
	repo := newTestRepo(t)
	sha, _ := repo.HeadSHA()
	_ = repo.CreateBranch("empty", sha)

	err := repo.MergeSquash("empty", "msg")
	if err != git.ErrAlreadyUpToDate {
		t.Errorf("expected ErrAlreadyUpToDate, got %v", err)
	}
}

func TestRevertNoCommit(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo.Root(), "rv.txt", "original\n")
	_ = repo.CommitAll("add rv.txt")
	sha, _ := repo.HeadSHA()

	if err := repo.RevertNoCommit(sha); err != nil {
		t.Fatal(err)
	}
	dirty, _ := repo.IsDirty()
	if !dirty {
		t.Error("expected dirty working tree after revert --no-commit")
	}
}

func TestResetHard(t *testing.T) {
	repo := newTestRepo(t)
	original, _ := repo.HeadSHA()

	writeFile(t, repo.Root(), "temp.txt", "temp\n")
	_ = repo.CommitAll("temp")

	if err := repo.ResetHard(original); err != nil {
		t.Fatal(err)
	}
	after, _ := repo.HeadSHA()
	if after != original {
		t.Errorf("reset: want %s, got %s", original, after)
	}
}
