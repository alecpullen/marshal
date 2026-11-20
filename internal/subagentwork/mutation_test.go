package subagentwork

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"marshal/internal/worktree"
)

// testIdleGate is an explicit, deterministic idle lease: it records what the
// service asked for and hands back a release function, or a refusal the test
// chose. Nothing here is an implicit approval.
type testIdleGate struct {
	mu       sync.Mutex
	calls    int
	owners   []string
	roots    [][]string
	releases int
	err      error
}

func (g *testIdleGate) AcquireIdle(_ context.Context, owner string, roots []string) (func(), error) {
	g.mu.Lock()
	g.calls++
	g.owners = append(g.owners, owner)
	g.roots = append(g.roots, append([]string(nil), roots...))
	err := g.err
	g.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		g.mu.Lock()
		g.releases++
		g.mu.Unlock()
	}, nil
}

func (g *testIdleGate) counts() (calls, releases int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls, g.releases
}

// testAdmission is the opaque proof a mutation is allowed. Committing it spends
// it on an accepted mutation; releasing it hands it back unused.
type testAdmission struct {
	mu        sync.Mutex
	commits   int
	releases  int
	commitErr error
}

func (a *testAdmission) Commit() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commits++
	return a.commitErr
}

func (a *testAdmission) Release() {
	a.mu.Lock()
	a.releases++
	a.mu.Unlock()
}

func (a *testAdmission) counts() (commits, releases int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.commits, a.releases
}

type testAdmitter struct {
	mu        sync.Mutex
	keys      []string
	admission *testAdmission
	err       error
}

func (a *testAdmitter) Admit(_ context.Context, key string) (Admission, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys = append(a.keys, key)
	if a.err != nil {
		return nil, a.err
	}
	if a.admission == nil {
		a.admission = &testAdmission{}
	}
	return a.admission, nil
}

func (a *testAdmitter) last() *testAdmission {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admission
}

// mutationHarness is the deterministic dependency set every mutation test uses:
// explicit leases, an explicit admission, and recorders for the snapshot and
// rescan hooks.
type mutationHarness struct {
	idle        *testIdleGate
	admitter    *testAdmitter
	snapshotErr error
	snapshots   []string
	rescans     []string
}

func newMutationHarness() *mutationHarness {
	return &mutationHarness{idle: &testIdleGate{}, admitter: &testAdmitter{}}
}

func (h *mutationHarness) deps() MutationDeps {
	return MutationDeps{
		Idle: h.idle,
		Snapshot: func(_ context.Context, root string) (string, error) {
			if h.snapshotErr != nil {
				return "", h.snapshotErr
			}
			h.snapshots = append(h.snapshots, root)
			return "snapshot-" + filepath.Base(root), nil
		},
		Rescan: func(root string) { h.rescans = append(h.rescans, root) },
		Admit:  h.admitter.Admit,
	}
}

// faultGit delegates to real git and fails one operation on demand, which is how
// a crash at a mutation boundary is modelled without a real crash.
type faultGit struct {
	worktree.CLIGitOps

	failMerge          bool
	failWorktreeRemove bool
	failBranchDelete   bool

	mu           sync.Mutex
	mergeCalls   int
	removeCalls  int
	deleteCalls  int
	unmergedSeen []string
	onMergeFail  func(dir string)
}

func (g *faultGit) MergeExact(ctx context.Context, dir, candidate, message string) (string, error) {
	g.mu.Lock()
	g.mergeCalls++
	fail := g.failMerge
	g.failMerge = false
	g.mu.Unlock()
	if fail {
		return "", errors.New("injected merge failure")
	}
	result, err := g.CLIGitOps.MergeExact(ctx, dir, candidate, message)
	if err != nil && g.onMergeFail != nil {
		g.onMergeFail(dir)
	}
	return result, err
}

func (g *faultGit) WorktreeRemove(dir, path string) error {
	g.mu.Lock()
	g.removeCalls++
	fail := g.failWorktreeRemove
	g.failWorktreeRemove = false
	g.mu.Unlock()
	if fail {
		return errors.New("injected worktree removal failure")
	}
	return g.CLIGitOps.WorktreeRemove(dir, path)
}

func (g *faultGit) BranchDelete(dir, branch string, force bool) error {
	g.mu.Lock()
	g.deleteCalls++
	fail := g.failBranchDelete
	g.failBranchDelete = false
	g.mu.Unlock()
	if fail {
		return errors.New("injected branch deletion failure")
	}
	return g.CLIGitOps.BranchDelete(dir, branch, force)
}

func (g *faultGit) counts() (merges, removes, deletes int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mergeCalls, g.removeCalls, g.deleteCalls
}

// faultStore fails one compare-and-swap whose record matches, modelling a crash
// between a git mutation and the journal write that would record it.
type faultStore struct {
	Store
	mu       sync.Mutex
	failWhen func(Record) bool
	failures int
}

var errInjectedSwap = errors.New("injected record write failure")

func (s *faultStore) CompareAndSwap(ctx context.Context, rec Record, expected uint64) (Record, error) {
	s.mu.Lock()
	fail := s.failWhen != nil && s.failWhen(rec)
	if fail {
		s.failWhen = nil
		s.failures++
	}
	s.mu.Unlock()
	if fail {
		return Record{}, errInjectedSwap
	}
	return s.Store.CompareAndSwap(ctx, rec, expected)
}

// prepareChild allocates one owned child for key and commits a single file in
// it, then records the requested execution outcome. An empty file means the
// child committed nothing.
func prepareChild(t *testing.T, svc *Service, f *gitFixture, key, file, content, outcome string) Record {
	t.Helper()
	ctx := context.Background()
	req := isolatedRequest(f.dir, f.dir)
	req.TaskKey = key
	req.InvocationID = "inv-" + key
	rec, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare(%s): %v", key, err)
	}
	if file != "" {
		writeIn(t, rec.Binding.Root, file, content)
		gitIn(t, rec.Binding.Root, "add", "-A")
		gitIn(t, rec.Binding.Root, "commit", "-m", "child work "+key)
	}
	rec, err = svc.Complete(ctx, rec.OwnerSessionID, rec.TaskKey, Completion{Outcome: outcome, ReportMessageID: 7})
	if err != nil {
		t.Fatalf("Complete(%s): %v", key, err)
	}
	return rec
}

func mutationFor(rec Record) Mutation {
	return Mutation{OwnerSessionID: rec.OwnerSessionID, TaskKey: rec.TaskKey, ExpectedHead: rec.Candidate}
}

func mustRecord(t *testing.T, store Store, owner, key string) Record {
	t.Helper()
	rec, err := store.Get(context.Background(), owner, key)
	if err != nil {
		t.Fatalf("Get(%s/%s): %v", owner, key, err)
	}
	return rec
}

// gitTry runs git in dir and reports its error instead of failing the test, so a
// fixture can stage a refusal.
func gitTry(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// operationMerges lists the commits in dir's history that carry this package's
// operation trailer: the merges a mutation actually created.
func operationMerges(t *testing.T, dir string) []string {
	t.Helper()
	out := gitIn(t, dir, "log", "--format=%H%x1f%B%x1e")
	var merges []string
	for _, entry := range strings.Split(out, "\x1e") {
		parts := strings.SplitN(entry, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.Contains(parts[1], "Marshal-Subagent-Operation:") {
			merges = append(merges, strings.TrimSpace(parts[0]))
		}
	}
	return merges
}

// requireNoMutation pins the refusal contract: the target never moved, no merge
// was created and the owned source artifacts are still there.
func requireNoMutation(t *testing.T, store Store, rec Record, target string, before string) Record {
	t.Helper()
	if head := gitIn(t, target, "rev-parse", "HEAD"); head != before {
		t.Fatalf("the target moved to %s during a refusal, want %s", head, before)
	}
	if merges := operationMerges(t, target); len(merges) != 0 {
		t.Fatalf("a refusal created merge commits: %v", merges)
	}
	if state, err := (worktree.CLIGitOps{}).MergeState(target); err != nil || state != "" {
		t.Fatalf("a refusal left merge state in the target: %q (%v)", state, err)
	}
	if _, err := os.Stat(rec.Binding.Root); err != nil {
		t.Fatalf("a refusal removed the owned worktree: %v", err)
	}
	if branches := ownedBranches(t, target); len(branches) != 1 {
		t.Fatalf("owned branches after a refusal = %v, want the one source branch", branches)
	}
	return mustRecord(t, store, rec.OwnerSessionID, rec.TaskKey)
}

// TestIntegrateTargetsCapturedFeatureWorktree pins the target: the merge lands in
// the parent checkout captured on the record — here a feature worktree — and it
// is a real merge commit recording both sides, while the project's own main
// checkout and unrelated untracked bytes are untouched.
func TestIntegrateTargetsCapturedFeatureWorktree(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	// Untracked bytes in the main checkout are unrelated to this task.
	writeIn(t, f.dir, "unrelated.txt", "keep me\n")
	mainHead := f.head()

	worktreesDir, err := worktree.AgentDir(f.dir)
	if err != nil {
		t.Fatalf("AgentDir: %v", err)
	}
	parent, err := worktree.EnsureWorktree(worktree.CLIGitOps{}, f.dir, worktreesDir, "feat/parent", "HEAD", worktree.WorktreeSetup{})
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	writeIn(t, parent.Path, "parent.txt", "parent\n")
	gitIn(t, parent.Path, "add", "-A")
	gitIn(t, parent.Path, "commit", "-m", "parent work")
	// Untracked bytes in the target checkout are also unrelated.
	writeIn(t, parent.Path, "target-scratch.txt", "target scratch\n")
	beforeTarget := gitIn(t, parent.Path, "rev-parse", "HEAD")

	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})
	req := isolatedRequest(f.dir, parent.Path)
	rec, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if rec.Target.Root != worktree.CanonicalPath(parent.Path) {
		t.Fatalf("target root = %q, want the captured feature worktree", rec.Target.Root)
	}
	writeIn(t, rec.Binding.Root, "child.txt", "child\n")
	gitIn(t, rec.Binding.Root, "add", "-A")
	gitIn(t, rec.Binding.Root, "commit", "-m", "child work")
	rec, err = svc.Complete(ctx, rec.OwnerSessionID, rec.TaskKey, Completion{Outcome: OutcomeDone})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	candidate := rec.Candidate

	h := newMutationHarness()
	integrated, err := svc.Integrate(ctx, mutationFor(rec), h.deps())
	if err != nil {
		t.Fatalf("Integrate: %v", err)
	}
	if integrated.Integration == nil || integrated.Integration.State != IntegrationIntegrated {
		t.Fatalf("integration = %+v, want an integrated receipt", integrated.Integration)
	}
	if integrated.Integration.ExpectedSource != candidate || integrated.Integration.BeforeTarget != beforeTarget {
		t.Fatalf("receipt = %+v, want the reviewed candidate %s on top of %s", integrated.Integration, candidate, beforeTarget)
	}
	result := gitIn(t, parent.Path, "rev-parse", "HEAD")
	if result == beforeTarget {
		t.Fatal("the target did not advance")
	}
	if integrated.Integration.ResultCommit != result {
		t.Fatalf("result commit = %s, want the target HEAD %s", integrated.Integration.ResultCommit, result)
	}
	parents := strings.Fields(gitIn(t, parent.Path, "rev-list", "--parents", "-n", "1", result))
	if len(parents) != 3 || parents[1] != beforeTarget || parents[2] != candidate {
		t.Fatalf("merge parents = %v, want [%s %s]", parents, beforeTarget, candidate)
	}
	// The candidate's own commit is still its own object, not a squash.
	gitIn(t, parent.Path, "cat-file", "-e", candidate+"^{commit}")

	// The project's main checkout is exactly as it was.
	if got := f.head(); got != mainHead {
		t.Fatalf("the main checkout moved to %s, want %s", got, mainHead)
	}
	if got := readIn(t, f.dir, "unrelated.txt"); got != "keep me\n" {
		t.Fatalf("the main checkout's untracked bytes = %q, want them untouched", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "child.txt")); !os.IsNotExist(err) {
		t.Fatal("the child's work reached the main checkout")
	}
	if got := readIn(t, parent.Path, "target-scratch.txt"); got != "target scratch\n" {
		t.Fatalf("the target's untracked bytes = %q, want them untouched", got)
	}

	// The integrated source is cleaned up only after the receipt is durable.
	if integrated.CleanupState != CleanupComplete {
		t.Fatalf("cleanup state = %q, want %q", integrated.CleanupState, CleanupComplete)
	}
	if _, err := os.Stat(rec.Binding.Root); !os.IsNotExist(err) {
		t.Fatal("the owned worktree survived a completed integration")
	}
	if branches := ownedBranches(t, f.dir); len(branches) != 0 {
		t.Fatalf("owned branches = %v, want none after integration", branches)
	}

	// Explicit leases and one committed admission: nothing implicit.
	if calls, releases := h.idle.counts(); calls != 1 || releases != 1 {
		t.Fatalf("idle leases = %d acquired, %d released, want one each", calls, releases)
	}
	if len(h.admitter.keys) != 1 {
		t.Fatalf("admissions requested = %d, want 1", len(h.admitter.keys))
	}
	if len(h.snapshots) != 1 || h.snapshots[0] != worktree.CanonicalPath(parent.Path) {
		t.Fatalf("snapshots = %v, want one of the exact target root", h.snapshots)
	}
	commits, released := h.admitter.last().counts()
	if commits != 1 || released != 0 {
		t.Fatalf("admission = %d commits, %d releases, want it spent exactly once", commits, released)
	}
}

// TestIntegrateSeveralCandidatesFromSameBase pins serial integration: a second
// child prepared from the same base is accepted after the first one advanced the
// target, and both source histories are preserved.
func TestIntegrateSeveralCandidatesFromSameBase(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	svc := NewService(NewMemoryStore(), worktree.CLIGitOps{})

	first := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
	second := prepareChild(t, svc, f, "task-2", "two.txt", "two\n", OutcomeDone)
	if first.Binding.BaseSHA != second.Binding.BaseSHA {
		t.Fatalf("fixture: the children started at %s and %s, want one base", first.Binding.BaseSHA, second.Binding.BaseSHA)
	}

	if _, err := svc.Integrate(ctx, mutationFor(first), newMutationHarness().deps()); err != nil {
		t.Fatalf("Integrate(first): %v", err)
	}
	afterFirst := f.head()

	target := f.head()
	secondReceipt, err := svc.Integrate(ctx, mutationFor(second), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("Integrate(second): %v", err)
	}
	if secondReceipt.Integration == nil || secondReceipt.Integration.State != IntegrationIntegrated {
		t.Fatalf("second integration = %+v, want integrated", secondReceipt.Integration)
	}
	if secondReceipt.Integration.BeforeTarget != afterFirst {
		t.Fatalf("second BeforeTarget = %s, want the advanced target %s", secondReceipt.Integration.BeforeTarget, afterFirst)
	}
	head := f.head()
	parents := strings.Fields(gitIn(t, f.dir, "rev-list", "--parents", "-n", "1", head))
	if len(parents) != 3 || parents[1] != afterFirst || parents[2] != second.Candidate {
		t.Fatalf("second merge parents = %v, want [%s %s]", parents, afterFirst, second.Candidate)
	}
	for _, candidate := range []string{first.Candidate, second.Candidate} {
		gitIn(t, f.dir, "cat-file", "-e", candidate+"^{commit}")
		gitIn(t, f.dir, "merge-base", "--is-ancestor", candidate, head)
	}
	if merges := operationMerges(t, f.dir); len(merges) != 2 {
		t.Fatalf("operation merges = %v, want exactly two", merges)
	}
	if branches := ownedBranches(t, f.dir); len(branches) != 0 {
		t.Fatalf("owned branches = %v, want both sources cleaned up", branches)
	}
	if target == head {
		t.Fatal("fixture: the second integration did not advance the target")
	}
}

// TestIntegrateRefusesSourceOrTargetDrift pins every fact that must be rechecked
// under the lease: a drift is a refusal with no mutation, never a best guess.
func TestIntegrateRefusesSourceOrTargetDrift(t *testing.T) {
	ctx := context.Background()

	t.Run("wrong owner", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()

		other := mutationFor(rec)
		other.OwnerSessionID = "someone-else"
		if _, err := svc.Integrate(ctx, other, newMutationHarness().deps()); !errors.Is(err, ErrRecordNotFound) {
			t.Fatalf("cross-owner Integrate error = %v, want ErrRecordNotFound", err)
		}
		got := requireNoMutation(t, store, rec, f.dir, before)
		if got.Integration != nil {
			t.Fatalf("a foreign owner's request wrote %+v onto the record", got.Integration)
		}
	})

	t.Run("source head moved", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()
		writeIn(t, rec.Binding.Root, "later.txt", "later\n")
		gitIn(t, rec.Binding.Root, "add", "-A")
		gitIn(t, rec.Binding.Root, "commit", "-m", "later work")

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate merged a source that had moved past the reviewed candidate")
		}
		got := requireNoMutation(t, store, rec, f.dir, before)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
	})

	t.Run("source registration replaced", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()
		gitIn(t, f.dir, "worktree", "remove", "--force", rec.Binding.Root)
		if err := os.MkdirAll(rec.Binding.Root, 0o700); err != nil {
			t.Fatalf("recreate the path: %v", err)
		}

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate accepted a directory that is no longer the owned worktree")
		}
		if head := f.head(); head != before {
			t.Fatalf("the target moved to %s during a refusal", head)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 0 {
			t.Fatalf("a refusal created merges: %v", merges)
		}
		got := mustRecord(t, store, rec.OwnerSessionID, rec.TaskKey)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
	})

	t.Run("source belongs to another repository", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()
		gitIn(t, f.dir, "worktree", "remove", "--force", rec.Binding.Root)
		if err := os.MkdirAll(rec.Binding.Root, 0o700); err != nil {
			t.Fatalf("recreate the path: %v", err)
		}
		gitIn(t, rec.Binding.Root, "init", "-b", rec.Binding.Branch)
		gitIn(t, rec.Binding.Root, "config", "user.email", "other@example.invalid")
		gitIn(t, rec.Binding.Root, "config", "user.name", "other")
		writeIn(t, rec.Binding.Root, "other.txt", "other\n")
		gitIn(t, rec.Binding.Root, "add", "-A")
		gitIn(t, rec.Binding.Root, "commit", "-m", "another repository")

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate accepted a workspace that belongs to another repository")
		}
		if head := f.head(); head != before {
			t.Fatalf("the target moved to %s during a refusal", head)
		}
		got := mustRecord(t, store, rec.OwnerSessionID, rec.TaskKey)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
	})

	t.Run("target branch changed", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()
		gitIn(t, f.dir, "checkout", "-b", "elsewhere")

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate followed the target onto another branch")
		}
		got := requireNoMutation(t, store, rec, f.dir, before)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
	})

	t.Run("dirty source", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()
		writeIn(t, rec.Binding.Root, "scratch.txt", "uncommitted\n")

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate merged a source with uncommitted work")
		}
		got := requireNoMutation(t, store, rec, f.dir, before)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
	})

	t.Run("dirty target worktree", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()
		writeIn(t, f.dir, "base.txt", "operator work in progress\n")

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate merged into a target with modified tracked files")
		}
		got := requireNoMutation(t, store, rec, f.dir, before)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
		if content := readIn(t, f.dir, "base.txt"); content != "operator work in progress\n" {
			t.Fatalf("the target's work in progress = %q, want it untouched", content)
		}
	})

	t.Run("dirty target index", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		writeIn(t, f.dir, "staged.txt", "staged\n")
		gitIn(t, f.dir, "add", "-A")
		before := f.head()

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate merged into a target with a staged change")
		}
		got := requireNoMutation(t, store, rec, f.dir, before)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
	})

	t.Run("merge in progress in the target", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		// A different operation is merging in the target.
		gitIn(t, f.dir, "checkout", "-b", "other")
		writeIn(t, f.dir, "base.txt", "other side\n")
		gitIn(t, f.dir, "add", "-A")
		gitIn(t, f.dir, "commit", "-m", "other side")
		other := gitIn(t, f.dir, "rev-parse", "HEAD")
		gitIn(t, f.dir, "checkout", "main")
		writeIn(t, f.dir, "base.txt", "main side\n")
		gitIn(t, f.dir, "add", "-A")
		gitIn(t, f.dir, "commit", "-m", "main side")
		before := f.head()
		if _, err := gitTry(t, f.dir, "merge", "other"); err == nil {
			t.Fatal("fixture: the foreign merge did not conflict")
		}

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate started a merge while the target was merging")
		}
		// The foreign merge is still there: it is not this operation's to abort.
		state, err := (worktree.CLIGitOps{}).MergeState(f.dir)
		if err != nil {
			t.Fatalf("MergeState: %v", err)
		}
		info := worktree.ParseMergeState(state)
		if !info.HasHead(other) {
			t.Fatalf("merge state = %q, want the foreign merge of %s intact", state, other)
		}
		if head := f.head(); head != before {
			t.Fatalf("the target HEAD moved to %s during a refusal", head)
		}
		got := mustRecord(t, store, rec.OwnerSessionID, rec.TaskKey)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
	})

	t.Run("untracked file the merge would overwrite", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		rec := prepareChild(t, svc, f, "task-1", "clash.txt", "child\n", OutcomeDone)
		writeIn(t, f.dir, "clash.txt", "target untracked\n")
		before := f.head()

		if _, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate merged over an untracked file")
		}
		got := requireNoMutation(t, store, rec, f.dir, before)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
		if content := readIn(t, f.dir, "clash.txt"); content != "target untracked\n" {
			t.Fatalf("the untracked file = %q, want it untouched", content)
		}
	})
}

// TestIntegrateConflictRestoresOnlyOwnMerge pins conflict handling: the merge
// this operation started is aborted, the target returns to its exact pre-
// operation commit and index, both branches survive, and the reported paths are
// git's own unmerged listing.
func TestIntegrateConflictRestoresOnlyOwnMerge(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	f.write("shared.txt", "base shared\n")
	f.git("add", "-A")
	f.git("commit", "-m", "shared base")

	fg := &faultGit{CLIGitOps: worktree.CLIGitOps{}}
	fg.onMergeFail = func(dir string) {
		out, err := gitTry(t, dir, "diff", "--name-only", "--diff-filter=U")
		if err != nil {
			t.Errorf("list unmerged paths: %v", err)
		}
		fg.unmergedSeen = strings.Fields(out)
	}
	store := NewMemoryStore()
	svc := NewService(store, fg)
	rec := prepareChild(t, svc, f, "task-1", "shared.txt", "child shared\n", OutcomeDone)

	// The target changes the same file, so merging the candidate conflicts.
	f.write("shared.txt", "target shared\n")
	f.git("add", "-A")
	f.git("commit", "-m", "target shared")
	targetHead := f.head()

	conflicted, err := svc.Integrate(ctx, mutationFor(rec), newMutationHarness().deps())
	if err == nil {
		t.Fatal("Integrate reported success for a conflicting merge")
	}
	if conflicted.Integration == nil || conflicted.Integration.State != IntegrationConflict {
		t.Fatalf("integration = %+v, want a conflict receipt", conflicted.Integration)
	}
	if len(fg.unmergedSeen) != 1 || fg.unmergedSeen[0] != "shared.txt" {
		t.Fatalf("git's unmerged paths at failure = %v, want [shared.txt]", fg.unmergedSeen)
	}
	for _, path := range fg.unmergedSeen {
		if !strings.Contains(conflicted.Integration.Reason, path) {
			t.Fatalf("conflict receipt %q does not report %q", conflicted.Integration.Reason, path)
		}
	}

	// The target is exactly where the operation found it.
	if head := f.head(); head != targetHead {
		t.Fatalf("target HEAD = %s, want the pre-operation %s", head, targetHead)
	}
	if status := gitIn(t, f.dir, "status", "--porcelain", "--untracked-files=no"); status != "" {
		t.Fatalf("target status = %q, want a clean index", status)
	}
	if state, err := (worktree.CLIGitOps{}).MergeState(f.dir); err != nil || state != "" {
		t.Fatalf("target merge state = %q (%v), want idle", state, err)
	}
	if got := readIn(t, f.dir, "shared.txt"); got != "target shared\n" {
		t.Fatalf("target shared.txt = %q, want the operator's committed content", got)
	}

	// Both branches survive: a conflict is not a reason to delete anyone's work.
	if branches := ownedBranches(t, f.dir); len(branches) != 1 {
		t.Fatalf("owned branches = %v, want the source branch retained", branches)
	}
	if tip := gitIn(t, rec.Binding.Root, "rev-parse", "HEAD"); tip != rec.Candidate {
		t.Fatalf("source HEAD = %s, want the reviewed candidate %s", tip, rec.Candidate)
	}
	if conflicted.CleanupState != CleanupRetained {
		t.Fatalf("cleanup state = %q, want the source retained", conflicted.CleanupState)
	}
	if merges := operationMerges(t, f.dir); len(merges) != 0 {
		t.Fatalf("a conflicted integration created merges: %v", merges)
	}
}

// TestIntegrateAlreadyPresentAndNoChanges pins idempotency: a repeated request
// returns the original receipt without a second merge, and a candidate equal to
// its own base creates no empty merge.
func TestIntegrateAlreadyPresentAndNoChanges(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})

	child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
	first, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("Integrate: %v", err)
	}
	receipt := *first.Integration
	merges := operationMerges(t, f.dir)
	if len(merges) != 1 {
		t.Fatalf("operation merges = %v, want one", merges)
	}

	again, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("repeated Integrate: %v", err)
	}
	if *again.Integration != receipt {
		t.Fatalf("repeated receipt = %+v, want the original %+v", *again.Integration, receipt)
	}
	if again.Revision != first.Revision {
		t.Fatalf("a repeated request advanced the revision to %d, want %d", again.Revision, first.Revision)
	}
	if got := operationMerges(t, f.dir); len(got) != len(merges) {
		t.Fatalf("a repeated request merged again: %v", got)
	}

	// A review fork that committed nothing has no history to bring in.
	noChange := prepareChild(t, svc, f, "task-2", "", "", OutcomeDone)
	if noChange.Candidate != noChange.Binding.BaseSHA {
		t.Fatalf("fixture: candidate = %s, base = %s", noChange.Candidate, noChange.Binding.BaseSHA)
	}
	targetBefore := f.head()
	noChangeReceipt, err := svc.Integrate(ctx, mutationFor(noChange), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("Integrate(no-change): %v", err)
	}
	if noChangeReceipt.Integration == nil || noChangeReceipt.Integration.State != IntegrationNoChanges {
		t.Fatalf("integration = %+v, want no_changes", noChangeReceipt.Integration)
	}
	if head := f.head(); head != targetBefore {
		t.Fatalf("a no-change candidate advanced the target to %s, want %s", head, targetBefore)
	}
	if got := operationMerges(t, f.dir); len(got) != len(merges) {
		t.Fatalf("a no-change candidate created a merge: %v", got)
	}
	// The base candidate is not brought into the parent implicitly either.
	if tracked := gitIn(t, f.dir, "status", "--porcelain", "--untracked-files=no"); tracked != "" {
		t.Fatalf("the target is dirty after a no-change integration: %q", tracked)
	}
	if noChangeReceipt.CleanupState != CleanupComplete {
		t.Fatalf("cleanup state = %q, want %q", noChangeReceipt.CleanupState, CleanupComplete)
	}
}

// TestCleanupFailureDoesNotFailIntegration pins the cleanup contract: the merge
// is the success, a failed cleanup leaves the exact receipt with a pending state,
// and the retry touches only the artifacts that are still there.
func TestCleanupFailureDoesNotFailIntegration(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	fg := &faultGit{CLIGitOps: worktree.CLIGitOps{}, failBranchDelete: true}
	store := NewMemoryStore()
	svc := NewService(store, fg)

	child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
	rec, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("a cleanup failure failed the integration: %v", err)
	}
	if rec.Integration == nil || rec.Integration.State != IntegrationIntegrated {
		t.Fatalf("integration = %+v, want integrated", rec.Integration)
	}
	if rec.CleanupState != CleanupIntegratedPending {
		t.Fatalf("cleanup state = %q, want %q", rec.CleanupState, CleanupIntegratedPending)
	}
	merges := operationMerges(t, f.dir)
	if len(merges) != 1 {
		t.Fatalf("operation merges = %v, want one", merges)
	}
	if branches := ownedBranches(t, f.dir); len(branches) != 1 {
		t.Fatalf("owned branches = %v, want the undeleted branch retained", branches)
	}

	_, removesBefore, _ := fg.counts()
	again, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("retry Integrate: %v", err)
	}
	if again.CleanupState != CleanupComplete {
		t.Fatalf("cleanup state after the retry = %q, want %q", again.CleanupState, CleanupComplete)
	}
	if again.Integration.State != IntegrationIntegrated || again.Integration.ResultCommit != rec.Integration.ResultCommit {
		t.Fatalf("the retry rewrote the receipt: %+v", again.Integration)
	}
	if _, removesAfter, _ := fg.counts(); removesAfter != removesBefore {
		t.Fatalf("the retry removed the worktree again: %d calls", removesAfter-removesBefore)
	}
	if branches := ownedBranches(t, f.dir); len(branches) != 0 {
		t.Fatalf("owned branches = %v, want the branch deleted by the retry", branches)
	}
	if got := operationMerges(t, f.dir); len(got) != 1 {
		t.Fatalf("the retry merged again: %v", got)
	}
}

// TestDiscardIsExplicitAndPreservesDirtyWork pins discard: only a verified clean
// owned branch of an admitted task is force-deleted, and every other state is
// retained.
func TestDiscardIsExplicitAndPreservesDirtyWork(t *testing.T) {
	ctx := context.Background()

	t.Run("clean abandoned branch", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()

		h := newMutationHarness()
		rec, err := svc.Discard(ctx, mutationFor(child), h.deps())
		if err != nil {
			t.Fatalf("Discard: %v", err)
		}
		if rec.Integration == nil || rec.Integration.State != IntegrationDiscarded || rec.Integration.Kind != KindDiscard {
			t.Fatalf("integration = %+v, want a discarded receipt", rec.Integration)
		}
		if rec.CleanupState != CleanupComplete {
			t.Fatalf("cleanup state = %q, want %q", rec.CleanupState, CleanupComplete)
		}
		if _, err := os.Stat(child.Binding.Root); !os.IsNotExist(err) {
			t.Fatal("the discarded worktree survived")
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 0 {
			t.Fatalf("owned branches = %v, want the discarded branch deleted", branches)
		}
		if head := f.head(); head != before {
			t.Fatalf("discard moved the target to %s, want %s", head, before)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 0 {
			t.Fatalf("discard merged something: %v", merges)
		}
		commits, released := h.admitter.last().counts()
		if commits != 1 || released != 0 {
			t.Fatalf("admission = %d commits, %d releases, want it spent once", commits, released)
		}

		again, err := svc.Discard(ctx, mutationFor(child), newMutationHarness().deps())
		if err != nil {
			t.Fatalf("repeated Discard: %v", err)
		}
		if *again.Integration != *rec.Integration {
			t.Fatalf("repeated receipt = %+v, want %+v", *again.Integration, *rec.Integration)
		}
	})

	t.Run("changed sha", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		writeIn(t, child.Binding.Root, "later.txt", "later\n")
		gitIn(t, child.Binding.Root, "add", "-A")
		gitIn(t, child.Binding.Root, "commit", "-m", "later work")

		if _, err := svc.Discard(ctx, mutationFor(child), newMutationHarness().deps()); err == nil {
			t.Fatal("Discard removed a branch that had moved past the reviewed candidate")
		}
		got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused", got.Integration)
		}
		if got.CleanupState != CleanupRetained {
			t.Fatalf("cleanup state = %q, want %q", got.CleanupState, CleanupRetained)
		}
		if _, err := os.Stat(child.Binding.Root); err != nil {
			t.Fatalf("a refused discard removed the worktree: %v", err)
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want the source retained", branches)
		}
	})

	t.Run("dirty work", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		writeIn(t, child.Binding.Root, "one.txt", "uncommitted edit\n")

		if _, err := svc.Discard(ctx, mutationFor(child), newMutationHarness().deps()); err == nil {
			t.Fatal("Discard removed a worktree with uncommitted work")
		}
		got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
		if got.Integration.State != IntegrationRefused || got.CleanupState != CleanupRetained {
			t.Fatalf("record = %+v, want a retained refusal", got)
		}
		if content := readIn(t, child.Binding.Root, "one.txt"); content != "uncommitted edit\n" {
			t.Fatalf("the child's work = %q, want it untouched", content)
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want the source retained", branches)
		}
	})

	t.Run("untracked work", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		writeIn(t, child.Binding.Root, "scratch.txt", "untracked\n")

		if _, err := svc.Discard(ctx, mutationFor(child), newMutationHarness().deps()); err == nil {
			t.Fatal("Discard removed a worktree with untracked work")
		}
		if content := readIn(t, child.Binding.Root, "scratch.txt"); content != "untracked\n" {
			t.Fatalf("the child's untracked work = %q, want it untouched", content)
		}
	})

	t.Run("busy workspace", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)

		h := newMutationHarness()
		h.idle.err = errors.New("workspace is busy")
		if _, err := svc.Discard(ctx, mutationFor(child), h.deps()); err == nil {
			t.Fatal("Discard ran without the idle lease")
		}
		got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
		if got.Integration != nil {
			t.Fatalf("a busy refusal wrote %+v", got.Integration)
		}
		if _, err := os.Stat(child.Binding.Root); err != nil {
			t.Fatalf("a busy refusal removed the worktree: %v", err)
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want the source retained", branches)
		}
	})

	t.Run("unknown execution", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		unknown := child
		unknown.Outcome = OutcomeUnknown
		updated, err := store.CompareAndSwap(ctx, unknown, child.Revision)
		if err != nil {
			t.Fatalf("CompareAndSwap: %v", err)
		}

		if _, err := svc.Discard(ctx, mutationFor(updated), newMutationHarness().deps()); err == nil {
			t.Fatal("Discard removed an unknown execution's work")
		}
		if _, err := os.Stat(child.Binding.Root); err != nil {
			t.Fatalf("an unknown execution lost its worktree: %v", err)
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want the source retained", branches)
		}
	})

	t.Run("ownership replaced", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		gitIn(t, child.Binding.Root, "checkout", "-b", "someone-else")

		if _, err := svc.Discard(ctx, mutationFor(child), newMutationHarness().deps()); err == nil {
			t.Fatal("Discard removed a workspace checked out on another branch")
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want the source retained", branches)
		}
	})
}

// TestPrepareMutationEffectKeyBindsFacts pins the effect key: it is a digest of
// every prepared fact, so a moved target needs a new admission, and no mutation
// runs without an idle lease and an admission.
func TestPrepareMutationEffectKeyBindsFacts(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})
	child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)

	prepared, err := svc.PrepareMutation(ctx, mutationFor(child))
	if err != nil {
		t.Fatalf("PrepareMutation: %v", err)
	}
	if prepared.RecordRevision != child.Revision ||
		prepared.Repository != child.Binding.Repository ||
		prepared.SourceRoot != child.Binding.Root ||
		prepared.SourceHead != child.Candidate ||
		prepared.TargetRoot != worktree.CanonicalPath(f.dir) ||
		prepared.TargetBranch != "main" ||
		prepared.TargetHead != f.head() {
		t.Fatalf("prepared mutation = %+v, want the recorded source and live target", prepared)
	}
	key := prepared.EffectKey()
	if len(key) != 64 {
		t.Fatalf("effect key = %q, want a sha256 hex digest", key)
	}
	unprepared, err := svc.PrepareMutation(ctx, mutationFor(child))
	if err != nil {
		t.Fatalf("PrepareMutation: %v", err)
	}
	if unprepared.EffectKey() != key {
		t.Fatal("an unchanged inspection produced a different effect key")
	}
	if child.Revision != mustRecord(t, store, child.OwnerSessionID, child.TaskKey).Revision {
		t.Fatal("PrepareMutation wrote to the record")
	}

	// A target fact that changed after evaluation is a different effect.
	f.write("moved.txt", "moved\n")
	f.git("add", "-A")
	f.git("commit", "-m", "the parent moved")
	moved, err := svc.PrepareMutation(ctx, mutationFor(child))
	if err != nil {
		t.Fatalf("PrepareMutation after the target moved: %v", err)
	}
	if moved.EffectKey() == key {
		t.Fatal("a moved target produced the same effect key")
	}

	// Nil is never approval.
	if _, err := svc.Integrate(ctx, mutationFor(child), MutationDeps{}); err == nil {
		t.Fatal("Integrate ran without an idle gate and an admission")
	}
	if _, err := svc.Integrate(ctx, mutationFor(child), MutationDeps{Admit: newMutationHarness().admitter.Admit}); err == nil {
		t.Fatal("Integrate ran without an idle gate")
	}
	if _, err := svc.Integrate(ctx, mutationFor(child), MutationDeps{Idle: newMutationHarness().idle}); err == nil {
		t.Fatal("Integrate ran without an admission")
	}
	got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
	if got.Integration != nil {
		t.Fatalf("a request without admission wrote %+v", got.Integration)
	}
	if merges := operationMerges(t, f.dir); len(merges) != 0 {
		t.Fatalf("a request without admission merged: %v", merges)
	}
}
