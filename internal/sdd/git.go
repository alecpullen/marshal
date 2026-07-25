package sdd

import (
	"fmt"
	"os/exec"
	"strings"
)

// GitOps is the typed seam over git operations. Every SDD subsystem that
// touches git depends on this interface, never on exec.Command. The real
// implementation is CLIGitOps (shells out); tests use FakeGitOps (Task 4).
// Spec unknown 5: keep shelling out (matches existing
// internal/agent/sdd/worktree.go) but wrap every call for testability.
type GitOps interface {
	// RevParse resolves a ref (branch, HEAD, sha) to a full SHA.
	RevParse(ref string) (string, error)
	// CurrentBranch returns the checked-out branch name of the Dir repo.
	CurrentBranch() (string, error)
	// BranchExists reports whether a local branch exists.
	BranchExists(branch string) bool
	// WorktreeAdd creates a worktree at path on a new branch rooted at
	// startPoint (a ref/sha).
	WorktreeAdd(path, branch, startPoint string) error
	// WorktreeRemove force-removes a worktree path.
	WorktreeRemove(path string) error
	// MergeFF fast-forwards branch onto target (target must be an ancestor).
	MergeFF(branch, target string) error
	// Commit commits all staged changes in Dir with the given message.
	Commit(message string) error
	// DiffStat returns the `git diff --stat` output between two refs.
	DiffStat(from, to string) (string, error)
}

// CLIGitOps shells out to the git CLI. Dir is the repo working directory.
type CLIGitOps struct {
	Dir string
}

func (g CLIGitOps) run(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("sdd git: %s: %w: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

func (g CLIGitOps) RevParse(ref string) (string, error) {
	return g.run("rev-parse", ref)
}

func (g CLIGitOps) CurrentBranch() (string, error) {
	return g.run("rev-parse", "--abbrev-ref", "HEAD")
}

func (g CLIGitOps) BranchExists(branch string) bool {
	_, err := g.run("rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func (g CLIGitOps) WorktreeAdd(path, branch, startPoint string) error {
	_, err := g.run("worktree", "add", "-b", branch, path, startPoint)
	return err
}

func (g CLIGitOps) WorktreeRemove(path string) error {
	_, err := g.run("worktree", "remove", "--force", path)
	return err
}

func (g CLIGitOps) MergeFF(branch, target string) error {
	_, err := g.run("merge", "--ff-only", branch)
	if err != nil {
		return err
	}
	return nil
}

func (g CLIGitOps) Commit(message string) error {
	_, err := g.run("commit", "-m", message)
	return err
}

func (g CLIGitOps) DiffStat(from, to string) (string, error) {
	return g.run("diff", "--stat", from+".."+to)
}
