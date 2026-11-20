package subagentwork

import (
	"context"
	"fmt"
	"os"

	"marshal/internal/worktree"
)

// Reconcile observes one owner's records after a restart and persists only what
// it can prove. It is deliberately observational: it never aborts a merge and
// never removes an artifact, because neither is possible without a fresh
// per-call admission. A retry of the parent's tool obtains that proof before
// pending cleanup runs.
//
// What it can prove is enough to keep the record truthful: an intent whose merge
// landed is recorded as integrated with that exact result, a target that moved
// somewhere else is recorded as unknown, and a settled operation whose owned
// artifacts are already gone is recorded as cleaned up rather than as an error.
func (s *Service) Reconcile(ctx context.Context, ownerSessionID string, deps MutationDeps) ([]Record, error) {
	if ownerSessionID == "" {
		return nil, fmt.Errorf("subagentwork: reconcile needs an owner session")
	}
	records, err := s.store.List(ctx, ownerSessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(records))
	for _, rec := range records {
		reconciled, err := s.reconcileRecord(ctx, deps, rec)
		if err != nil {
			return out, err
		}
		out = append(out, reconciled)
	}
	return out, nil
}

func (s *Service) reconcileRecord(ctx context.Context, deps MutationDeps, rec Record) (Record, error) {
	if rec.Integration == nil {
		// An execution outcome is not a lifecycle result: reconciling a record
		// that was never mutated leaves it exactly as the child reported it.
		return rec, nil
	}
	git, ok := s.git.(MutationGit)
	if !ok {
		return rec, nil
	}
	// Reconciliation reads the same roots a mutation would, so it takes the same
	// lease when one is available. A startup with no gate observes without it.
	release := func() {}
	if deps.Idle != nil {
		r, err := deps.Idle.AcquireIdle(ctx, rec.OwnerSessionID, mutationRoots(rec))
		if err != nil {
			// Another process holds these roots: its own reconciliation will see
			// the truth, so this one observes nothing rather than guessing.
			return rec, nil
		}
		release = r
	}
	defer release()

	switch rec.Integration.State {
	case IntegrationIntent, IntegrationUnknown:
		return s.proveIntegration(ctx, git, rec)
	case IntegrationIntegrated, IntegrationNoChanges, IntegrationDiscarded:
		if rec.CleanupState == CleanupComplete {
			return rec, nil
		}
		return s.proveCleanup(ctx, git, rec)
	}
	return rec, nil
}

// proveIntegration settles a recorded intent by the only facts that prove it: the
// exact source candidate is reachable from the recorded target, and — when a new
// merge is what put it there — that merge's parents are this operation's
// recorded before-target and candidate.
func (s *Service) proveIntegration(ctx context.Context, git MutationGit, rec Record) (Record, error) {
	integ := *rec.Integration
	if integ.ExpectedSource == "" || rec.Target.Root == "" || rec.Target.Repository == "" {
		return rec, nil
	}
	target, err := worktree.CaptureCheckoutIdentity(s.git, rec.Target.Root)
	if err != nil || target.Repository != rec.Target.Repository {
		// The checkout cannot be read or is no longer the recorded one: nothing
		// here is provable, and following a changed repository is a refusal.
		return rec, nil
	}
	if target.Head == integ.BeforeTarget {
		state, err := git.MergeState(target.Root)
		if err != nil {
			return rec, nil
		}
		if state != "" {
			// The operation's own merge is still in progress. Settling it needs
			// an admission this call does not have, so record what is true.
			note := integ
			note.State = IntegrationUnknown
			note.Reason = fmt.Sprintf("the target holds an in-progress merge of %v; a fresh admission is required to settle it", worktree.ParseMergeState(state).Heads)
			return s.rewriteIntegration(ctx, rec, note, rec.CleanupState)
		}
		// Nothing landed. The journalled intent is the truth as it stands.
		return rec, nil
	}

	settled := integ
	settled.State = IntegrationIntegrated
	settled.ResultCommit = target.Head
	cleanup := rec.CleanupState
	if cleanup != CleanupComplete {
		cleanup = CleanupIntegratedPending
	}

	if parents, err := git.Parents(target.Root, target.Head); err == nil && recordsMerge(parents, integ.BeforeTarget, integ.ExpectedSource) {
		settled.Reason = "the operation's merge is recorded in the target"
		return s.rewriteIntegration(ctx, rec, settled, cleanup)
	}
	present, err := s.git.IsAncestor(target.Root, integ.ExpectedSource, target.Head)
	if err != nil {
		return rec, nil
	}
	if present {
		settled.Reason = "the candidate is reachable from the target, so no merge was needed"
		return s.rewriteIntegration(ctx, rec, settled, cleanup)
	}

	unknown := integ
	unknown.State = IntegrationUnknown
	unknown.Reason = fmt.Sprintf("the target advanced to %s without the recorded candidate %s", target.Head, integ.ExpectedSource)
	return s.rewriteIntegration(ctx, rec, unknown, rec.CleanupState)
}

// proveCleanup records a completed cleanup when the owned artifacts are already
// gone. It removes nothing: that needs a fresh admission.
func (s *Service) proveCleanup(ctx context.Context, git MutationGit, rec Record) (Record, error) {
	target, err := s.cleanupCheckout(rec)
	if err != nil {
		return rec, nil
	}
	if rec.Binding.Root != "" && rec.Binding.Mode == ModeIsolated {
		if _, err := os.Stat(rec.Binding.Root); err == nil {
			return rec, nil
		} else if !os.IsNotExist(err) {
			return rec, nil
		}
	}
	if rec.Binding.Branch != "" && git.BranchExists(target.Root, rec.Binding.Branch) {
		return rec, nil
	}
	complete := rec
	complete.CleanupState = CleanupComplete
	written, err := s.store.CompareAndSwap(ctx, complete, rec.Revision)
	if err != nil {
		return rec, nil
	}
	s.publish(written)
	return written, nil
}

// rewriteIntegration persists a proven journal state, leaving the record alone
// when it already says exactly that.
func (s *Service) rewriteIntegration(ctx context.Context, rec Record, integ Integration, cleanup string) (Record, error) {
	if rec.Integration != nil && *rec.Integration == integ && rec.CleanupState == cleanup {
		return rec, nil
	}
	written, err := s.writeMutation(ctx, rec, integ, cleanup)
	if err != nil {
		return rec, nil
	}
	return written, nil
}

// mutationRoots is every root a mutation or its reconciliation touches.
func mutationRoots(rec Record) []string {
	var roots []string
	if rec.Binding.Root != "" {
		roots = append(roots, rec.Binding.Root)
	}
	if rec.Target.Root != "" {
		roots = append(roots, rec.Target.Root)
	}
	return roots
}
