package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit blanks the developer's git configuration so a fixture only ever
// uses the identity it sets in the repository itself.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// TestCaptureCheckoutIdentityRecordsRepositoryAndBranch pins what an identity
// is: the canonical root, the repository's common git directory, the branch and
// the commit. A worktree of the same repository shares the repository identity
// but not the root.
func TestCaptureCheckoutIdentityRecordsRepositoryAndBranch(t *testing.T) {
	isolateGit(t)
	repo := initRealRepo(t)

	id, err := CaptureCheckoutIdentity(CLIGitOps{}, repo)
	if err != nil {
		t.Fatalf("CaptureCheckoutIdentity: %v", err)
	}
	if id.Root != CanonicalPath(repo) {
		t.Fatalf("root = %q, want the canonical checkout path", id.Root)
	}
	if want := CanonicalPath(filepath.Join(repo, ".git")); id.Repository != want {
		t.Fatalf("repository = %q, want the canonical common git directory %q", id.Repository, want)
	}
	if id.Branch != "main" {
		t.Fatalf("branch = %q, want main", id.Branch)
	}
	if want := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD")); id.Head != want {
		t.Fatalf("head = %q, want %q", id.Head, want)
	}
	if err := CheckCheckoutIdentity(CLIGitOps{}, id); err != nil {
		t.Fatalf("CheckCheckoutIdentity on its own identity: %v", err)
	}

	// A linked worktree of the same repository is the same repository and a
	// different checkout.
	wt := realWorktree(t, repo)
	wtID, err := CaptureCheckoutIdentity(CLIGitOps{}, wt.Path)
	if err != nil {
		t.Fatalf("CaptureCheckoutIdentity(worktree): %v", err)
	}
	if wtID.Repository != id.Repository {
		t.Fatalf("worktree repository = %q, want the same %q", wtID.Repository, id.Repository)
	}
	if wtID.Root == id.Root {
		t.Fatal("the worktree reported the main checkout's root")
	}
	if wtID.Branch != "feat/x" {
		t.Fatalf("worktree branch = %q, want feat/x", wtID.Branch)
	}
}

// TestCaptureCheckoutIdentityDetachedHead pins the detached case: it is a valid
// identity with no branch, and a checkout that later gains a branch no longer
// matches it.
func TestCaptureCheckoutIdentityDetachedHead(t *testing.T) {
	isolateGit(t)
	repo := initRealRepo(t)
	head := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))
	realgit(t, repo, "checkout", "--detach", head)

	id, err := CaptureCheckoutIdentity(CLIGitOps{}, repo)
	if err != nil {
		t.Fatalf("CaptureCheckoutIdentity: %v", err)
	}
	if id.Branch != "" {
		t.Fatalf("branch = %q, want empty for a detached HEAD", id.Branch)
	}
	if id.Head != head {
		t.Fatalf("head = %q, want the detached commit %q", id.Head, head)
	}
	if err := CheckCheckoutIdentity(CLIGitOps{}, id); err != nil {
		t.Fatalf("CheckCheckoutIdentity on a detached identity: %v", err)
	}

	// Checking a branch out in that checkout is a different identity.
	realgit(t, repo, "checkout", "main")
	if err := CheckCheckoutIdentity(CLIGitOps{}, id); err == nil {
		t.Fatal("a checkout that gained a branch still matched the detached identity")
	}
}

// scriptedIdentity is an IdentityGit for mismatches a real checkout cannot be
// coaxed into quickly.
type scriptedIdentity struct {
	repository string
	head       string
	branch     string
}

func (g scriptedIdentity) RevParse(dir, ref string) (string, error) {
	switch ref {
	case "HEAD":
		return g.head, nil
	case "--abbrev-ref HEAD":
		return g.branch, nil
	}
	return "", errors.New("scriptedIdentity: unexpected ref " + ref)
}

func (g scriptedIdentity) RepositoryID(dir string) (string, error) {
	if g.repository == "" {
		return "", errors.New("scriptedIdentity: not a repository")
	}
	return g.repository, nil
}

// TestCheckCheckoutIdentityReportsEachMismatch pins the three ownership checks:
// the repository a checkout belongs to, the branch it has checked out, and the
// directory the recorded path resolves to.
func TestCheckCheckoutIdentityReportsEachMismatch(t *testing.T) {
	isolateGit(t)

	t.Run("repository", func(t *testing.T) {
		repo := initRealRepo(t)
		id, err := CaptureCheckoutIdentity(CLIGitOps{}, repo)
		if err != nil {
			t.Fatalf("CaptureCheckoutIdentity: %v", err)
		}
		other := scriptedIdentity{repository: "/elsewhere/.git", head: id.Head, branch: id.Branch}
		err = CheckCheckoutIdentity(other, id)
		if err == nil {
			t.Fatal("a checkout reporting another repository matched the recorded identity")
		}
		if !strings.Contains(err.Error(), "repository") {
			t.Fatalf("mismatch error = %v, want it to name the repository", err)
		}
	})

	t.Run("branch", func(t *testing.T) {
		repo := initRealRepo(t)
		id, err := CaptureCheckoutIdentity(CLIGitOps{}, repo)
		if err != nil {
			t.Fatalf("CaptureCheckoutIdentity: %v", err)
		}
		other := scriptedIdentity{repository: id.Repository, head: id.Head, branch: "hijacked"}
		err = CheckCheckoutIdentity(other, id)
		if err == nil {
			t.Fatal("a checkout on another branch matched the recorded identity")
		}
		if !strings.Contains(err.Error(), "branch") {
			t.Fatalf("mismatch error = %v, want it to name the branch", err)
		}
	})

	t.Run("symlink pivot", func(t *testing.T) {
		repo := initRealRepo(t)
		id, err := CaptureCheckoutIdentity(CLIGitOps{}, repo)
		if err != nil {
			t.Fatalf("CaptureCheckoutIdentity: %v", err)
		}
		other := initRealRepo(t)
		moved := repo + "-moved"
		if err := os.Rename(repo, moved); err != nil {
			t.Fatalf("move checkout aside: %v", err)
		}
		if err := os.Symlink(other, repo); err != nil {
			t.Fatalf("pivot checkout: %v", err)
		}
		err = CheckCheckoutIdentity(CLIGitOps{}, id)
		if err == nil {
			t.Fatal("a pivoted checkout matched the recorded identity")
		}
		if !strings.Contains(err.Error(), "resolves to") {
			t.Fatalf("mismatch error = %v, want it to name the resolved path", err)
		}
	})

	t.Run("missing root", func(t *testing.T) {
		repo := initRealRepo(t)
		id, err := CaptureCheckoutIdentity(CLIGitOps{}, repo)
		if err != nil {
			t.Fatalf("CaptureCheckoutIdentity: %v", err)
		}
		if err := os.RemoveAll(repo); err != nil {
			t.Fatalf("remove checkout: %v", err)
		}
		if err := CheckCheckoutIdentity(CLIGitOps{}, id); err == nil {
			t.Fatal("a checkout that no longer exists matched the recorded identity")
		}
	})
}

// TestExactCommitHelpers pins the argv-only git reads a durable record needs:
// the repository identity, exact commit resolution, the commit range, its
// changed paths, and ancestry.
func TestExactCommitHelpers(t *testing.T) {
	isolateGit(t)
	repo := initRealRepo(t)
	git := CLIGitOps{}
	first := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))
	realWriteFile(t, repo, "b.txt", "b\n")
	realGitCommitAll(t, repo, "second")
	second := strings.TrimSpace(realgit(t, repo, "rev-parse", "HEAD"))

	// RepositoryID names the repository, not the checkout: every worktree of it
	// reports the same value.
	mainID, err := git.RepositoryID(repo)
	if err != nil {
		t.Fatalf("RepositoryID: %v", err)
	}
	wt := realWorktree(t, repo)
	wtID, err := git.RepositoryID(wt.Path)
	if err != nil {
		t.Fatalf("RepositoryID(worktree): %v", err)
	}
	if mainID != wtID {
		t.Fatalf("worktree repository = %q, want %q", wtID, mainID)
	}

	// ResolveCommit accepts an exact existing commit and refuses a ref name or
	// an unknown id.
	if got, err := git.ResolveCommit(repo, first); err != nil || got != first {
		t.Fatalf("ResolveCommit(%s) = %q, %v", first, got, err)
	}
	if _, err := git.ResolveCommit(repo, "main"); err == nil {
		t.Fatal("ResolveCommit accepted a ref name as a commit object id")
	}
	if _, err := git.ResolveCommit(repo, "0000000000000000000000000000000000000000"); err == nil {
		t.Fatal("ResolveCommit accepted a commit that does not exist")
	}

	// The range between two commits is the candidate's own history, and its
	// changed paths are the files that history touches.
	commits, err := git.CommitIDs(repo, first, second)
	if err != nil {
		t.Fatalf("CommitIDs: %v", err)
	}
	if len(commits) != 1 || commits[0] != second {
		t.Fatalf("commits = %v, want only %s", commits, second)
	}
	paths, err := git.ChangedPaths(repo, first, second)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "b.txt" {
		t.Fatalf("changed paths = %v, want only b.txt", paths)
	}

	// IsAncestor answers with git's exit code, both ways round.
	if ok, err := git.IsAncestor(repo, first, second); err != nil || !ok {
		t.Fatalf("IsAncestor(first, second) = %v, %v, want true", ok, err)
	}
	if ok, err := git.IsAncestor(repo, second, first); err != nil || ok {
		t.Fatalf("IsAncestor(second, first) = %v, %v, want false", ok, err)
	}
}
