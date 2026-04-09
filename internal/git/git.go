// Package git provides real git operations via os/exec.
package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git implements real git operations via os/exec.
type Git struct {
	repoRoot   string // absolute path to git repository
	baseBranch string // usually "main" or "master"
}

// GitError wraps git command failures with context.
type GitError struct {
	Op      string // operation name
	Command string // full command
	Output  string // stderr output
	Err     error  // underlying error
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s failed: %v\ncommand: %s\noutput: %s",
		e.Op, e.Err, e.Command, e.Output)
}

// New creates a new Git instance.
// It verifies repoRoot is a git repository and detects the base branch.
func New(repoRoot string) (*Git, error) {
	// Clean and get absolute path
	absPath, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}

	// Verify it's a git repository
	if err := isGitRepo(absPath); err != nil {
		return nil, err
	}

	// Detect base branch (main or master)
	baseBranch, err := detectBaseBranch(absPath)
	if err != nil {
		return nil, err
	}

	return &Git{
		repoRoot:   absPath,
		baseBranch: baseBranch,
	}, nil
}

// isGitRepo verifies the path contains a .git directory.
func isGitRepo(path string) error {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("not a git repository: %s", path)
	}
	return nil
}

// detectBaseBranch determines if the repo uses "main" or "master".
func detectBaseBranch(repoPath string) (string, error) {
	// Check for main first (modern default)
	if branchExists(repoPath, "main") {
		return "main", nil
	}
	// Fall back to master
	if branchExists(repoPath, "master") {
		return "master", nil
	}
	return "", fmt.Errorf("no main or master branch found in %s", repoPath)
}

// branchExists checks if a branch exists in the repository.
func branchExists(repoPath, branch string) bool {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", branch)
	return cmd.Run() == nil
}

// CreateIsolationBranch creates and checks out a new branch from HEAD.
func (g *Git) CreateIsolationBranch(name string) error {
	// First ensure we're on the base branch
	if err := g.CheckoutBranch(g.baseBranch); err != nil {
		return fmt.Errorf("checkout base branch: %w", err)
	}

	// Create and checkout new branch
	cmd := exec.Command("git", "-C", g.repoRoot, "checkout", "-b", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &GitError{
			Op:      "create-isolation-branch",
			Command: cmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return nil
}

// GetDiff returns the diff between current state and HEAD.
// Includes staged changes and unstaged changes, but not untracked files.
func (g *Git) GetDiff() (string, error) {
	// Get staged changes
	stagedCmd := exec.Command("git", "-C", g.repoRoot, "diff", "--cached", "HEAD")
	stagedOutput, err := stagedCmd.CombinedOutput()
	if err != nil {
		return "", &GitError{
			Op:      "get-diff-staged",
			Command: stagedCmd.String(),
			Output:  string(stagedOutput),
			Err:     err,
		}
	}

	// Get unstaged changes
	unstagedCmd := exec.Command("git", "-C", g.repoRoot, "diff", "HEAD")
	unstagedOutput, err := unstagedCmd.CombinedOutput()
	if err != nil {
		return "", &GitError{
			Op:      "get-diff-unstaged",
			Command: unstagedCmd.String(),
			Output:  string(unstagedOutput),
			Err:     err,
		}
	}

	// Also get untracked files as a diff-like output
	var untrackedOutput []byte
	statusCmd := exec.Command("git", "-C", g.repoRoot, "status", "--porcelain")
	statusOut, err := statusCmd.CombinedOutput()
	if err == nil {
		lines := strings.Split(string(statusOut), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "?? ") {
				file := strings.TrimPrefix(line, "?? ")
				content, _ := exec.Command("git", "-C", g.repoRoot, "diff", "--no-index", "/dev/null", strings.TrimSpace(file)).CombinedOutput()
				// Only include the actual diff part (skip the "diff --git" header)
				if len(content) > 0 {
					untrackedOutput = append(untrackedOutput, []byte("\n")...)
					untrackedOutput = append(untrackedOutput, content...)
				}
			}
		}
	}

	return string(stagedOutput) + string(unstagedOutput) + string(untrackedOutput), nil
}

// StageAndCommit stages all changes and commits with message.
func (g *Git) StageAndCommit(message string) error {
	// Stage all changes
	addCmd := exec.Command("git", "-C", g.repoRoot, "add", "-A")
	if output, err := addCmd.CombinedOutput(); err != nil {
		return &GitError{
			Op:      "stage",
			Command: addCmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}

	// Check if there are any staged changes to commit
	statusCmd := exec.Command("git", "-C", g.repoRoot, "diff", "--cached", "--quiet")
	if statusCmd.Run() == nil {
		// No staged changes, nothing to commit
		return nil
	}

	// Commit
	commitCmd := exec.Command("git", "-C", g.repoRoot, "commit", "-m", message)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return &GitError{
			Op:      "commit",
			Command: commitCmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return nil
}

// HardResetToHead resets to HEAD, discarding changes.
func (g *Git) HardResetToHead() error {
	cmd := exec.Command("git", "-C", g.repoRoot, "reset", "--hard", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &GitError{
			Op:      "hard-reset",
			Command: cmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return nil
}

// DeleteBranch deletes a branch (force if needed).
func (g *Git) DeleteBranch(name string) error {
	// Cannot delete current branch
	current, err := g.CurrentBranch()
	if err != nil {
		return err
	}
	if current == name {
		// Switch to base branch first
		if err := g.CheckoutBranch(g.baseBranch); err != nil {
			return fmt.Errorf("cannot delete current branch: checkout failed: %w", err)
		}
	}

	cmd := exec.Command("git", "-C", g.repoRoot, "branch", "-D", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &GitError{
			Op:      "delete-branch",
			Command: cmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return nil
}

// CheckoutBranch switches to an existing branch.
func (g *Git) CheckoutBranch(name string) error {
	cmd := exec.Command("git", "-C", g.repoRoot, "checkout", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &GitError{
			Op:      "checkout",
			Command: cmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return nil
}

// CurrentBranch returns the current branch name.
func (g *Git) CurrentBranch() (string, error) {
	cmd := exec.Command("git", "-C", g.repoRoot, "branch", "--show-current")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", &GitError{
			Op:      "current-branch",
			Command: cmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return strings.TrimSpace(string(output)), nil
}

// MergeBranch merges branch into base branch with message.
func (g *Git) MergeBranch(name string, message string) error {
	// Checkout base branch
	if err := g.CheckoutBranch(g.baseBranch); err != nil {
		return fmt.Errorf("checkout base branch: %w", err)
	}

	// Merge the branch (use --no-ff to ensure a merge commit is created)
	cmd := exec.Command("git", "-C", g.repoRoot, "merge", "--no-ff", name, "-m", message)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's a merge conflict
		if strings.Contains(string(output), "CONFLICT") {
			// Abort the merge
			abortCmd := exec.Command("git", "-C", g.repoRoot, "merge", "--abort")
			_ = abortCmd.Run() // Best effort

			return &GitError{
				Op:      "merge-conflict",
				Command: cmd.String(),
				Output:  string(output),
				Err:     fmt.Errorf("merge conflicts detected - manual resolution required"),
			}
		}

		return &GitError{
			Op:      "merge",
			Command: cmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return nil
}

// IsWorkingTreeDirty checks if there are uncommitted changes.
func (g *Git) IsWorkingTreeDirty() (bool, error) {
	cmd := exec.Command("git", "-C", g.repoRoot, "status", "--porcelain")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, &GitError{
			Op:      "status",
			Command: cmd.String(),
			Output:  string(output),
			Err:     err,
		}
	}
	return len(output) > 0, nil
}

// RepoRoot returns the absolute path to the repository root.
func (g *Git) RepoRoot() string {
	return g.repoRoot
}

// BaseBranch returns the detected base branch name.
func (g *Git) BaseBranch() string {
	return g.baseBranch
}

// HeadSHA returns the SHA of the current HEAD commit.
func (g *Git) HeadSHA() string {
	cmd := exec.Command("git", "-C", g.repoRoot, "rev-parse", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
