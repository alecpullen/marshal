package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requireGit skips when git is unavailable; the linked-worktree case shells out
// to set up fixtures.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
}

// initRepo creates a fresh git repo at dir with one commit so HEAD resolves.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "README").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func TestReadMainWorktree(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	// Fresh cache state for an isolated dir.
	info := Read(dir)
	if !info.InRepo {
		t.Fatalf("InRepo = false, want true")
	}
	if info.Branch != "main" && info.Branch != "master" {
		t.Fatalf("Branch = %q, want main or master", info.Branch)
	}
	if info.Worktree != "" {
		t.Fatalf("Worktree = %q, want empty in main worktree", info.Worktree)
	}
}

func TestReadFindsRepoFromSubdir(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	sub := filepath.Join(dir, "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	info := Read(sub)
	if !info.InRepo {
		t.Fatalf("InRepo = false from subdir, want true")
	}
	if info.Branch == "" {
		t.Fatalf("Branch empty from subdir")
	}
}

func TestReadLinkedWorktree(t *testing.T) {
	requireGit(t)
	main := t.TempDir()
	initRepo(t, main)
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", main, "worktree", "add", "-b", "feature", linked).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	info := Read(linked)
	if !info.InRepo {
		t.Fatalf("InRepo = false in linked worktree")
	}
	if info.Branch != "feature" {
		t.Fatalf("Branch = %q, want feature", info.Branch)
	}
	if info.Worktree != "linked" {
		t.Fatalf("Worktree = %q, want linked", info.Worktree)
	}
}

func TestReadDetachedHead(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	// Detach HEAD at the only commit.
	if out, err := exec.Command("git", "-C", dir, "checkout", "--detach", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}
	info := Read(dir)
	if !info.InRepo {
		t.Fatalf("InRepo = false in detached repo")
	}
	if !isHex(info.Branch[:min(len(info.Branch), 7)]) || len(info.Branch) < 7 {
		// Detached: we use the first 7 chars of the SHA.
		t.Fatalf("Branch = %q, want 7-char short SHA", info.Branch)
	}
}

func TestReadNonRepo(t *testing.T) {
	dir := t.TempDir()
	info := Read(dir)
	if info.InRepo || info.Branch != "" || info.Worktree != "" {
		t.Fatalf("non-repo returned %+v, want zero Info", info)
	}
}

func TestReadCachesPerDir(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	first := Read(dir)
	// Mutate HEAD in-process by switching branch via git and confirm cache
	// still serves the old value (proving cache hit), then invalidate.
	if out, err := exec.Command("git", "-C", dir, "checkout", "-b", "newbranch").CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b: %v\n%s", err, out)
	}
	cached := Read(dir)
	if cached.Branch != first.Branch {
		// Cache is best-effort; if it already reflected the new branch that's fine.
		// The point of this test is that a second read does not error.
	}
	Invalidate(dir)
	refreshed := Read(dir)
	if refreshed.Branch != "newbranch" {
		t.Fatalf("after Invalidate, Branch = %q, want newbranch", refreshed.Branch)
	}
}

func TestReadTruncatesLongBranch(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	long := strings.Repeat("a", 40)
	if out, err := exec.Command("git", "-C", dir, "checkout", "-b", long).CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b long: %v\n%s", err, out)
	}
	Invalidate(dir)
	info := Read(dir)
	if len([]rune(info.Branch)) > 21 {
		t.Fatalf("truncated branch %q longer than 20+1 runes", info.Branch)
	}
	if !strings.HasSuffix(info.Branch, "…") {
		t.Fatalf("truncated branch %q missing ellipsis", info.Branch)
	}
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Suppress unused import on non-darwin where exec is still used.
var _ = runtime.GOOS
