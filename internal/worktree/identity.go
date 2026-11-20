package worktree

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckoutIdentity is the durable identity of one checkout: where it is, the
// repository that owns it, what it has checked out, and the commit it is at.
//
// Root and Repository are canonical absolute paths, and Repository is the
// common git directory rather than a branch name: every worktree of one
// repository reports the same Repository, and a copied, re-initialised or
// re-pointed directory reports its own. Identity is therefore something a
// caller can record and recheck, not something inferred from where a directory
// happens to sit.
type CheckoutIdentity struct {
	Root       string // canonical absolute path of the checkout
	Repository string // canonical absolute common git directory
	Branch     string // "" when HEAD is detached
	Head       string // commit at HEAD
}

// IdentityGit is the git subset identity capture and rechecking need. It is a
// narrower interface than GitOps so a caller that only records and verifies
// identity does not have to provide the whole seam.
type IdentityGit interface {
	RevParse(dir, ref string) (string, error)
	RepositoryID(dir string) (string, error)
}

// CaptureCheckoutIdentity reads dir's identity. The directory must exist and
// be inside a git repository: an identity that cannot name a repository cannot
// prove ownership, so this is an error rather than a partial value.
//
// A detached HEAD is a valid identity — dispatch may start from one — and is
// recorded with an empty Branch.
func CaptureCheckoutIdentity(git IdentityGit, dir string) (CheckoutIdentity, error) {
	if dir == "" {
		return CheckoutIdentity{}, fmt.Errorf("worktree identity: no directory given")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return CheckoutIdentity{}, fmt.Errorf("worktree identity: %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return CheckoutIdentity{}, fmt.Errorf("worktree identity: %s: %w", dir, err)
	}
	if !info.IsDir() {
		return CheckoutIdentity{}, fmt.Errorf("worktree identity: %s is not a directory", dir)
	}
	root := CanonicalPath(abs)

	repository, err := git.RepositoryID(root)
	if err != nil {
		return CheckoutIdentity{}, fmt.Errorf("worktree identity: %s: %w", dir, err)
	}
	head, err := git.RevParse(root, "HEAD")
	if err != nil {
		return CheckoutIdentity{}, fmt.Errorf("worktree identity: %s: HEAD: %w", dir, err)
	}
	branch, err := git.RevParse(root, "--abbrev-ref HEAD")
	if err != nil {
		// --abbrev-ref fails only on an unborn HEAD; the commit above already
		// succeeded, so treat the branch as unknown rather than inventing one.
		branch = ""
	}
	if branch == "HEAD" {
		branch = "" // detached
	}
	return CheckoutIdentity{
		Root:       root,
		Repository: repository,
		Branch:     branch,
		Head:       head,
	}, nil
}

// CheckCheckoutIdentity rechecks that the checkout recorded as want is still
// the one at want.Root. It compares the canonical root (so a directory
// replaced by a symlink to somewhere else no longer matches), the repository
// (so a checkout re-pointed at another repository no longer matches) and the
// checked-out branch (so a replaced branch no longer matches).
//
// It deliberately does not compare Head: a checkout's HEAD legitimately
// advances as work is committed, and the caller records the observed candidate
// separately.
func CheckCheckoutIdentity(git IdentityGit, want CheckoutIdentity) error {
	if want.Root == "" {
		return fmt.Errorf("worktree identity: the record names no workspace root")
	}
	got, err := CaptureCheckoutIdentity(git, want.Root)
	if err != nil {
		return fmt.Errorf("worktree identity: %s: %w", want.Root, err)
	}
	if got.Root != want.Root {
		return fmt.Errorf("worktree identity: %s now resolves to %s", want.Root, got.Root)
	}
	if want.Repository != "" && got.Repository != want.Repository {
		return fmt.Errorf("worktree identity: %s belongs to repository %s, recorded %s", want.Root, got.Repository, want.Repository)
	}
	if got.Branch != want.Branch {
		return fmt.Errorf("worktree identity: %s has branch %q checked out, recorded %q", want.Root, got.Branch, want.Branch)
	}
	return nil
}
