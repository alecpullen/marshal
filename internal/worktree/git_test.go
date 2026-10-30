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

// isolateGitIdentity points git's config lookups at per-test files and clears
// any identity from the environment, so an identity-free fixture really has no
// identity no matter how the developer's machine is configured and nothing the
// test does can touch their real Git configuration.
//
// The identity variables are unset rather than blanked: an empty
// GIT_AUTHOR_NAME is still a value, and git rejects it.
func isolateGitIdentity(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(dir, "system.gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, key := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	} {
		unsetEnvForTest(t, key)
	}
}

// unsetEnvForTest removes key for the duration of the test and restores its
// previous value, or its absence, on cleanup.
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if !had {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, prev)
	})
}

// commitWithoutRepoIdentity builds the identity-free checkout, guards that it
// really has no effective identity, commits through the production fallback,
// and returns the author of the resulting commit.
func commitWithoutRepoIdentity(t *testing.T) string {
	t.Helper()
	repo := initRepoWithoutIdentity(t)
	g := CLIGitOps{}

	// Guard the fixture: no identity may be in effect, or the test would
	// pass for the wrong reason.
	if out, err := g.run(repo, "config", "user.email"); err == nil {
		t.Fatalf("fixture has an effective identity (%q); it is not exercising the bug", out)
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
	return author
}

// A bridge-made agent checkout has no git identity: the bridge clones it and
// clones do not carry one. CommitAll then fails with "Author identity
// unknown" and the agent's work can never be shipped — the exit path, the
// pipeline controller and the CI fixer all commit through here.
func TestCLIGitOpsCommitAllWithoutARepoIdentity(t *testing.T) {
	isolateGitIdentity(t)
	author := commitWithoutRepoIdentity(t)
	if !strings.Contains(author, "@") {
		t.Fatalf("commit author = %q, want a usable identity", author)
	}
}

// A real machine usually has a Git identity, so the identity-free fixture must
// be shown to ignore one rather than merely benefit from its absence. A
// synthetic global config stands in for the developer's here.
func TestCLIGitOpsNoIdentityIgnoresPoisonedGlobal(t *testing.T) {
	const (
		poisonedName  = "Poisoned Developer"
		poisonedEmail = "poisoned@example.com"
	)
	// A non-repository directory keeps the control lookup free of any local
	// config: global beats system, so the synthetic identity must win.
	dir := t.TempDir()
	poisoned := filepath.Join(dir, "global.gitconfig")
	body := "[user]\n\tname = " + poisonedName + "\n\temail = " + poisonedEmail + "\n"
	if err := os.WriteFile(poisoned, []byte(body), 0o644); err != nil {
		t.Fatalf("write synthetic global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", poisoned)

	out, err := exec.Command("git", "-C", dir, "config", "user.email").CombinedOutput()
	if err != nil {
		t.Fatalf("control git config user.email: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != poisonedEmail {
		t.Fatalf("control git config user.email = %q, want the synthetic %q", got, poisonedEmail)
	}

	// The fixture overrides those lookups with its own per-test paths, so no
	// identity is in effect and CommitAll must fall back to marshal's.
	isolateGitIdentity(t)
	author := commitWithoutRepoIdentity(t)
	if strings.Contains(author, poisonedEmail) || strings.Contains(author, poisonedName) {
		t.Fatalf("commit author = %q, want marshal's fallback, not the ambient identity", author)
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
