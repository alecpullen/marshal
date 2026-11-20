package subagentwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/worktree"
)

// isolatedRequest is the ordinary isolated dispatch a parent would make: the
// project root and the parent's active checkout, which may be the same
// directory.
func isolatedRequest(projectRoot, parentRoot string) PrepareRequest {
	return PrepareRequest{
		OwnerSessionID:    "owner-1",
		TaskKey:           "task-1",
		InvocationID:      "inv-1",
		ProjectRoot:       projectRoot,
		ParentRoot:        parentRoot,
		IsolatedSupported: true,
	}
}

// TestPrepareStartsAtActiveParentCommit pins the source of the child's base: the
// parent's *active* checkout, which may itself be a worktree, is not assumed to
// be the project-root checkout.
func TestPrepareStartsAtActiveParentCommit(t *testing.T) {
	f := newGitFixture(t)
	worktreesDir, err := worktree.AgentDir(f.dir)
	if err != nil {
		t.Fatalf("AgentDir: %v", err)
	}
	parent, err := worktree.EnsureWorktree(worktree.CLIGitOps{}, f.dir, worktreesDir, "feat/parent", "HEAD", worktree.WorktreeSetup{})
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	writeIn(t, parent.Path, "interface.go", "package parent\n")
	gitIn(t, parent.Path, "add", "-A")
	gitIn(t, parent.Path, "commit", "-m", "parent interface")
	parentHead := gitIn(t, parent.Path, "rev-parse", "HEAD")
	if parentHead == f.head() {
		t.Fatal("fixture: the parent worktree must be ahead of the main checkout")
	}

	svc := NewService(NewMemoryStore(), worktree.CLIGitOps{})
	rec, err := svc.Prepare(context.Background(), isolatedRequest(f.dir, parent.Path))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if rec.Outcome != OutcomePrepared {
		t.Fatalf("outcome = %q, want %q", rec.Outcome, OutcomePrepared)
	}
	if rec.Binding.Mode != ModeIsolated {
		t.Fatalf("binding mode = %q, want isolated", rec.Binding.Mode)
	}
	if rec.Binding.BaseSHA != parentHead {
		t.Fatalf("binding base = %s, want the parent worktree head %s", rec.Binding.BaseSHA, parentHead)
	}
	if rec.Binding.Branch != subagentBranchPrefix+"task-1" {
		t.Fatalf("binding branch = %q, want the branch derived from the task key", rec.Binding.Branch)
	}
	if rec.Binding.Root == worktree.CanonicalPath(parent.Path) {
		t.Fatal("the child was bound to the parent's checkout instead of its own worktree")
	}
	if rec.Target.Root != worktree.CanonicalPath(parent.Path) {
		t.Fatalf("target root = %q, want the captured parent checkout", rec.Target.Root)
	}
	if rec.Target.Branch != "feat/parent" || rec.Target.BaseSHA != parentHead {
		t.Fatalf("target = %+v, want the parent checkout at %s", rec.Target, parentHead)
	}

	// The child contains the committed parent-only interface, because it starts
	// at the parent worktree's commit rather than the main checkout's.
	if got := gitIn(t, rec.Binding.Root, "rev-parse", "HEAD"); got != parentHead {
		t.Fatalf("child HEAD = %s, want %s", got, parentHead)
	}
	if got := readIn(t, rec.Binding.Root, "interface.go"); got != "package parent\n" {
		t.Fatalf("child interface.go = %q, want the committed parent content", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "interface.go")); !os.IsNotExist(err) {
		t.Fatal("the main checkout received the parent-only interface")
	}
}

// TestPrepareExactReviewerBase pins the explicit base commit: it changes where
// the child starts and never where its work lands.
func TestPrepareExactReviewerBase(t *testing.T) {
	f := newGitFixture(t)
	reviewBase := f.head()
	f.write("later.txt", "later\n")
	f.git("add", "-A")
	f.git("commit", "-m", "later")
	parentHead := f.head()
	if parentHead == reviewBase {
		t.Fatal("fixture: the parent must have advanced past the review base")
	}

	req := isolatedRequest(f.dir, f.dir)
	req.BaseCommit = reviewBase
	svc := NewService(NewMemoryStore(), worktree.CLIGitOps{})
	rec, err := svc.Prepare(context.Background(), req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if rec.Binding.BaseSHA != reviewBase {
		t.Fatalf("binding base = %s, want the explicit commit %s", rec.Binding.BaseSHA, reviewBase)
	}
	if got := gitIn(t, rec.Binding.Root, "rev-parse", "HEAD"); got != reviewBase {
		t.Fatalf("child HEAD = %s, want the explicit commit %s", got, reviewBase)
	}
	if _, err := os.Stat(filepath.Join(rec.Binding.Root, "later.txt")); !os.IsNotExist(err) {
		t.Fatal("the child started past the explicit base commit")
	}
	// The explicit base changes the child's start only: the target is still the
	// captured parent checkout, which did not move.
	if rec.Target.Root != worktree.CanonicalPath(f.dir) {
		t.Fatalf("target root = %q, want the parent checkout", rec.Target.Root)
	}
	if rec.Target.Branch != "main" || rec.Target.BaseSHA != parentHead {
		t.Fatalf("target = %+v, want the parent checkout at %s", rec.Target, parentHead)
	}
	if got := f.head(); got != parentHead {
		t.Fatalf("the parent HEAD moved to %s during allocation", got)
	}
	if got := gitIn(t, f.dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("the target branch changed to %q", got)
	}
}

// TestPrepareDoesNotCopyOrCommitParentDirt pins the commit-first rule: dispatch
// neither stages nor commits the parent's unrelated changes, and the child only
// sees committed history.
func TestPrepareDoesNotCopyOrCommitParentDirt(t *testing.T) {
	f := newGitFixture(t)
	f.write("base.txt", "modified in the parent\n")
	f.write("scratch.txt", "untracked scratch\n")

	svc := NewService(NewMemoryStore(), worktree.CLIGitOps{})
	rec, err := svc.Prepare(context.Background(), isolatedRequest(f.dir, f.dir))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// The parent's tracked modification and untracked file are byte-identical.
	if got := readIn(t, f.dir, "base.txt"); got != "modified in the parent\n" {
		t.Fatalf("parent base.txt = %q, want it untouched", got)
	}
	if got := readIn(t, f.dir, "scratch.txt"); got != "untracked scratch\n" {
		t.Fatalf("parent scratch.txt = %q, want it untouched", got)
	}
	// Neither reached the isolated child, which starts from the committed tree.
	if got := readIn(t, rec.Binding.Root, "base.txt"); got != "base\n" {
		t.Fatalf("child base.txt = %q, want the committed content", got)
	}
	if _, err := os.Stat(filepath.Join(rec.Binding.Root, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatal("the child received the parent's untracked file")
	}
	if status := gitIn(t, rec.Binding.Root, "status", "--porcelain"); status != "" {
		t.Fatalf("child status = %q, want a clean checkout", status)
	}
	if rec.Binding.BaseSHA != f.head() {
		t.Fatalf("binding base = %s, want the committed parent HEAD %s", rec.Binding.BaseSHA, f.head())
	}
}

// failingWorktreeAdd performs the real worktree creation and then reports a
// failure. It models the partial creation a crash or a failing step leaves
// behind: the branch and checkout exist, but the record could not be completed.
type failingWorktreeAdd struct {
	worktree.CLIGitOps
}

func (g failingWorktreeAdd) WorktreeAdd(dir, path, branch, startPoint string) error {
	if err := g.CLIGitOps.WorktreeAdd(dir, path, branch, startPoint); err != nil {
		return err
	}
	return errors.New("simulated failure after the worktree was created")
}

// TestPrepareDuplicateAndPartialCreation pins the deduplication rule: a repeated
// admitted task keeps the one child it was given, and a partial creation stays
// identifiable instead of acquiring a second workspace.
func TestPrepareDuplicateAndPartialCreation(t *testing.T) {
	ctx := context.Background()

	t.Run("repeated admission allocates one child", func(t *testing.T) {
		f := newGitFixture(t)
		svc := NewService(NewMemoryStore(), worktree.CLIGitOps{})
		req := isolatedRequest(f.dir, f.dir)

		first, err := svc.Prepare(ctx, req)
		if err != nil {
			t.Fatalf("first Prepare: %v", err)
		}
		second, err := svc.Prepare(ctx, req)
		if err != nil {
			t.Fatalf("second Prepare: %v", err)
		}
		if second.Binding != first.Binding {
			t.Fatalf("repeated admission returned binding %+v, want %+v", second.Binding, first.Binding)
		}
		if second.Revision != first.Revision {
			t.Fatalf("repeated admission advanced the revision to %d, want %d", second.Revision, first.Revision)
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want exactly one", branches)
		}
		if worktrees := checkoutWorktrees(t, f.dir); len(worktrees) != 1 {
			t.Fatalf("child worktrees = %v, want exactly one", worktrees)
		}
	})

	t.Run("partial creation stays identifiable", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, failingWorktreeAdd{CLIGitOps: worktree.CLIGitOps{}})
		req := isolatedRequest(f.dir, f.dir)

		rec, err := svc.Prepare(ctx, req)
		if err == nil {
			t.Fatal("Prepare reported success after the simulated allocation failure")
		}
		if rec.Outcome != OutcomeFailed {
			t.Fatalf("outcome = %q, want %q", rec.Outcome, OutcomeFailed)
		}
		if rec.SourceError == "" {
			t.Fatal("the failed allocation recorded no source error")
		}
		// The record is durable and names the artifact that was created.
		stored, err := store.Get(ctx, req.OwnerSessionID, req.TaskKey)
		if err != nil {
			t.Fatalf("Get after failed allocation: %v", err)
		}
		if stored.Binding.Branch != subagentBranchPrefix+req.TaskKey {
			t.Fatalf("recorded branch = %q, want the branch derived from the task key", stored.Binding.Branch)
		}
		if stored.Binding.Root != rec.Binding.Root {
			t.Fatalf("stored root = %q, want the retained %q", stored.Binding.Root, rec.Binding.Root)
		}

		// A repeated admission does not allocate a second child.
		again, err := svc.Prepare(ctx, req)
		if err != nil {
			t.Fatalf("repeated Prepare: %v", err)
		}
		if again.Binding != stored.Binding || again.Outcome != OutcomeFailed {
			t.Fatalf("repeated Prepare returned %+v, want the retained failed record", again)
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want exactly one after the failure", branches)
		}
		if worktrees := checkoutWorktrees(t, f.dir); len(worktrees) != 1 {
			t.Fatalf("child worktrees = %v, want exactly one after the failure", worktrees)
		}
	})
}

// TestPrepareSharedAndUnsupportedModes pins the mode rules: shared mode works
// where git does not, isolated mode refuses rather than falling back, and an
// explicit base commit or an unknown mode is refused.
func TestPrepareSharedAndUnsupportedModes(t *testing.T) {
	ctx := context.Background()
	nongit := t.TempDir()
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})
	request := func(key string) PrepareRequest {
		return PrepareRequest{
			OwnerSessionID:    "owner-1",
			TaskKey:           key,
			InvocationID:      "inv-" + key,
			ProjectRoot:       nongit,
			ParentRoot:        nongit,
			IsolatedSupported: true,
		}
	}

	// A non-git directory is a valid shared workspace, and shared mode performs
	// no git mutation.
	shared := request("shared-1")
	shared.Mode = ModeShared
	rec, err := svc.Prepare(ctx, shared)
	if err != nil {
		t.Fatalf("shared Prepare in a non-git directory: %v", err)
	}
	if rec.Binding.Mode != ModeShared {
		t.Fatalf("binding mode = %q, want shared", rec.Binding.Mode)
	}
	if rec.Binding.Root != worktree.CanonicalPath(nongit) {
		t.Fatalf("shared root = %q, want the captured root", rec.Binding.Root)
	}
	if rec.Binding.Repository != "" {
		t.Fatalf("a non-git shared binding invented repository %q", rec.Binding.Repository)
	}
	if rec.Outcome != OutcomePrepared {
		t.Fatalf("shared outcome = %q, want %q", rec.Outcome, OutcomePrepared)
	}
	if _, err := os.Stat(filepath.Join(nongit, ".git")); !os.IsNotExist(err) {
		t.Fatal("shared mode mutated the directory into a git checkout")
	}

	// Isolated dispatch needs a git repository and refuses instead of falling
	// back to the shared mode.
	if _, err := svc.Prepare(ctx, request("isolated-1")); err == nil {
		t.Fatal("isolated Prepare in a non-git directory succeeded")
	}
	if _, err := store.Get(ctx, "owner-1", "isolated-1"); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("a refused isolated dispatch recorded a workspace: %v", err)
	}

	// Shared mode rejects a base override rather than pretending to check it out.
	withBase := request("shared-2")
	withBase.Mode = ModeShared
	withBase.BaseCommit = "0123456789abcdef0123456789abcdef01234567"
	if _, err := svc.Prepare(ctx, withBase); err == nil {
		t.Fatal("shared Prepare accepted a base commit")
	}

	// An unknown mode is refused, never treated as the omitted default.
	unknown := request("weird-1")
	unknown.Mode = Mode("elsewhere")
	if _, err := svc.Prepare(ctx, unknown); err == nil {
		t.Fatal("Prepare accepted an unknown workspace mode")
	}

	// A task key that is not safe opaque text is refused rather than sanitised
	// into a path or branch.
	for _, key := range []string{"../escape", "has/slash", "..", "-dash", "a..b"} {
		bad := request("placeholder")
		bad.TaskKey = key
		if _, err := svc.Prepare(ctx, bad); err == nil {
			t.Fatalf("task key %q was accepted", key)
		}
	}
}

// TestPrepareRejectsUnsupportedLinkedWorktreeBackend pins the capability gate: a
// backend that cannot host a linked worktree refuses isolated dispatch before
// anything is allocated, and support is never inferred.
func TestPrepareRejectsUnsupportedLinkedWorktreeBackend(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})

	// The container backend reports no support for linked worktrees because its
	// cwd-only mount excludes the repository's common git directory; the
	// sandbox and app fixtures pin which backend reports what (restricted and
	// passthrough permit host worktrees). Prepare must refuse before git runs.
	req := isolatedRequest(f.dir, f.dir)
	req.IsolatedSupported = false
	if _, err := svc.Prepare(ctx, req); err == nil {
		t.Fatal("Prepare allocated an isolated workspace on a backend without linked-worktree support")
	} else if !strings.Contains(err.Error(), "linked git worktree") {
		t.Fatalf("refusal = %v, want it to name the missing capability", err)
	}

	// Nothing was allocated: no branch, no checkout, no record.
	if branches := ownedBranches(t, f.dir); len(branches) != 0 {
		t.Fatalf("owned branches after a refusal = %v, want none", branches)
	}
	if worktrees := checkoutWorktrees(t, f.dir); len(worktrees) != 0 {
		t.Fatalf("child worktrees after a refusal = %v, want none", worktrees)
	}
	if _, err := store.Get(ctx, req.OwnerSessionID, req.TaskKey); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("a refused dispatch recorded a workspace: %v", err)
	}

	// A backend that does state the capability allocates the host worktree.
	req.IsolatedSupported = true
	rec, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare with a supporting backend: %v", err)
	}
	if rec.Outcome != OutcomePrepared {
		t.Fatalf("outcome = %q, want %q", rec.Outcome, OutcomePrepared)
	}
	if branches := ownedBranches(t, f.dir); len(branches) != 1 {
		t.Fatalf("owned branches = %v, want exactly one", branches)
	}
}

// TestPrepareDetachedParentIdentity pins a detached parent: dispatch may start
// there, the identity is recorded, and the target records no branch for the
// integration step to guess at.
func TestPrepareDetachedParentIdentity(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	detached := f.head()
	f.git("checkout", "--detach", detached)
	if branch := f.git("rev-parse", "--abbrev-ref", "HEAD"); branch != "HEAD" {
		t.Fatalf("fixture: HEAD is on branch %q, want detached", branch)
	}

	req := isolatedRequest(f.dir, f.dir)
	svc := NewService(NewMemoryStore(), worktree.CLIGitOps{})
	rec, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare from a detached parent: %v", err)
	}
	if rec.Binding.BaseSHA != detached {
		t.Fatalf("binding base = %s, want the detached commit %s", rec.Binding.BaseSHA, detached)
	}
	if rec.Target.Branch != "" {
		t.Fatalf("target branch = %q, want empty for a detached parent", rec.Target.Branch)
	}
	if rec.Target.Root != worktree.CanonicalPath(f.dir) || rec.Target.BaseSHA != detached {
		t.Fatalf("target = %+v, want the detached parent checkout", rec.Target)
	}
	if got := gitIn(t, rec.Binding.Root, "rev-parse", "HEAD"); got != detached {
		t.Fatalf("child HEAD = %s, want %s", got, detached)
	}

	// Inspection works from a detached parent too: the child is a normal branch
	// in an owned worktree.
	inspected, err := svc.Inspect(ctx, req.OwnerSessionID, req.TaskKey)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspected.SourceError != "" {
		t.Fatalf("inspection of a healthy workspace reported %q", inspected.SourceError)
	}
	if inspected.Candidate != detached {
		t.Fatalf("candidate = %s, want the child HEAD %s", inspected.Candidate, detached)
	}
}

// TestPrepareObserverReceivesDeepCopies pins the instance-scoped observer: it
// publishes after a successful write and after a read reconciliation, and a
// caller mutating what it was handed cannot reach stored state.
func TestPrepareObserverReceivesDeepCopies(t *testing.T) {
	ctx := context.Background()
	nongit := t.TempDir()
	store := NewMemoryStore()
	var published []Record
	svc := NewService(store, worktree.CLIGitOps{}, WithRecordObserver(func(rec Record) {
		published = append(published, rec)
	}))
	req := PrepareRequest{
		OwnerSessionID: "owner-1",
		TaskKey:        "shared-1",
		InvocationID:   "inv-1",
		ProjectRoot:    nongit,
		ParentRoot:     nongit,
		Mode:           ModeShared,
	}
	rec, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("observer publications = %d, want 1", len(published))
	}
	if published[0].TaskKey != rec.TaskKey || published[0].Binding.Root != rec.Binding.Root {
		t.Fatalf("observed record = %+v, want the written record", published[0])
	}

	// A duplicate admission publishes the read reconciliation without a write.
	again, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("duplicate Prepare: %v", err)
	}
	if len(published) != 2 {
		t.Fatalf("observer publications = %d, want 2", len(published))
	}
	if again.Revision != rec.Revision {
		t.Fatalf("duplicate admission advanced the revision to %d, want %d", again.Revision, rec.Revision)
	}

	// The observer's copy is deep: mutating it cannot change stored state.
	published[0].Binding.Root = "/somewhere/else"
	published[0].SetupWarnings = append(published[0].SetupWarnings, "mutated")
	stored, err := store.Get(ctx, req.OwnerSessionID, req.TaskKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Binding.Root != rec.Binding.Root {
		t.Fatalf("stored root changed to %q through the observer's copy", stored.Binding.Root)
	}
	if len(stored.SetupWarnings) != 0 {
		t.Fatalf("stored setup warnings changed to %v through the observer's copy", stored.SetupWarnings)
	}
}
