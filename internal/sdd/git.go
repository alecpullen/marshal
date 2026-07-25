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

// FakeGitOps is an in-memory GitOps for tests. It records every call and
// returns canned results. It is safe for concurrent use within a single test.
type FakeGitOps struct {
	mu        sync.Mutex
	refs      map[string]string     // ref -> sha
	calls     map[string][][]string // subcommand -> args history
	worktrees map[string]bool       // path -> exists
	mergeErrs map[string]error      // branch -> error to return from MergeFF
	branches  map[string]bool       // branch -> exists
}

// NewFakeGitOps builds an empty fake.
func NewFakeGitOps() *FakeGitOps {
	return &FakeGitOps{
		refs:      map[string]string{},
		calls:     map[string][][]string{},
		worktrees: map[string]bool{},
		mergeErrs: map[string]error{},
		branches:  map[string]bool{},
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

// SetMergeFFError makes MergeFF(branch, _) return err.
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

func (f *FakeGitOps) MergeFF(branch, target string) error {
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

func (f *FakeGitOps) DiffStat(from, to string) (string, error) {
	f.record("diff", []string{"--stat", from + ".." + to})
	return "", nil
}
