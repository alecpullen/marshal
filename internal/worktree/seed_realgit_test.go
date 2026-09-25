package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealGitSeedDocsArchiveSurvivesCommitAndMerge proves the seeding
// contract against a real repository, end to end. A whole-directory
// symlink to the checkout's .docs-archive defeats the directory-form
// gitignore pattern (git's ignore matching is type-sensitive), so it shows
// up as untracked, `git add -A` commits it, and merging the branch back
// replaces the real archive with a self-referential symlink. The shallow
// real directory must keep status clean, stay out of the committed tree,
// remain readable through the worktree, and leave the real archive intact
// after the merge.
func TestRealGitSeedDocsArchiveSurvivesCommitAndMerge(t *testing.T) {
	repo := initRealRepo(t)
	const body = "the plan body to protect\n"
	if err := os.MkdirAll(filepath.Join(repo, ".docs-archive", "superpowers", "plans"), 0o755); err != nil {
		t.Fatal(err)
	}
	realWriteFile(t, repo, ".docs-archive/superpowers/plans/p.md", body)
	realWriteFile(t, repo, ".gitignore", ".docs-archive/\n")
	realGitCommitAll(t, repo, "gitignore the docs archive")

	// The production path: EnsureWorktree seeds the fresh worktree.
	wt := realWorktree(t, repo)
	if len(wt.SeedWarnings) != 0 {
		t.Fatalf("SeedWarnings = %v, want none", wt.SeedWarnings)
	}

	// The assertion that would have caught the original bug: a
	// whole-directory symlink reports here as "?? .docs-archive". The real
	// shallow directory matches the directory-form pattern, so status is
	// clean — and a clean status is what keeps `git add -A` from staging
	// the archive in the first place.
	if out := realgit(t, wt.Path, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("worktree status not clean:\n%s", out)
	}

	// The archive is still readable through the worktree.
	got, err := os.ReadFile(filepath.Join(wt.Path, ".docs-archive", "superpowers", "plans", "p.md"))
	if err != nil {
		t.Fatalf("read seeded archive: %v", err)
	}
	if string(got) != body {
		t.Fatalf("seeded plan = %q, want %q", got, body)
	}

	// Ordinary agent work, committed through the code path the pipeline
	// uses. With the original bug this commit also carried the archive
	// symlink ("create mode 120000").
	realWriteFile(t, wt.Path, "new.txt", "from agent\n")
	if _, err := (CLIGitOps{}).CommitAll(wt.Path, "agent work"); err != nil {
		t.Fatalf("commit worktree: %v", err)
	}
	// `git add -A` must not have picked up the archive: it stays out of
	// the committed tree entirely.
	for _, line := range strings.Split(realgit(t, wt.Path, "ls-tree", "-r", "--name-only", "HEAD"), "\n") {
		name := strings.TrimSpace(line)
		if name == ".docs-archive" || strings.HasPrefix(name, ".docs-archive/") {
			t.Fatalf("archive reached the committed tree as %q", name)
		}
	}

	// Merge the branch back, the step that used to destroy the archive.
	target, err := (CLIGitOps{}).RevParse(repo, "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	res, err := FinishBranch(CLIGitOps{}, repo, target, wt, FinishOptions{})
	if err != nil {
		t.Fatalf("FinishBranch: %v", err)
	}
	if !res.Merged || res.SquashCommit {
		t.Fatalf("res = %+v, want a merged no-ff finish", res)
	}

	// The real archive must still be a real, readable directory in the
	// project checkout — not a symlink pointing at itself.
	info, err := os.Lstat(filepath.Join(repo, ".docs-archive"))
	if err != nil {
		t.Fatalf("lstat archive after merge: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf(".docs-archive became %v after merge, want a real directory", info.Mode())
	}
	if !info.IsDir() {
		t.Fatalf(".docs-archive is %v after merge, want a real directory", info.Mode())
	}
	got, err = os.ReadFile(filepath.Join(repo, ".docs-archive", "superpowers", "plans", "p.md"))
	if err != nil {
		t.Fatalf("read archive after merge: %v", err)
	}
	if string(got) != body {
		t.Fatalf("plan after merge = %q, want %q", got, body)
	}
}
