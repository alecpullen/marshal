package git_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alec/marshal/internal/git"
)

// newTestRepo initialises a temporary git repository, makes an initial commit,
// and returns a Repo pointed at it.
func newTestRepo(t *testing.T) *git.Repo {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@marshal"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}

	// Write and commit an initial file so the repo has a HEAD.
	writeFile(t, dir, "README.md", "# test repo\n")
	gitAdd(t, dir, ".")
	gitCommit(t, dir, "initial commit")

	repo, err := git.New(dir, git.RepoConfig{CoAuthoredBy: "test-model"})
	if err != nil {
		t.Fatalf("git.New: %v", err)
	}
	return repo
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitAdd(t *testing.T, dir string, paths ...string) {
	t.Helper()
	args := append([]string{"add"}, paths...)
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
}

func gitCommit(t *testing.T, dir, message string) {
	t.Helper()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_COMMITTER_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@marshal",
		"GIT_COMMITTER_EMAIL=test@marshal",
	)
	c := exec.Command("git", "commit", "-m", message)
	c.Dir = dir
	c.Env = env
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func gitLog(t *testing.T, dir string) []string {
	t.Helper()
	c := exec.Command("git", "log", "--oneline", "--no-decorate")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func gitBranches(t *testing.T, dir string) []string {
	t.Helper()
	c := exec.Command("git", "branch", "--format=%(refname:short)")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func branchExists(t *testing.T, dir, name string) bool {
	t.Helper()
	for _, b := range gitBranches(t, dir) {
		if b == name {
			return true
		}
	}
	return false
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	c := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("current branch: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	c := exec.Command("git", "rev-parse", "HEAD")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("HEAD SHA: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func shaOfBranch(t *testing.T, dir, branch string) string {
	t.Helper()
	c := exec.Command("git", "rev-parse", branch)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("SHA of %q: %v", branch, err)
	}
	return strings.TrimSpace(string(out))
}

func assertBranchExists(t *testing.T, dir, branch string) {
	t.Helper()
	if !branchExists(t, dir, branch) {
		t.Errorf("expected branch %q to exist; branches: %v", branch, gitBranches(t, dir))
	}
}

func assertBranchGone(t *testing.T, dir, branch string) {
	t.Helper()
	if branchExists(t, dir, branch) {
		t.Errorf("expected branch %q to be deleted; branches: %v", branch, gitBranches(t, dir))
	}
}

func assertCurrentBranch(t *testing.T, dir, want string) {
	t.Helper()
	if got := currentBranch(t, dir); got != want {
		t.Errorf("current branch: want %q got %q", want, got)
	}
}

// assertCommitCount asserts the number of commits on the given branch.
func assertCommitCount(t *testing.T, dir, branch string, want int) {
	t.Helper()
	c := exec.Command("git", "rev-list", "--count", branch)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-list: %v", err)
	}
	var got int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &got)
	if got != want {
		t.Errorf("commit count on %q: want %d, got %d", branch, want, got)
	}
}
