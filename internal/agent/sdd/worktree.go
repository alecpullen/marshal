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
// Remove removes the worktree directory but keeps the branch (so the user
// can /merge or /pr). Errors are swallowed.
func (wt *Worktree) Remove() error {
	exec.Command("git", "-C", wt.parent, "worktree", "remove", "--force", wt.Path).Run()
	return nil
}
