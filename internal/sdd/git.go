package sdd

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
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
	// MergeFF fast-forwards branch into the working tree's currently
	// checked-out branch. The caller is responsible for ensuring the
	// checkout is on the target branch (the controller in P3 will
	// pre-checkout before calling).
	MergeFF(branch string) error
	// Commit commits all staged changes in Dir with the given message.
	Commit(message string) error
	// DiffStat returns the `git diff --stat` output between two refs.
	DiffStat(from, to string) (string, error)
	// IsAncestor reports whether ancestor is an ancestor of descendant
	// (git merge-base --is-ancestor). Used by branch-base-guard.
	IsAncestor(ancestor, descendant string) (bool, error)
	// DeleteBranch removes a local branch (git branch -d). Best-effort; the
	// caller tolerates errors. Used by CleanupStale.
	DeleteBranch(branch string) error
	// ListSDDBranches lists all sdd/* branches (git for-each-ref
	// refs/heads/sdd/). Used by CleanupStale.
	ListSDDBranches() ([]string, error)
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

func (g CLIGitOps) MergeFF(branch string) error {
	_, err := g.run("merge", "--ff-only", branch)
	return err
}

func (g CLIGitOps) Commit(message string) error {
	_, err := g.run("commit", "-m", message)
	return err
}

func (g CLIGitOps) IsAncestor(ancestor, descendant string) (bool, error) {
	_, err := g.run("merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	// git merge-base --is-ancestor exits 1 when not an ancestor (not a real error).
	if strings.Contains(err.Error(), "exit status 1") {
		return false, nil
	}
	return false, err
}

func (g CLIGitOps) DeleteBranch(branch string) error {
	_, err := g.run("branch", "-d", branch)
	return err
}

func (g CLIGitOps) ListSDDBranches() ([]string, error) {
	out, err := g.run("for-each-ref", "--format=%(refname:short)", "refs/heads/sdd/")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

func (g CLIGitOps) DiffStat(from, to string) (string, error) {
	return g.run("diff", "--stat", from+".."+to)
}

// FakeGitOps is an in-memory GitOps for tests. It records every call and
// returns canned results. It is safe for concurrent use within a single test.
type FakeGitOps struct {
	mu        sync.Mutex
	refs      map[string]string          // ref -> sha
	calls     map[string][][]string      // subcommand -> args history
	worktrees map[string]bool            // path -> exists
	mergeErrs map[string]error           // branch -> error to return from MergeFF
	branches  map[string]bool            // branch -> exists
	diffStats map[string]string          // "from..to" -> canned DiffStat output
	ancestors map[string]map[string]bool // ancestor -> set of descendants
}

// NewFakeGitOps builds an empty fake.
func NewFakeGitOps() *FakeGitOps {
	return &FakeGitOps{
		refs:      map[string]string{},
		calls:     map[string][][]string{},
		worktrees: map[string]bool{},
		mergeErrs: map[string]error{},
		branches:  map[string]bool{},
		diffStats: map[string]string{},
		ancestors: map[string]map[string]bool{},
	}
}

func (f *FakeGitOps) record(sub string, args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[sub] = append(f.calls[sub], args)
}

// Calls returns the recorded args for a git subcommand (e.g. "worktree").
func (f *FakeGitOps) Calls(sub string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls[sub]...)
}

// SetRef makes RevParse(ref) return sha.
func (f *FakeGitOps) SetRef(ref, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs[ref] = sha
	if strings.HasPrefix(ref, "refs/heads/") {
		f.branches[strings.TrimPrefix(ref, "refs/heads/")] = true
	} else if !strings.HasPrefix(ref, "refs/") && ref != "HEAD" {
		f.branches[ref] = true
	}
}

// SetBranch marks a branch as existing without a ref.
func (f *FakeGitOps) SetBranch(branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches[branch] = true
}

// SetMergeFFError makes MergeFF(branch) return err.
func (f *FakeGitOps) SetMergeFFError(branch string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mergeErrs[branch] = err
}

func (f *FakeGitOps) RevParse(ref string) (string, error) {
	f.record("rev-parse", []string{ref})
	f.mu.Lock()
	defer f.mu.Unlock()
	if sha, ok := f.refs[ref]; ok {
		return sha, nil
	}
	if sha, ok := f.refs["refs/heads/"+ref]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("sdd git fake: unknown ref %q", ref)
}

func (f *FakeGitOps) CurrentBranch() (string, error) {
	f.record("rev-parse", []string{"--abbrev-ref", "HEAD"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.refs["HEAD_BRANCH"]; ok {
		return b, nil
	}
	return "main", nil
}

func (f *FakeGitOps) BranchExists(branch string) bool {
	f.record("rev-parse", []string{"--verify", "--quiet", "refs/heads/" + branch})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.branches[branch]
}

func (f *FakeGitOps) WorktreeAdd(path, branch, startPoint string) error {
	f.record("worktree", []string{"add", "-b", branch, path, startPoint})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.branches[branch] {
		return fmt.Errorf("sdd git fake: branch %s already exists", branch)
	}
	f.branches[branch] = true
	f.worktrees[path] = true
	return nil
}

func (f *FakeGitOps) WorktreeRemove(path string) error {
	f.record("worktree", []string{"remove", "--force", path})
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.worktrees, path)
	return nil
}

func (f *FakeGitOps) MergeFF(branch string) error {
	f.record("merge", []string{"--ff-only", branch})
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.mergeErrs[branch]; ok {
		return err
	}
	return nil
}

func (f *FakeGitOps) Commit(message string) error {
	f.record("commit", []string{"-m", message})
	return nil
}

// SetAncestor records that ancestor is an ancestor of descendant.
func (f *FakeGitOps) SetAncestor(ancestor, descendant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ancestors[ancestor] == nil {
		f.ancestors[ancestor] = map[string]bool{}
	}
	f.ancestors[ancestor][descendant] = true
}

func (f *FakeGitOps) IsAncestor(ancestor, descendant string) (bool, error) {
	f.record("merge-base", []string{"--is-ancestor", ancestor, descendant})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ancestors[ancestor][descendant], nil
}

// SetDiffStat makes DiffStat(from, to) return output.
func (f *FakeGitOps) SetDiffStat(from, to, output string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.diffStats == nil {
		f.diffStats = map[string]string{}
	}
	f.diffStats[from+".."+to] = output
}

func (f *FakeGitOps) DiffStat(from, to string) (string, error) {
	f.record("diff", []string{"--stat", from + ".." + to})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.diffStats == nil {
		f.diffStats = map[string]string{}
	}
	return f.diffStats[from+".."+to], nil
}

func (f *FakeGitOps) DeleteBranch(branch string) error {
	f.record("branch", []string{"-d", branch})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.branches[branch] {
		return fmt.Errorf("sdd git fake: branch %s does not exist", branch)
	}
	delete(f.branches, branch)
	return nil
}

func (f *FakeGitOps) ListSDDBranches() ([]string, error) {
	f.record("for-each-ref", []string{"refs/heads/sdd/"})
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for b := range f.branches {
		if strings.HasPrefix(b, "sdd/") {
			out = append(out, b)
		}
	}
	return out, nil
}
