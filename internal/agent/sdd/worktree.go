package sdd

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree represents a git worktree created for an SDD run.
type Worktree struct {
	Path   string
	Branch string
	parent string
}

// CreateWorktree creates a git worktree on a new branch rooted at the
// current HEAD of the working directory.
func CreateWorktree(workingDir, branchName string) (*Worktree, error) {
	wtPath := filepath.Join(workingDir, ".marshal", "worktrees", strings.ReplaceAll(branchName, "/", "-"))
	if err := exec.Command("git", "-C", workingDir, "worktree", "add", "-b", branchName, wtPath).Run(); err != nil {
		return nil, fmt.Errorf("sdd worktree: git worktree add: %w", err)
	}
	return &Worktree{Path: wtPath, Branch: branchName, parent: workingDir}, nil
}

// MergeBase returns the merge-base of the worktree branch and main.
func (wt *Worktree) MergeBase() (string, error) {
	out, err := exec.Command("git", "-C", wt.Path, "merge-base", "main", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("sdd worktree: merge-base: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Remove removes the worktree and its branch. Errors are swallowed.
func (wt *Worktree) Remove() error {
	exec.Command("git", "-C", wt.parent, "worktree", "remove", "--force", wt.Path).Run()
	exec.Command("git", "-C", wt.parent, "branch", "-D", wt.Branch).Run()
	return nil
}
