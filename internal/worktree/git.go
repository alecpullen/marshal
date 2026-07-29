package worktree

import (
	"fmt"
	"os/exec"
	"strings"
)

// GitOps is the seam over git. Every pipeline subsystem depends on this
// interface, never on exec.Command directly, so the controller can be
// tested without a real repository. Each method takes the working
// directory it runs in: the controller resolves refs against the main
// checkout while committing inside the run's worktree.
type GitOps interface {
	RevParse(dir, ref string) (string, error)
	MergeBase(dir, a, b string) (string, error)
	BranchExists(dir, branch string) bool
	WorktreeAdd(dir, path, branch, startPoint string) error
	WorktreeList(dir string) ([]string, error)
	IsDirty(dir string) (bool, error)
	// CommitAll stages every change (including untracked files) and
	// commits it, returning the new HEAD SHA. It returns an error when
	// there is nothing to commit — the caller checks IsDirty first.
	CommitAll(dir, message string) (string, error)
	LogOneline(dir, rng string) (string, error)
	DiffStat(dir, rng string) (string, error)
	Diff(dir, rng string, contextLines int) (string, error)
}

// CLIGitOps shells out to the git CLI.
type CLIGitOps struct{}

func (CLIGitOps) run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("worktree git: %s: %w: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

func (g CLIGitOps) RevParse(dir, ref string) (string, error) {
	return g.run(dir, "rev-parse", ref)
}

func (g CLIGitOps) MergeBase(dir, a, b string) (string, error) {
	return g.run(dir, "merge-base", a, b)
}

func (g CLIGitOps) BranchExists(dir, branch string) bool {
	_, err := g.run(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func (g CLIGitOps) WorktreeAdd(dir, path, branch, startPoint string) error {
	_, err := g.run(dir, "worktree", "add", "-b", branch, path, startPoint)
	return err
}

func (g CLIGitOps) WorktreeList(dir string) ([]string, error) {
	out, err := g.run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func (g CLIGitOps) IsDirty(dir string) (bool, error) {
	out, err := g.run(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (g CLIGitOps) CommitAll(dir, message string) (string, error) {
	if _, err := g.run(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := g.run(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return g.RevParse(dir, "HEAD")
}

func (g CLIGitOps) LogOneline(dir, rng string) (string, error) {
	return g.run(dir, "log", "--oneline", rng)
}

func (g CLIGitOps) DiffStat(dir, rng string) (string, error) {
	return g.run(dir, "diff", "--stat", rng)
}

func (g CLIGitOps) Diff(dir, rng string, contextLines int) (string, error) {
	return g.run(dir, "diff", fmt.Sprintf("-U%d", contextLines), rng)
}
