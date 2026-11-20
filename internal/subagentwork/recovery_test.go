package subagentwork

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"marshal/internal/worktree"
)

// TestDiscardCleanupFailureStaysRecoverable pins the discard half of the cleanup
// contract. A discard whose worktree removal succeeded but whose branch deletion
// failed is SETTLED: the candidate will never be integrated. Journaling the
// settled state is what lets the retry reach resumeCleanup, which tolerates the
// already-removed worktree. Leaving the record at a bare intent would send the
// retry back through the identity checks, fail on the root this operation
// removed, and overwrite the only record of the operation with a refusal.
func TestDiscardCleanupFailureStaysRecoverable(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	fg := &faultGit{CLIGitOps: worktree.CLIGitOps{}, failBranchDelete: true}
	store := NewMemoryStore()
	svc := NewService(store, fg)

	child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)

	rec, err := svc.Discard(ctx, mutationFor(child), newMutationHarness().deps())
	if err == nil {
		t.Fatal("a discard whose branch deletion failed reported success")
	}
	if rec.Integration == nil || rec.Integration.State != IntegrationDiscarded {
		t.Fatalf("integration = %+v, want the settled discard state, not a bare intent", rec.Integration)
	}
	if rec.Integration.Kind != KindDiscard {
		t.Fatalf("integration kind = %q, want %q", rec.Integration.Kind, KindDiscard)
	}
	if rec.CleanupState != CleanupRetained {
		t.Fatalf("cleanup state = %q, want %q", rec.CleanupState, CleanupRetained)
	}
	if _, err := os.Stat(child.Binding.Root); err == nil {
		t.Fatal("the worktree survived the discard")
	}

	// The retry must resume the cleanup the settled receipt still owes.
	again, err := svc.Discard(ctx, mutationFor(child), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("retry Discard: %v", err)
	}
	if again.Integration == nil || again.Integration.State != IntegrationDiscarded {
		t.Fatalf("integration after the retry = %+v, want discarded", again.Integration)
	}
	if again.CleanupState != CleanupComplete {
		t.Fatalf("cleanup after the retry = %q, want %q", again.CleanupState, CleanupComplete)
	}
	if branches := ownedBranches(t, f.dir); len(branches) != 0 {
		t.Fatalf("owned branches = %v, want the retry to delete the retained branch", branches)
	}
	if merges := operationMerges(t, f.dir); len(merges) != 0 {
		t.Fatalf("discard merged something: %v", merges)
	}
}

// TestIntegrateSettlesItsOwnInterruptedMerge pins recovery from a crash between
// journalling an integration intent and aborting its conflicting merge. The
// target holds the operation's OWN half-applied merge; a retry carries the
// admission needed to settle it, so it restores the target and integrates
// instead of refusing a target this operation wedged.
func TestIntegrateSettlesItsOwnInterruptedMerge(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})

	child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
	before := f.head()

	// Model the crash: the intent is journalled, and the target holds this
	// operation's merge of the recorded candidate, uncommitted.
	intent := Integration{
		OperationID:    "op-interrupted",
		Kind:           KindIntegrate,
		State:          IntegrationIntent,
		ExpectedSource: child.Candidate,
		BeforeTarget:   before,
	}
	journalled, err := svc.writeMutation(ctx, child, intent, CleanupRetained)
	if err != nil {
		t.Fatalf("journal the intent: %v", err)
	}
	gitIn(t, f.dir, "merge", "--no-commit", "--no-ff", child.Candidate)
	if head := f.head(); head != before {
		t.Fatalf("the wedged merge moved HEAD to %s, want %s", head, before)
	}

	got, err := svc.Integrate(ctx, mutationFor(journalled), newMutationHarness().deps())
	if err != nil {
		t.Fatalf("Integrate over its own interrupted merge: %v", err)
	}
	if got.Integration == nil || got.Integration.State != IntegrationIntegrated {
		t.Fatalf("integration = %+v, want integrated", got.Integration)
	}
	if got.CleanupState != CleanupComplete {
		t.Fatalf("cleanup state = %q, want %q", got.CleanupState, CleanupComplete)
	}
	if head := f.head(); head == before {
		t.Fatalf("the integration left the target at %s", head)
	}
	if content := readIn(t, f.dir, "one.txt"); strings.TrimSpace(content) != "one" {
		t.Fatalf("target one.txt = %q, want the integrated content", content)
	}
	if merges := operationMerges(t, f.dir); len(merges) != 1 {
		t.Fatalf("operation merges = %v, want exactly the one integration", merges)
	}
}

// TestIntegrateLeavesAForeignMergeAlone is the other half: a merge this
// operation cannot prove it owns is neither aborted nor overwritten in the
// journal. The refusal must not destroy the intent, which is the only evidence
// of what this operation was doing.
func TestIntegrateLeavesAForeignMergeAlone(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})

	child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
	before := f.head()

	// Someone else's merge, in progress in the target.
	writeIn(t, f.dir, "intruder.txt", "intruder\n")
	gitIn(t, f.dir, "add", "-A")
	gitIn(t, f.dir, "commit", "-m", "intruder work")
	intruder := f.head()
	gitIn(t, f.dir, "checkout", before)
	intruderBranch := "intruder"
	gitIn(t, f.dir, "branch", "-f", intruderBranch, intruder)
	gitIn(t, f.dir, "merge", "--no-commit", "--no-ff", intruderBranch)

	intent := Integration{
		OperationID:    "op-foreign",
		Kind:           KindIntegrate,
		State:          IntegrationIntent,
		ExpectedSource: child.Candidate,
		BeforeTarget:   before,
	}
	journalled, err := svc.writeMutation(ctx, child, intent, CleanupRetained)
	if err != nil {
		t.Fatalf("journal the intent: %v", err)
	}

	if _, err := svc.Integrate(ctx, mutationFor(journalled), newMutationHarness().deps()); err == nil {
		t.Fatal("Integrate settled a merge it cannot prove it owns")
	}
	if state := gitIn(t, f.dir, "rev-parse", "--verify", "MERGE_HEAD"); strings.TrimSpace(state) != intruder {
		t.Fatalf("MERGE_HEAD = %q, want the foreign merge left in progress at %s", state, intruder)
	}
	if head := f.head(); head != before {
		t.Fatalf("the refusal moved the target to %s, want %s", head, before)
	}
	got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
	if got.Integration == nil || got.Integration.State != IntegrationIntent {
		t.Fatalf("integration = %+v, want the intent preserved", got.Integration)
	}
	if got.Integration.OperationID != intent.OperationID || got.Integration.BeforeTarget != intent.BeforeTarget {
		t.Fatalf("the refusal rewrote the operation's facts: %+v", got.Integration)
	}
}

// staleOnceStore fails the first compare-and-swap of a completion with the
// stale-revision sentinel another observation of the same record produces.
type staleOnceStore struct {
	Store
	mu    sync.Mutex
	stale int
}

func (s *staleOnceStore) CompareAndSwap(ctx context.Context, rec Record, expected uint64) (Record, error) {
	s.mu.Lock()
	if s.stale > 0 {
		s.stale--
		s.mu.Unlock()
		return Record{}, ErrStaleRecord
	}
	s.mu.Unlock()
	return s.Store.CompareAndSwap(ctx, rec, expected)
}

// arm makes the next n compare-and-swaps fail as stale. It is armed after the
// allocation, so the injected staleness lands on the write under test.
func (s *staleOnceStore) arm(n int) {
	s.mu.Lock()
	s.stale = n
	s.mu.Unlock()
}

// TestCompleteRetriesAStaleRevision pins that a lost CAS cannot lose the
// completion. agent.await's task-key lookup inspects the same record, so the
// revision can move between Complete's read and its write; without a retry the
// record would keep saying "running" and the retained report id would be gone
// with it, which a restart then reports as interrupted.
func TestCompleteRetriesAStaleRevision(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := &staleOnceStore{Store: NewMemoryStore()}
	svc := NewService(store, worktree.CLIGitOps{})

	req := isolatedRequest(f.dir, f.dir)
	req.TaskKey = "task-complete"
	req.InvocationID = "inv-task-complete"
	rec, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	store.arm(1)

	done, err := svc.Complete(ctx, rec.OwnerSessionID, rec.TaskKey, Completion{Outcome: OutcomeDone, ReportMessageID: 42})
	if err != nil {
		t.Fatalf("Complete over a stale revision: %v", err)
	}
	if done.Outcome != OutcomeDone {
		t.Fatalf("outcome = %q, want %q", done.Outcome, OutcomeDone)
	}
	if done.ReportMessageID != 42 {
		t.Fatalf("report message id = %d, want 42", done.ReportMessageID)
	}
	stored := mustRecord(t, store, rec.OwnerSessionID, rec.TaskKey)
	if stored.Outcome != OutcomeDone || stored.ReportMessageID != 42 {
		t.Fatalf("stored completion = outcome %q, report %d; want done/42", stored.Outcome, stored.ReportMessageID)
	}
}
