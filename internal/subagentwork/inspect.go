package subagentwork

import (
	"context"
	"fmt"
)

// Inspect re-observes one retained task and records what it finds. It is the
// read path a parent uses after a restart or before integrating: the record it
// returns is the same durable record, refreshed with the workspace's actual
// candidate, commits, changed paths and dirtiness.
//
// Inspect records true unknowns rather than defaults. A workspace whose
// identity no longer matches the record — a replaced branch, a checkout
// re-pointed at another repository, a path that is now a symlink elsewhere —
// leaves an ownership error on the record and leaves the previously observed
// candidate untouched, so nothing downstream mistakes the tampered checkout for
// work to mutate. A workspace that cannot be read leaves Dirty nil, never
// clean.
//
// An observation that changes nothing does not advance the revision, so polling
// Inspect does not churn the record a concurrent writer is comparing against.
func (s *Service) Inspect(ctx context.Context, ownerSessionID, taskKey string) (Record, error) {
	if ownerSessionID == "" || taskKey == "" {
		return Record{}, fmt.Errorf("subagentwork: inspect needs an owner session and a task key")
	}
	rec, err := s.store.Get(ctx, ownerSessionID, taskKey)
	if err != nil {
		return Record{}, err
	}
	observed := s.observe(rec)
	if recordEqual(rec, observed) {
		// A read reconciliation still publishes: the caller learns the durable
		// state without a write, and the revision stays put.
		s.publish(rec)
		return rec, nil
	}
	written, err := s.store.CompareAndSwap(ctx, observed, rec.Revision)
	if err != nil {
		return Record{}, fmt.Errorf("subagentwork: record workspace observation: %w", err)
	}
	s.publish(written)
	return written, nil
}

// recordEqual reports whether two records carry the same values. Revision is
// compared as well, but an observation never changes it: identical revisions
// plus identical evidence means there is nothing to write.
func recordEqual(a, b Record) bool {
	if a.Version != b.Version || a.Revision != b.Revision ||
		a.OwnerSessionID != b.OwnerSessionID || a.TaskKey != b.TaskKey ||
		a.InvocationID != b.InvocationID || a.Binding != b.Binding ||
		a.Target != b.Target || a.Outcome != b.Outcome ||
		a.Candidate != b.Candidate || a.SourceError != b.SourceError ||
		a.ReportMessageID != b.ReportMessageID || a.CleanupState != b.CleanupState {
		return false
	}
	if !stringsEqual(a.Commits, b.Commits) || !stringsEqual(a.ChangedPaths, b.ChangedPaths) ||
		!stringsEqual(a.SetupWarnings, b.SetupWarnings) {
		return false
	}
	if (a.Dirty == nil) != (b.Dirty == nil) || (a.Dirty != nil && *a.Dirty != *b.Dirty) {
		return false
	}
	if (a.Integration == nil) != (b.Integration == nil) ||
		(a.Integration != nil && *a.Integration != *b.Integration) {
		return false
	}
	if len(a.Verification) != len(b.Verification) {
		return false
	}
	for i := range a.Verification {
		av, bv := a.Verification[i], b.Verification[i]
		if av.Command != bv.Command || av.Outcome != bv.Outcome {
			return false
		}
		if (av.ExitCode == nil) != (bv.ExitCode == nil) || (av.ExitCode != nil && *av.ExitCode != *bv.ExitCode) {
			return false
		}
	}
	if len(a.SetupPlan.Hooks) != len(b.SetupPlan.Hooks) {
		return false
	}
	for i := range a.SetupPlan.Hooks {
		if a.SetupPlan.Hooks[i] != b.SetupPlan.Hooks[i] {
			return false
		}
	}
	return true
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
