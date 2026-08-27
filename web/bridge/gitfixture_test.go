package bridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newBareRepoFixture creates a bare repo with one commit and returns its
// path, for use as a local "remote". Everything in these tests is
// offline: no network, no git host.
func newBareRepoFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "seed")

	mustGit(t, "", "init", "--bare", "--initial-branch=main", bare)
	mustGit(t, "", "init", "--initial-branch=main", work)
	mustGit(t, work, "config", "user.email", "test@example.com")
	mustGit(t, work, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "add", "README.md")
	mustGit(t, work, "commit", "-m", "seed")
	mustGit(t, work, "remote", "add", "origin", bare)
	mustGit(t, work, "push", "origin", "main")
	return bare
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// testGitRunner returns a real (non-faked) runner for fixture tests.
func testGitRunner(t *testing.T) *gitRunner {
	t.Helper()
	g, err := newGitRunner()
	if err != nil {
		t.Skip(err)
	}
	return g
}
