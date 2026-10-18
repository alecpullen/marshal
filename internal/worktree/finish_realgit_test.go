package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The helpers below are adapted from internal/acp/worktree_realgit_test.go
// (cross-package test sharing is not allowed, so the pattern is copied).
// They prove the behaviour FakeGitOps cannot: real git's merge, squash,
// worktree removal and branch deletion.

func realgit(t *testing.T, dir string, args ...string) string {
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

func initRealRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	realgit(t, dir, "init", "-b", "main")
	// The merge paths force commits (a --no-ff merge commit, a squash
	// commit) and inherit the ambient environment rather than the identity
	// the helper sets per-call. On a machine with no usable global identity
	// those commits fail, so pin one for the repo.
	realgit(t, dir, "config", "user.email", "t@e")
	realgit(t, dir, "config", "user.name", "t")
	realWriteFile(t, dir, "a.txt", "base\n")
	realGitCommitAll(t, dir, "base")
	return dir
}

func realWriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func realGitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	realgit(t, dir, "add", "-A")
	realgit(t, dir, "commit", "-m", msg)
}

// realWorktree isolates a worktree the way the ACP session path does:
// AgentDir creates .marshal/worktrees with its self-ignoring .gitignore (so
// the fresh worktree is not an untracked path of the project), then
// EnsureWorktree branches feat/x off HEAD there.
func realWorktree(t *testing.T, repo string) Worktree {
	t.Helper()
	dir, err := AgentDir(repo)
	if err != nil {
		t.Fatalf("AgentDir: %v", err)
	}
	wt, err := EnsureWorktree(CLIGitOps{}, repo, dir, "feat/x", "HEAD", WorktreeSetup{})
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	return wt
}

func TestRealGitSquashFinishSquashesAndCleansUp(t *testing.T) {
	repo := initRealRepo(t)
	wt := realWorktree(t, repo)
	realWriteFile(t, wt.Path, "new.txt", "from agent\n")
	realGitCommitAll(t, wt.Path, "agent work")
	target, err := CLIGitOps{}.RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	res, err := FinishBranch(CLIGitOps{}, repo, target, wt, FinishOptions{Squash: true, CommitMessage: "squashed feature", DeleteBranch: true})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if !res.Merged || !res.SquashCommit {
		t.Fatalf("res = %+v, want a merged squash", res)
	}
	if res.Commit == target {
		t.Fatalf("Commit = %q, want a new commit on main", res.Commit)
	}
	// The squash commit carries the agent's file and the given message.
	out := realgit(t, repo, "show", "--stat", "--format=%s", "HEAD")
	if !strings.Contains(out, "squashed feature") {
		t.Errorf("squash commit subject missing:\n%s", out)
	}
	if !strings.Contains(out, "new.txt") {
		t.Errorf("squash commit missing the agent's file:\n%s", out)
	}
	// The worktree is gone and the branch is deleted.
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still present: %v", err)
	}
	if (CLIGitOps{}).BranchExists(repo, "feat/x") {
		t.Error("branch feat/x still exists after DeleteBranch")
	}
	// The project is left on main, clean, not mid-merge.
	if branch := strings.TrimSpace(realgit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")); branch != "main" {
		t.Errorf("project on %q, want main", branch)
	}
	if dirty, err := (CLIGitOps{}).IsDirty(repo); err != nil || dirty {
		t.Errorf("project dirty after squash finish: %v (%v)", dirty, err)
	}
}

func TestRealGitNoFFFinishMergesAndCleansUp(t *testing.T) {
	repo := initRealRepo(t)
	wt := realWorktree(t, repo)
	realWriteFile(t, wt.Path, "new.txt", "from agent\n")
	realGitCommitAll(t, wt.Path, "agent work")
	target, err := CLIGitOps{}.RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	res, err := FinishBranch(CLIGitOps{}, repo, target, wt, FinishOptions{DeleteBranch: true})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if !res.Merged || res.SquashCommit {
		t.Fatalf("res = %+v, want a merged no-ff finish", res)
	}
	if res.Commit == target {
		t.Fatalf("Commit = %q, want a merge commit on main", res.Commit)
	}
	// --no-ff leaves a merge commit whose second parent is the branch tip.
	parents := strings.Fields(strings.TrimSpace(realgit(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")))
	if len(parents) != 3 {
		t.Fatalf("HEAD has %d parents, want a merge commit with 2: %v", len(parents)-1, parents)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still present: %v", err)
	}
	if (CLIGitOps{}).BranchExists(repo, "feat/x") {
		t.Error("branch feat/x still exists after DeleteBranch")
	}
}

func TestRealGitConflictingFinishAbortsClean(t *testing.T) {
	repo := initRealRepo(t)
	wt := realWorktree(t, repo)
	// The agent edits a.txt; main edits the same file, so the merge conflicts.
	realWriteFile(t, wt.Path, "a.txt", "agent\n")
	realGitCommitAll(t, wt.Path, "agent edit")
	realWriteFile(t, repo, "a.txt", "main\n")
	realGitCommitAll(t, repo, "main edit")
	// The caller captured the target before main moved on; capture the
	// current HEAD here so the merge itself is reached — the moved-target
	// guard is covered by the FakeGitOps tests.
	target, err := CLIGitOps{}.RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	res, err := FinishBranch(CLIGitOps{}, repo, target, wt, FinishOptions{})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if res.Merged || res.Reason != ReasonConflicts {
		t.Fatalf("res = %+v, want refused with %s", res, ReasonConflicts)
	}
	// The critical property: the project is usable, not mid-merge.
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); err == nil {
		t.Error("project left mid-merge after a refused finish")
	}
	if dirty, err := (CLIGitOps{}).IsDirty(repo); err != nil || dirty {
		t.Errorf("project dirty after an aborted merge: %v (%v)", dirty, err)
	}
}

// Untracked files in the operator's checkout — notes, drafts, local config —
// must neither block a finish nor end up in the merge commit.
func TestRealGitFinishMergesPastUntrackedProjectFiles(t *testing.T) {
	for _, squash := range []bool{true, false} {
		repo := initRealRepo(t)
		wt := realWorktree(t, repo)
		realWriteFile(t, wt.Path, "new.txt", "from agent\n")
		realGitCommitAll(t, wt.Path, "agent work")
		target, _ := CLIGitOps{}.RevParse(repo, "HEAD")

		realWriteFile(t, repo, "notes.md", "operator notes\n")
		if err := os.MkdirAll(filepath.Join(repo, "drafts"), 0o755); err != nil {
			t.Fatal(err)
		}
		realWriteFile(t, repo, "drafts/plan.md", "draft\n")

		res, err := FinishBranch(CLIGitOps{}, repo, target, wt, FinishOptions{Squash: squash, CommitMessage: "feature", DeleteBranch: true})
		if err != nil {
			t.Fatalf("squash=%v FinishBranch: %v", squash, err)
		}
		if !res.Merged {
			t.Fatalf("squash=%v res = %+v, want merged past untracked files", squash, res)
		}
		files := realgit(t, repo, "show", "--name-only", "--format=", "HEAD")
		if squash && !strings.Contains(files, "new.txt") {
			t.Errorf("squash commit missing the agent's file:\n%s", files)
		}
		if strings.Contains(files, "notes.md") || strings.Contains(files, "plan.md") {
			t.Errorf("squash=%v: the merge commit swept in untracked operator files:\n%s", squash, files)
		}
		status := realgit(t, repo, "status", "--porcelain")
		if !strings.Contains(status, "?? notes.md") || !strings.Contains(status, "?? drafts/") {
			t.Errorf("squash=%v: operator files are no longer untracked:\n%s", squash, status)
		}
		if got, _ := os.ReadFile(filepath.Join(repo, "notes.md")); string(got) != "operator notes\n" {
			t.Errorf("squash=%v: notes.md changed: %q", squash, got)
		}
	}
}

// An untracked file at a path the branch adds would be overwritten. git
// refuses before changing anything; the refusal names the file as blocking
// rather than reporting an empty conflict.
func TestRealGitFinishRefusesUntrackedFileTheMergeWouldOverwrite(t *testing.T) {
	for _, squash := range []bool{true, false} {
		repo := initRealRepo(t)
		wt := realWorktree(t, repo)
		realWriteFile(t, wt.Path, "new.txt", "from agent\n")
		realGitCommitAll(t, wt.Path, "agent work")
		target, _ := CLIGitOps{}.RevParse(repo, "HEAD")
		realWriteFile(t, repo, "new.txt", "operator's own file\n")

		res, err := FinishBranch(CLIGitOps{}, repo, target, wt, FinishOptions{Squash: squash, DeleteBranch: true})
		if err != nil {
			t.Fatalf("squash=%v FinishBranch: %v", squash, err)
		}
		if res.Merged || res.Reason != ReasonDirtyProject || len(res.Blocking) != 1 || res.Blocking[0] != "new.txt" {
			t.Fatalf("squash=%v res = %+v, want project_dirty blocked by new.txt", squash, res)
		}
		if head, _ := (CLIGitOps{}).RevParse(repo, "HEAD"); head != target {
			t.Errorf("squash=%v: project HEAD moved to %s", squash, head)
		}
		if got, _ := os.ReadFile(filepath.Join(repo, "new.txt")); string(got) != "operator's own file\n" {
			t.Errorf("squash=%v: the operator's untracked file changed: %q", squash, got)
		}
		if tracked, _ := (CLIGitOps{}).TrackedChanges(repo); len(tracked) != 0 {
			t.Errorf("squash=%v: refused merge left tracked changes: %v", squash, tracked)
		}
		if _, err := os.Stat(wt.Path); err != nil {
			t.Errorf("squash=%v: worktree removed after a refusal: %v", squash, err)
		}
	}
}

// Uncommitted changes to tracked files still block, and are named.
func TestRealGitFinishRefusesTrackedChangesAndNamesThem(t *testing.T) {
	repo := initRealRepo(t)
	wt := realWorktree(t, repo)
	realWriteFile(t, wt.Path, "new.txt", "from agent\n")
	realGitCommitAll(t, wt.Path, "agent work")
	target, _ := CLIGitOps{}.RevParse(repo, "HEAD")
	realWriteFile(t, repo, "a.txt", "operator edit\n")

	res, err := FinishBranch(CLIGitOps{}, repo, target, wt, FinishOptions{Squash: true, DeleteBranch: true})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if res.Merged || res.Reason != ReasonDirtyProject || len(res.Blocking) != 1 || res.Blocking[0] != "a.txt" {
		t.Fatalf("res = %+v, want project_dirty blocked by a.txt", res)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "operator edit\n" {
		t.Errorf("the operator's edit changed: %q", got)
	}
}

// TrackedChanges reports staged, unstaged and renamed tracked paths and
// nothing untracked — including a first entry whose status starts with a
// space, which a trimmed parse would corrupt.
func TestRealGitTrackedChangesIgnoresUntracked(t *testing.T) {
	repo := initRealRepo(t)
	realWriteFile(t, repo, "b.txt", "b\n")
	realGitCommitAll(t, repo, "add b")
	realWriteFile(t, repo, "a.txt", "unstaged edit\n")
	realgit(t, repo, "mv", "b.txt", "c.txt")
	realWriteFile(t, repo, "untracked.txt", "u\n")

	got, err := CLIGitOps{}.TrackedChanges(repo)
	if err != nil {
		t.Fatalf("TrackedChanges: %v", err)
	}
	want := map[string]bool{"a.txt": true, "c.txt": true}
	if len(got) != len(want) {
		t.Fatalf("TrackedChanges = %q, want a.txt and c.txt only", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("TrackedChanges = %q, want a.txt and c.txt only", got)
		}
	}
}
