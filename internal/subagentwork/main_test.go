package subagentwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/testenv"
)

func TestMain(m *testing.M) {
	// Point HOME, the Marshal/XDG config and data dirs and the provider
	// credentials at a temporary directory, so no test in this package can
	// read the developer's configuration.
	os.Exit(testenv.Run(m))
}

// gitFixture is a real repository the workspace tests allocate from. It sets a
// repository-local identity and blanks the global and system git configuration,
// so a test never contacts a remote, never needs a network, and never inherits a
// machine-specific setting from the developer's environment.
type gitFixture struct {
	t   *testing.T
	dir string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f := &gitFixture{t: t, dir: t.TempDir()}
	f.git("init", "-b", "main")
	f.git("config", "user.name", "marshal test")
	f.git("config", "user.email", "test@marshal.invalid")
	f.write("base.txt", "base\n")
	f.git("add", "-A")
	f.git("commit", "-m", "base")
	return f
}

func (f *gitFixture) git(args ...string) string {
	f.t.Helper()
	return gitIn(f.t, f.dir, args...)
}

func (f *gitFixture) write(name, content string) {
	f.t.Helper()
	writeIn(f.t, f.dir, name, content)
}

func (f *gitFixture) head() string {
	f.t.Helper()
	return gitIn(f.t, f.dir, "rev-parse", "HEAD")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readIn(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// ownedBranches lists the branches this package owns in dir.
func ownedBranches(t *testing.T, dir string) []string {
	t.Helper()
	out := gitIn(t, dir, "branch", "--list", subagentBranchPrefix+"*")
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*")); line != "" {
			branches = append(branches, line)
		}
	}
	return branches
}

// checkoutWorktrees lists the worktrees registered in dir, excluding the main
// checkout.
func checkoutWorktrees(t *testing.T, dir string) []string {
	t.Helper()
	out := gitIn(t, dir, "worktree", "list", "--porcelain")
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		if filepath.Clean(path) == filepath.Clean(dir) {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}
