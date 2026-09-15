package worktree

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"marshal/internal/app/config"
)

// Worktree is the isolated checkout a run works in. The user's main
// checkout is never touched.
type Worktree struct {
	Path   string
	Branch string
	Base   string
	// Fresh is true when this call created the worktree (the branch did not
	// exist and WorktreeAdd ran). Callers execute SetupPlan hooks only when
	// Fresh is set; a resumed worktree is already set up.
	Fresh bool
	// SeedWarnings holds non-fatal problems from seeding git-ignored paths
	// into a fresh worktree. Empty when Fresh is false or seeding was clean.
	SeedWarnings []string
	// SetupPlan lists the setup hooks the caller must run inside the
	// worktree when Fresh is true. Zero when Fresh is false.
	SetupPlan SetupPlan
}

// WorktreeSetup carries what happens to a worktree at creation time.
// Seeding runs inside EnsureWorktree; hooks are planned here but executed
// by the caller (hook execution needs a CommandRunner, which worktree
// cannot import).
type WorktreeSetup struct {
	Config config.WorktreeConfig
}

// SetupPlan is what a caller must execute after EnsureWorktree reports a
// fresh worktree.
type SetupPlan struct {
	Hooks []config.WorktreeSetupHook
}

// EnsureWorktree returns the run's worktree, creating it on the first call
// and reusing it on resume. A branch that exists without its worktree is an
// error the human must resolve: silently reusing the branch would append a
// resumed run's commits to an unrelated one.
//
// On the creation branch the worktree is seeded from setup.Config and the
// configured hooks are returned in Worktree.SetupPlan for the caller to
// execute (hook execution needs a CommandRunner this package cannot import).
func EnsureWorktree(git GitOps, repoRoot, worktreesDir, branch, startRef string, setup WorktreeSetup) (Worktree, error) {
	path := filepath.Join(worktreesDir, strings.ReplaceAll(branch, "/", "-"))
	if git.BranchExists(repoRoot, branch) {
		existing, err := git.WorktreeList(repoRoot)
		if err != nil {
			return Worktree{}, fmt.Errorf("worktree: list: %w", err)
		}
		for _, p := range existing {
			if p == path {
				base, err := git.RevParse(repoRoot, branch)
				if err != nil {
					return Worktree{}, fmt.Errorf("worktree: rev-parse %s: %w", branch, err)
				}
				return Worktree{Path: path, Branch: branch, Base: base}, nil
			}
		}
		return Worktree{}, fmt.Errorf("worktree: branch %s already exists but has no worktree at %s; delete the branch or remove it by hand before resuming", branch, path)
	}
	base, err := git.RevParse(repoRoot, startRef)
	if err != nil {
		return Worktree{}, fmt.Errorf("worktree: rev-parse %s: %w", startRef, err)
	}
	if err := git.WorktreeAdd(repoRoot, path, branch, base); err != nil {
		return Worktree{}, fmt.Errorf("worktree: add %s: %w", path, err)
	}
	seedWarnings := SeedIntoWorktree(setup.Config, git, repoRoot, path)
	return Worktree{
		Path:         path,
		Branch:       branch,
		Base:         base,
		Fresh:        true,
		SeedWarnings: seedWarnings,
		SetupPlan:    SetupPlan{Hooks: setup.Config.SetupHooks},
	}, nil
}

// FakeGitOps is the in-memory GitOps used by every test in this package
// that does not need a real repository. Refs maps ref names to SHAs;
// Heads maps a directory to its current HEAD; Commits records CommitAll
// calls in order.
type FakeGitOps struct {
	Refs      map[string]string
	Branches  map[string]bool
	Worktrees []string
	// WorktreeBranches maps a worktree path to the branch it is attached to.
	// Backs WorktreeBranch for ownership verification.
	WorktreeBranches map[string]string
	Added            []string
	Heads            map[string]string
	Commits          []string
	Dirty            bool
	// DirtyDirs maps a directory to its dirty state. When a dir is present,
	// IsDirty returns that value; otherwise it falls back to the global
	// Dirty. CommitAll clears the entry for the committed dir, so a committed
	// worktree reads as clean while the project root can stay dirty.
	DirtyDirs map[string]bool
	// NextHead, when non-empty, is popped as the SHA of the next CommitAll.
	NextHead  []string
	LogOut    string
	LogErr    error
	StatOut   string
	DiffOut   string
	CommitErr error
	// Removed records paths passed to WorktreeRemove; RemoveErr simulates
	// git refusing (e.g. dirty worktree). Pruned records a prune call.
	Removed   []string
	Pruned    bool
	RemoveErr error
	// Merges records Merge calls by branch; MergeErr, when set, is returned
	// by Merge so tests can drive the conflict path. MergeFunc, when set,
	// runs after the call is recorded and overrides MergeErr — tests use it
	// to move HEAD the way a real merge does.
	Merges      []string
	MergeErr    error
	MergeFunc   func(dir, branch string) error
	Aborts      int
	ResetMerges int
	Deleted     []string
	// DiffStatOut backs DiffNumstat; DiffOut backs Diff and DiffPath.
	DiffStatOut string
	// AbbrevRef is returned by RevParse for "--abbrev-ref HEAD".
	AbbrevRef string
	// SquashMerges records SquashMerge calls by branch; SquashMergeErr, when
	// set, is returned by SquashMerge so tests can drive the conflict path.
	SquashMerges    []string
	SquashMergeErr  error
	SquashMergeFunc func(dir, branch string) error
	// AheadBehindFunc, BranchAgeFunc, ListWorktreesFunc and CheckIgnoreFunc
	// back the newer GitOps methods. Nil falls back to a zero-value success
	// default, matching the fake's nil-func convention: ListWorktrees
	// derives from Worktrees/WorktreeBranches, the rest return zeros.
	AheadBehindFunc   func(dir, base, branch string) (ahead, behind int, err error)
	BranchAgeFunc     func(dir, branch string) (time.Time, error)
	ListWorktreesFunc func(dir string) ([]WorktreeInfo, error)
	CheckIgnoreFunc   func(dir, path string) (bool, error)
	// DeleteBranchFunc, when set, is returned by BranchDelete after the
	// call is recorded — tests use it to prove cleanup failures stay
	// best-effort.
	DeleteBranchFunc func(dir, branch string, force bool) error
	// DeletedForce parallels Deleted, recording the force flag of each
	// BranchDelete call.
	DeletedForce []bool
	// Calls records every method invocation in order, so tests can assert
	// a consumer only ever reads (e.g. only Diff, never WorktreeAdd).
	calls []string
}

// Calls returns the recorded method-invocation log in order.
func (f *FakeGitOps) Calls() []string { return f.calls }

func NewFakeGitOps() *FakeGitOps {
	return &FakeGitOps{
		Refs:             map[string]string{},
		Branches:         map[string]bool{},
		Heads:            map[string]string{},
		DirtyDirs:        map[string]bool{},
		WorktreeBranches: map[string]string{},
	}
}

// record appends the method name to the call log.
func (f *FakeGitOps) record(name string) { f.calls = append(f.calls, name) }

func (f *FakeGitOps) RevParse(dir, ref string) (string, error) {
	f.record("RevParse")
	if ref == "--abbrev-ref HEAD" {
		return f.AbbrevRef, nil
	}
	if sha, ok := f.Refs[ref]; ok {
		return sha, nil
	}
	if ref == "HEAD" {
		if sha, ok := f.Heads[dir]; ok {
			return sha, nil
		}
	}
	return "", fmt.Errorf("fake git: unknown ref %q", ref)
}

func (f *FakeGitOps) MergeBase(dir, a, b string) (string, error) {
	f.record("MergeBase")
	if sha, ok := f.Refs["merge-base"]; ok {
		return sha, nil
	}
	return f.RevParse(dir, a)
}

func (f *FakeGitOps) BranchExists(dir, branch string) bool {
	f.record("BranchExists")
	return f.Branches[branch]
}

func (f *FakeGitOps) WorktreeAdd(dir, path, branch, startPoint string) error {
	f.record("WorktreeAdd")
	f.Added = append(f.Added, path)
	f.Branches[branch] = true
	f.Worktrees = append(f.Worktrees, path)
	f.WorktreeBranches[path] = branch
	f.Heads[path] = startPoint
	return nil
}

func (f *FakeGitOps) WorktreeList(dir string) ([]string, error) {
	f.record("WorktreeList")
	return f.Worktrees, nil
}

// WorktreeBranch returns the branch recorded for path in WorktreeBranches,
// or an error when path is not a registered worktree.
func (f *FakeGitOps) WorktreeBranch(dir, path string) (string, error) {
	f.record("WorktreeBranch")
	if b, ok := f.WorktreeBranches[path]; ok {
		return b, nil
	}
	return "", fmt.Errorf("fake git: %s is not a registered worktree", path)
}

func (f *FakeGitOps) WorktreeRemove(dir, path string) error {
	f.record("WorktreeRemove")
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	f.Removed = append(f.Removed, path)
	// Mirror real git: removing a worktree unregisters it but leaves the
	// branch (and its ref) intact — BranchDelete is what removes the branch.
	for i, p := range f.Worktrees {
		if p == path {
			f.Worktrees = append(f.Worktrees[:i], f.Worktrees[i+1:]...)
			break
		}
	}
	delete(f.WorktreeBranches, path)
	return nil
}

func (f *FakeGitOps) WorktreePrune(dir string) error {
	f.record("WorktreePrune")
	f.Pruned = true
	return nil
}

// IsDirty reports the Dirty field. Dirty is sticky: CommitAll does not
// clear it, so a scripted multi-task run keeps producing commits without
// the test resetting the flag between tasks.
func (f *FakeGitOps) IsDirty(dir string) (bool, error) {
	f.record("IsDirty")
	if v, ok := f.DirtyDirs[dir]; ok {
		return v, nil
	}
	return f.Dirty, nil
}

func (f *FakeGitOps) CommitAll(dir, message string) (string, error) {
	f.record("CommitAll")
	if f.CommitErr != nil {
		return "", f.CommitErr
	}
	f.Commits = append(f.Commits, message)
	delete(f.DirtyDirs, dir)
	head := fmt.Sprintf("commit%03d0000000000000000000000000000000", len(f.Commits))
	if len(f.NextHead) > 0 {
		head = f.NextHead[0]
		f.NextHead = f.NextHead[1:]
	}
	f.Heads[dir] = head
	return head, nil
}

func (f *FakeGitOps) LogOneline(dir, rng string) (string, error) {
	f.record("LogOneline")
	return f.LogOut, f.LogErr
}
func (f *FakeGitOps) DiffStat(dir, rng string) (string, error) {
	f.record("DiffStat")
	return f.StatOut, nil
}
func (f *FakeGitOps) Diff(dir, rng string, contextLines int) (string, error) {
	f.record("Diff")
	return f.DiffOut, nil
}

func (f *FakeGitOps) Merge(dir, branch string) error {
	f.record("Merge")
	f.Merges = append(f.Merges, branch)
	if f.MergeFunc != nil {
		return f.MergeFunc(dir, branch)
	}
	return f.MergeErr
}

func (f *FakeGitOps) MergeAbort(dir string) error {
	f.record("MergeAbort")
	f.Aborts++
	return nil
}

func (f *FakeGitOps) ResetMerge(dir string) error {
	f.record("ResetMerge")
	f.ResetMerges++
	return nil
}

func (f *FakeGitOps) BranchDelete(dir, branch string, force bool) error {
	f.record("BranchDelete")
	f.Deleted = append(f.Deleted, branch)
	f.DeletedForce = append(f.DeletedForce, force)
	if f.DeleteBranchFunc != nil {
		return f.DeleteBranchFunc(dir, branch, force)
	}
	return nil
}

func (f *FakeGitOps) DiffNumstat(dir, rng string) (string, error) {
	f.record("DiffNumstat")
	return f.DiffStatOut, nil
}

func (f *FakeGitOps) DiffPath(dir, rng, path string, contextLines int) (string, error) {
	f.record("DiffPath")
	return f.DiffOut, nil
}

func (f *FakeGitOps) SquashMerge(dir, branch string) error {
	f.record("SquashMerge")
	if f.SquashMergeFunc != nil {
		return f.SquashMergeFunc(dir, branch)
	}
	f.SquashMerges = append(f.SquashMerges, branch)
	return f.SquashMergeErr
}

func (f *FakeGitOps) AheadBehind(dir, base, branch string) (int, int, error) {
	f.record("AheadBehind")
	if f.AheadBehindFunc != nil {
		return f.AheadBehindFunc(dir, base, branch)
	}
	return 0, 0, nil
}

func (f *FakeGitOps) BranchAge(dir, branch string) (time.Time, error) {
	f.record("BranchAge")
	if f.BranchAgeFunc != nil {
		return f.BranchAgeFunc(dir, branch)
	}
	return time.Time{}, nil
}

func (f *FakeGitOps) ListWorktrees(dir string) ([]WorktreeInfo, error) {
	f.record("ListWorktrees")
	if f.ListWorktreesFunc != nil {
		return f.ListWorktreesFunc(dir)
	}
	infos := make([]WorktreeInfo, 0, len(f.Worktrees))
	for _, p := range f.Worktrees {
		infos = append(infos, WorktreeInfo{Path: p, Branch: f.WorktreeBranches[p]})
	}
	return infos, nil
}

func (f *FakeGitOps) CheckIgnore(dir, path string) (bool, error) {
	f.record("CheckIgnore")
	if f.CheckIgnoreFunc != nil {
		return f.CheckIgnoreFunc(dir, path)
	}
	return false, nil
}
