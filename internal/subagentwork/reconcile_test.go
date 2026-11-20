package subagentwork

import (
	"context"
	"errors"
	"os"
	"testing"

	"marshal/internal/worktree"
)

func findRecord(t *testing.T, records []Record, key string) Record {
	t.Helper()
	for _, rec := range records {
		if rec.TaskKey == key {
			return rec
		}
	}
	t.Fatalf("reconcile returned no record for %s: %+v", key, records)
	return Record{}
}

// TestReconcileEveryMutationBoundary injects one failure at each boundary of a
// guarded integration — before the journal, after the journal, after the merge,
// after the receipt, during worktree removal and during branch deletion — and
// requires each recovery to leave exactly one truthful result and never a second
// merge.
func TestReconcileEveryMutationBoundary(t *testing.T) {
	ctx := context.Background()

	t.Run("admission refused before the journal", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()

		h := newMutationHarness()
		h.admitter.err = errors.New("authorizer refused the mutation")
		if _, err := svc.Integrate(ctx, mutationFor(child), h.deps()); err == nil {
			t.Fatal("Integrate reported success without an admission")
		}
		got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
		if got.Integration != nil {
			t.Fatalf("a refused admission journalled %+v", got.Integration)
		}
		if head := f.head(); head != before {
			t.Fatalf("the target moved to %s without an admission", head)
		}

		// Nothing was mutilated, so the retry integrates exactly once.
		retried, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
		if err != nil {
			t.Fatalf("retry Integrate: %v", err)
		}
		if retried.Integration.State != IntegrationIntegrated {
			t.Fatalf("retry integration = %+v, want integrated", retried.Integration)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 1 {
			t.Fatalf("operation merges = %v, want exactly one", merges)
		}
		// Reconciliation of a settled operation changes nothing.
		reconciled, err := svc.Reconcile(ctx, child.OwnerSessionID, MutationDeps{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if rec := findRecord(t, reconciled, child.TaskKey); rec.Integration.State != IntegrationIntegrated {
			t.Fatalf("reconciled integration = %+v, want the settled receipt", rec.Integration)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 1 {
			t.Fatalf("reconciliation merged again: %v", merges)
		}
	})

	t.Run("snapshot failure before the journal", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()

		h := newMutationHarness()
		h.snapshotErr = errors.New("snapshot store unavailable")
		if _, err := svc.Integrate(ctx, mutationFor(child), h.deps()); err == nil {
			t.Fatal("Integrate continued without the target snapshot")
		}
		if got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey); got.Integration != nil {
			t.Fatalf("a failed snapshot journalled %+v", got.Integration)
		}
		if head := f.head(); head != before {
			t.Fatalf("the target moved to %s after a failed snapshot", head)
		}
	})

	t.Run("merge refused after the journal", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		fg := &faultGit{CLIGitOps: worktree.CLIGitOps{}, failMerge: true}
		svc := NewService(store, fg)
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)
		before := f.head()

		if _, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate reported success when the merge failed")
		}
		got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
		if got.Integration == nil || got.Integration.State != IntegrationRefused {
			t.Fatalf("integration = %+v, want refused: nothing was merged", got.Integration)
		}
		if head := f.head(); head != before {
			t.Fatalf("the target moved to %s", head)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 0 {
			t.Fatalf("a failed merge left merges behind: %v", merges)
		}
		// Reconciliation cannot invent a merge that never happened.
		reconciled, err := svc.Reconcile(ctx, child.OwnerSessionID, MutationDeps{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if rec := findRecord(t, reconciled, child.TaskKey); rec.Integration.State != IntegrationRefused {
			t.Fatalf("reconciled integration = %+v, want refused", rec.Integration)
		}
		// The retry merges once.
		retried, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
		if err != nil {
			t.Fatalf("retry Integrate: %v", err)
		}
		if retried.Integration.State != IntegrationIntegrated {
			t.Fatalf("retry integration = %+v, want integrated", retried.Integration)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 1 {
			t.Fatalf("operation merges = %v, want exactly one", merges)
		}
	})

	t.Run("merge landed before the receipt was written", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		fs := &faultStore{Store: store, failWhen: func(rec Record) bool {
			return rec.Integration != nil && rec.Integration.State == IntegrationIntegrated
		}}
		svc := NewService(fs, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)

		if _, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps()); err == nil {
			t.Fatal("Integrate reported success after the receipt write was lost")
		}
		merges := operationMerges(t, f.dir)
		if len(merges) != 1 {
			t.Fatalf("operation merges = %v, want the one merge that landed", merges)
		}
		got := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
		if got.Integration == nil || got.Integration.State != IntegrationIntent {
			t.Fatalf("integration after the lost write = %+v, want the journalled intent", got.Integration)
		}

		// Reconciliation proves the merge is this operation's, without merging.
		h := newMutationHarness()
		reconciled, err := svc.Reconcile(ctx, child.OwnerSessionID, MutationDeps{Idle: h.idle})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		rec := findRecord(t, reconciled, child.TaskKey)
		if rec.Integration.State != IntegrationIntegrated {
			t.Fatalf("reconciled integration = %+v, want the proven integrated receipt", rec.Integration)
		}
		if rec.Integration.ResultCommit != merges[0] {
			t.Fatalf("proven result commit = %s, want the landed merge %s", rec.Integration.ResultCommit, merges[0])
		}
		if rec.CleanupState != CleanupIntegratedPending {
			t.Fatalf("cleanup state after proof = %q, want %q", rec.CleanupState, CleanupIntegratedPending)
		}
		if got := operationMerges(t, f.dir); len(got) != 1 {
			t.Fatalf("reconciliation merged again: %v", got)
		}

		// The parent's retry returns that receipt and only finishes cleanup.
		retried, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
		if err != nil {
			t.Fatalf("retry Integrate: %v", err)
		}
		if retried.Integration.ResultCommit != merges[0] {
			t.Fatalf("retry receipt = %+v, want the original result %s", retried.Integration, merges[0])
		}
		if retried.CleanupState != CleanupComplete {
			t.Fatalf("cleanup state = %q, want %q", retried.CleanupState, CleanupComplete)
		}
		if got := operationMerges(t, f.dir); len(got) != 1 {
			t.Fatalf("the retry merged again: %v", got)
		}
	})

	t.Run("worktree removal failed after the receipt", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		fg := &faultGit{CLIGitOps: worktree.CLIGitOps{}, failWorktreeRemove: true}
		svc := NewService(store, fg)
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)

		rec, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
		if err != nil {
			t.Fatalf("a cleanup failure failed the integration: %v", err)
		}
		if rec.Integration.State != IntegrationIntegrated || rec.CleanupState != CleanupIntegratedPending {
			t.Fatalf("record = %+v, want an integrated receipt with pending cleanup", rec)
		}
		if _, err := os.Stat(child.Binding.Root); err != nil {
			t.Fatalf("the worktree is gone after a failed removal: %v", err)
		}
		// Observational reconciliation cannot remove artifacts without a fresh
		// admission, so the pending state stands.
		reconciled, err := svc.Reconcile(ctx, child.OwnerSessionID, MutationDeps{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got := findRecord(t, reconciled, child.TaskKey); got.CleanupState != CleanupIntegratedPending {
			t.Fatalf("cleanup state = %q, want it still pending", got.CleanupState)
		}
		if _, err := os.Stat(child.Binding.Root); err != nil {
			t.Fatalf("reconciliation removed the worktree without an admission: %v", err)
		}

		retried, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
		if err != nil {
			t.Fatalf("retry Integrate: %v", err)
		}
		if retried.CleanupState != CleanupComplete {
			t.Fatalf("cleanup state after the retry = %q, want %q", retried.CleanupState, CleanupComplete)
		}
		if _, err := os.Stat(child.Binding.Root); !os.IsNotExist(err) {
			t.Fatal("the retry did not remove the owned worktree")
		}
		if merges := operationMerges(t, f.dir); len(merges) != 1 {
			t.Fatalf("operation merges = %v, want exactly one", merges)
		}
	})

	t.Run("branch deletion failed after the receipt", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		fg := &faultGit{CLIGitOps: worktree.CLIGitOps{}, failBranchDelete: true}
		svc := NewService(store, fg)
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)

		rec, err := svc.Integrate(ctx, mutationFor(child), newMutationHarness().deps())
		if err != nil {
			t.Fatalf("a cleanup failure failed the integration: %v", err)
		}
		if rec.CleanupState != CleanupIntegratedPending {
			t.Fatalf("cleanup state = %q, want %q", rec.CleanupState, CleanupIntegratedPending)
		}
		if branches := ownedBranches(t, f.dir); len(branches) != 1 {
			t.Fatalf("owned branches = %v, want the undeleted branch", branches)
		}

		// The artifacts are removed by hand while no process is running: the
		// reopen proves the cleanup is complete instead of inventing an error.
		gitIn(t, f.dir, "branch", "-D", child.Binding.Branch)
		reopened := NewService(store, worktree.CLIGitOps{})
		reconciled, err := reopened.Reconcile(ctx, child.OwnerSessionID, MutationDeps{})
		if err != nil {
			t.Fatalf("Reconcile after reopen: %v", err)
		}
		got := findRecord(t, reconciled, child.TaskKey)
		if got.CleanupState != CleanupComplete {
			t.Fatalf("cleanup state = %q, want the proven %q", got.CleanupState, CleanupComplete)
		}
		if got.Integration.ResultCommit != rec.Integration.ResultCommit {
			t.Fatalf("the proven receipt = %+v, want the original %+v", got.Integration, rec.Integration)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 1 {
			t.Fatalf("operation merges = %v, want exactly one", merges)
		}
	})

	t.Run("reconcile never aborts a merge it cannot settle", func(t *testing.T) {
		f := newGitFixture(t)
		store := NewMemoryStore()
		svc := NewService(store, worktree.CLIGitOps{})
		child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeDone)

		// A recorded intent that never reached its merge.
		rec := mustRecord(t, store, child.OwnerSessionID, child.TaskKey)
		intent := rec
		intent.Integration = &Integration{
			OperationID:    "op-interrupted",
			Kind:           KindIntegrate,
			State:          IntegrationIntent,
			ExpectedSource: child.Candidate,
			BeforeTarget:   f.head(),
		}
		if _, err := store.CompareAndSwap(ctx, intent, rec.Revision); err != nil {
			t.Fatalf("CompareAndSwap: %v", err)
		}

		// Someone else's conflicted merge sits in the target.
		gitIn(t, f.dir, "checkout", "-b", "other")
		writeIn(t, f.dir, "base.txt", "other side\n")
		gitIn(t, f.dir, "add", "-A")
		gitIn(t, f.dir, "commit", "-m", "other side")
		other := gitIn(t, f.dir, "rev-parse", "HEAD")
		gitIn(t, f.dir, "checkout", "main")
		writeIn(t, f.dir, "base.txt", "main side\n")
		gitIn(t, f.dir, "add", "-A")
		gitIn(t, f.dir, "commit", "-m", "main side")
		if _, err := gitTry(t, f.dir, "merge", "other"); err == nil {
			t.Fatal("fixture: the foreign merge did not conflict")
		}

		// A startup reconcile has no admission, so it may only observe.
		reconciled, err := svc.Reconcile(ctx, child.OwnerSessionID, MutationDeps{})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		got := findRecord(t, reconciled, child.TaskKey)
		if got.Integration.State != IntegrationUnknown {
			t.Fatalf("integration = %+v, want unknown: the merge cannot be settled without an admission", got.Integration)
		}
		state, err := (worktree.CLIGitOps{}).MergeState(f.dir)
		if err != nil {
			t.Fatalf("MergeState: %v", err)
		}
		if !worktree.ParseMergeState(state).HasHead(other) {
			t.Fatalf("merge state = %q, want the foreign merge untouched", state)
		}
		if merges := operationMerges(t, f.dir); len(merges) != 0 {
			t.Fatalf("reconciliation merged: %v", merges)
		}
	})
}

// TestInterruptedModelIsNotResumedOrSuccessful pins the reopen path: available
// evidence survives a restart, the interrupted outcome is never promoted to
// success, and nothing runs — no provider turn, no merge.
func TestInterruptedModelIsNotResumedOrSuccessful(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})
	child := prepareChild(t, svc, f, "task-1", "one.txt", "one\n", OutcomeInterrupted)
	if child.Candidate == "" || child.ReportMessageID != 7 {
		t.Fatalf("interrupted record lost its evidence: %+v", child)
	}
	before := f.head()

	// A new process builds a service over the durable records.
	reopened := NewService(store, worktree.CLIGitOps{})
	reconciled, err := reopened.Reconcile(ctx, child.OwnerSessionID, MutationDeps{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	rec := findRecord(t, reconciled, child.TaskKey)
	if rec.Outcome != OutcomeInterrupted {
		t.Fatalf("outcome after reopen = %q, want %q", rec.Outcome, OutcomeInterrupted)
	}
	if rec.Candidate != child.Candidate || rec.ReportMessageID != 7 {
		t.Fatalf("reopened record = %+v, want the preserved candidate and report", rec)
	}
	if rec.Integration != nil {
		t.Fatalf("an interrupted run acquired an integration: %+v", rec.Integration)
	}

	// Inspection refreshes evidence without promoting the outcome.
	inspected, err := reopened.Inspect(ctx, child.OwnerSessionID, child.TaskKey)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspected.Outcome != OutcomeInterrupted || inspected.Candidate != child.Candidate {
		t.Fatalf("inspected record = %+v, want the interrupted outcome preserved", inspected)
	}

	// No merge ran, the source is retained, and the target never moved.
	if head := f.head(); head != before {
		t.Fatalf("the target moved to %s", head)
	}
	if merges := operationMerges(t, f.dir); len(merges) != 0 {
		t.Fatalf("reopening merged work: %v", merges)
	}
	if branches := ownedBranches(t, f.dir); len(branches) != 1 {
		t.Fatalf("owned branches = %v, want the interrupted source retained", branches)
	}
	if _, err := os.Stat(child.Binding.Root); err != nil {
		t.Fatalf("the interrupted source's worktree is gone: %v", err)
	}
	// An unknown outcome is never admitted for mutation either.
	unknown := inspected
	unknown.Outcome = OutcomeUnknown
	updated, err := store.CompareAndSwap(ctx, unknown, inspected.Revision)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if _, err := reopened.Integrate(ctx, mutationFor(updated), newMutationHarness().deps()); err == nil {
		t.Fatal("Integrate admitted a workspace whose execution is unknown")
	}
}
