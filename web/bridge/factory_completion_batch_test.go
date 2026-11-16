package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// reviewedPR is one pull request at the identity a user reviewed: a node, a
// head and a base a merge batch pins and a forge can move.
func reviewedPR(number int) FactoryPR {
	return FactoryPR{
		Number:  number,
		URL:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", number),
		NodeID:  fmt.Sprintf("node-%d", number),
		HeadRef: "marshal/factory/item",
		BaseRef: "main",
		HeadSHA: fmt.Sprintf("head-%d", number),
		BaseSHA: "base",
		State:   "open",
	}
}

// batchForge is a forge double for the merge batches: it answers GetFactoryPR
// from a table, applies a merge to that table, and records every call, so a
// test can assert exactly how many merges were sent and in what order.
//
// mergeErr is what the merge call itself answers, and landAnyway says whether
// the merge happened regardless — the lost response, where the answer and the
// outcome disagree.
type batchForge struct {
	factoryForgeStubs
	prs        map[int]FactoryPR
	gets       []int
	merges     []FactoryPRMergeRequest
	mergeErr   error
	landAnyway bool
	readErr    error
}

func (f *batchForge) ObserveFactoryCI(context.Context, Repo, string, Credential) (FactoryCI, error) {
	return FactoryCI{}, errors.New("merge batches do not observe CI")
}

func (f *batchForge) FindFactoryPR(context.Context, Repo, string, string, Credential) (*FactoryPR, error) {
	return nil, errors.New("merge batches do not find pull requests")
}

func (f *batchForge) SetFactoryDraft(context.Context, Repo, FactoryPR, bool, Credential) error {
	return errors.New("merge batches do not set drafts")
}

func (f *batchForge) GetFactoryPR(_ context.Context, _ Repo, number int, _ Credential) (FactoryPR, error) {
	f.gets = append(f.gets, number)
	if f.readErr != nil {
		return FactoryPR{}, f.readErr
	}
	pr, ok := f.prs[number]
	if !ok {
		return FactoryPR{}, fmt.Errorf("pull request %d not found", number)
	}
	return pr, nil
}

func (f *batchForge) MergeFactoryPR(_ context.Context, _ Repo, req FactoryPRMergeRequest, _ Credential) error {
	f.merges = append(f.merges, req)
	if f.mergeErr == nil || f.landAnyway {
		f.apply(req)
	}
	return f.mergeErr
}

// apply merges the pull request only when the request still names the reviewed
// identity, so a merge sent for a head the pull request has left changes
// nothing, exactly as a forge would refuse it.
func (f *batchForge) apply(req FactoryPRMergeRequest) {
	pr, ok := f.prs[req.Number]
	if !ok || pr.NodeID != req.NodeID || pr.HeadSHA != req.HeadSHA || pr.BaseRef != req.BaseRef {
		return
	}
	pr.State = "merged"
	f.prs[req.Number] = pr
}

// readOnlyBatchForge is a forge with no merge capability: it reads through the
// same table as batchForge and promotes none of its methods, so the merge
// capability is genuinely absent.
type readOnlyBatchForge struct {
	factoryForgeStubs
	inner *batchForge
}

func (f *readOnlyBatchForge) ObserveFactoryCI(context.Context, Repo, string, Credential) (FactoryCI, error) {
	return FactoryCI{}, errors.New("a read-only forge does not observe CI")
}

func (f *readOnlyBatchForge) FindFactoryPR(context.Context, Repo, string, string, Credential) (*FactoryPR, error) {
	return nil, errors.New("a read-only forge does not find pull requests")
}

func (f *readOnlyBatchForge) SetFactoryDraft(context.Context, Repo, FactoryPR, bool, Credential) error {
	return errors.New("a read-only forge does not set drafts")
}

func (f *readOnlyBatchForge) GetFactoryPR(ctx context.Context, repo Repo, number int, cred Credential) (FactoryPR, error) {
	return f.inner.GetFactoryPR(ctx, repo, number, cred)
}

// batchEffects is a controller effects double whose forge opener is pinned, so
// no test reaches the network.
type batchEffects struct {
	*loopEffects
	repo    Repo
	base    Forge
	factory FactoryForge
}

func (e *batchEffects) forge(_ context.Context, _ FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
	return e.repo, e.base, e.factory, Credential{}, nil
}

// batchController is a controller over s whose forge is forge, reached both as
// the read capability and as the base forge the merge capability is looked up
// on.
func batchController(s *FactoryStore, forge Forge, factory FactoryForge, clock func() time.Time) *FactoryController {
	effects := &batchEffects{loopEffects: &loopEffects{store: s}, repo: linkRepo(), base: forge, factory: factory}
	return NewFactoryController(s, effects, clock)
}

// batchJournal opens a temporary journal holding one approved item whose
// completion links every reviewed pull request, in the order given.
func batchJournal(t *testing.T, now time.Time, numbers ...int) (*FactoryStore, FactoryItem) {
	t.Helper()
	s, err := OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	req := factoryReq()
	req.Config.TargetBranch = "main"
	created, err := s.Create(req, now)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Update(created.ID, "batch-approve", created.Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: now}}
		dst.State = "checking"
		dst.BaseSHA = "base"
		completion := factoryCompletionOf(*dst)
		for _, n := range numbers {
			completion.Links = append(completion.Links, FactoryPRLink{RepoID: "repo", PR: reviewedPR(n), Origin: "user", ObservedAt: now, Fresh: true})
		}
		dst.Completion = &completion
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return s, i
}

// mergeCommand confirms the reviewed pull requests in order. The kind decides
// the completion intent, exactly as a surface sends it.
func mergeCommand(kind, strategy, runChoice string, numbers ...int) *FactoryCompletionCommand {
	prs := make([]FactoryCompletionPR, 0, len(numbers))
	for _, n := range numbers {
		pr := reviewedPR(n)
		prs = append(prs, FactoryCompletionPR{RepoID: "repo", Number: pr.Number, NodeID: pr.NodeID, HeadSHA: pr.HeadSHA, BaseRef: pr.BaseRef})
	}
	return &FactoryCompletionCommand{PRs: prs, Strategy: strategy, CompleteAfter: kind == "merge_and_complete", RunChoice: runChoice}
}

// authorizeBatch confirms one batch and requires it to be accepted.
func authorizeBatch(t *testing.T, c *FactoryController, item FactoryItem, requestID, kind, strategy, runChoice string, numbers ...int) FactoryItem {
	t.Helper()
	i, err := c.Command(context.Background(), item.ID, FactoryCommand{
		RequestID:       requestID,
		Kind:            kind,
		ExpectedVersion: item.Version,
		Completion:      mergeCommand(kind, strategy, runChoice, numbers...),
	})
	if err != nil {
		t.Fatalf("%s %s: %v", kind, requestID, err)
	}
	return i
}

// advanceBatches runs one pass of the batch driver.
func advanceBatches(t *testing.T, c *FactoryController) {
	t.Helper()
	if err := c.advanceCompletionBatches(context.Background()); err != nil {
		t.Fatalf("advance: %v", err)
	}
}

// currentItem re-reads one item from the journal.
func currentItem(t *testing.T, s *FactoryStore, id string) FactoryItem {
	t.Helper()
	i, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// seedBatch writes a batch straight into the journal, which is how a crash at
// one boundary leaves the item: no command applied it, the journal just holds
// what the last completed phase wrote.
func seedBatch(t *testing.T, s *FactoryStore, item FactoryItem, batch FactoryMergeBatch) FactoryItem {
	t.Helper()
	saved, err := s.Update(item.ID, "seed-"+batch.ID, item.Version, json.RawMessage(`{"seed":true}`), func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		completion.Batch = &batch
		dst.Completion = &completion
		return nil
	}, item.UpdatedAt)
	if err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	return saved
}

// seedStep is one reviewed step in the state a crash left it in.
func seedStep(number int, state, result string) FactoryMergeStep {
	pr := reviewedPR(number)
	return FactoryMergeStep{
		RepoID:   "repo",
		Number:   pr.Number,
		NodeID:   pr.NodeID,
		HeadSHA:  pr.HeadSHA,
		BaseRef:  pr.BaseRef,
		Strategy: "squash",
		State:    state,
		Result:   result,
	}
}

// TestCompletionBatchOrder covers the ordered batch a confirmation journals and
// the one-reviewed-merge-at-a-time drive over it: the first entry merges and
// confirms before the second is touched, the item completes only after the last
// entry confirms, and the completion is recorded once.
func TestCompletionBatchOrder(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	s, item := batchJournal(t, now, 12, 13)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}}
	c := batchController(s, forge, forge, func() time.Time { return now })

	confirmed := authorizeBatch(t, c, item, "merge-order", "merge_and_complete", "squash", "", 12, 13)
	batch := confirmed.Completion.Batch
	if batch == nil {
		t.Fatal("the confirmation journalled no batch")
	}
	if batch.ID != "batch-"+item.ID+"-merge-order" || batch.RequestID != "merge-order" || batch.State != "active" || !batch.CompleteAfter {
		t.Fatalf("batch identity: %+v", batch)
	}
	if len(batch.Steps) != 2 || batch.Steps[0].Number != 12 || batch.Steps[1].Number != 13 {
		t.Fatalf("reviewed order: %+v", batch.Steps)
	}
	for _, step := range batch.Steps {
		if step.State != "unattempted" || step.Strategy != "squash" || step.HeadSHA != fmt.Sprintf("head-%d", step.Number) || step.BaseRef != "main" || step.RepoID != "repo" {
			t.Fatalf("reviewed step: %+v", step)
		}
	}
	if ev := confirmed.History[len(confirmed.History)-1]; ev.Kind != "completion_batch_authorized" || !strings.Contains(ev.Message, "squash") || !strings.Contains(ev.Message, "#12, #13") {
		t.Fatalf("authorization event: %+v", ev)
	}
	// Authorization names no external effect: nothing is merged before the
	// driver reads the batch.
	if len(forge.merges) != 0 || len(forge.gets) != 0 {
		t.Fatalf("the confirmation reached the forge: %v %v", forge.merges, forge.gets)
	}

	advanceBatches(t, c)
	if len(forge.merges) != 1 {
		t.Fatalf("merges after one pass: %v", forge.merges)
	}
	if m := forge.merges[0]; m.Number != 12 || m.Strategy != "squash" || m.HeadSHA != "head-12" || m.BaseRef != "main" || m.NodeID != "node-12" {
		t.Fatalf("the first merge was not the reviewed one: %+v", m)
	}
	first := currentItem(t, s, item.ID)
	if first.Completion.Batch.Steps[0].State != "confirmed" || first.Completion.Batch.Steps[1].State != "unattempted" {
		t.Fatalf("steps after the first merge: %+v", first.Completion.Batch.Steps)
	}
	if factoryCompletionStatus(first) != "open" || len(first.Completion.Records) != 0 {
		t.Fatalf("the item completed before its last merge: %+v", first.Completion)
	}

	advanceBatches(t, c)
	if len(forge.merges) != 2 || forge.merges[1].Number != 13 {
		t.Fatalf("merges after two passes: %v", forge.merges)
	}
	final := currentItem(t, s, item.ID)
	if final.Completion.Batch.State != "complete" {
		t.Fatalf("batch: %+v", final.Completion.Batch)
	}
	if factoryCompletionStatus(final) != "complete" || len(final.Completion.Records) != 1 {
		t.Fatalf("completion: %+v", final.Completion)
	}
	record := final.Completion.Records[0]
	if record.ID != "completion-"+final.Completion.Batch.ID || record.Actor != "studio" || record.Goal != final.Title || record.Criteria != "spec" {
		t.Fatalf("completion record: %+v", record)
	}
	// The links the batch confirmed render the merge that confirmed them.
	for _, l := range final.Completion.Links {
		if l.PR.State != "merged" || !l.Fresh || !l.ObservedAt.Equal(now) {
			t.Fatalf("link after its merge: %+v", l)
		}
	}
	// A completed batch is history: a further pass merges nothing.
	advanceBatches(t, c)
	if len(forge.merges) != 2 {
		t.Fatalf("a completed batch merged again: %v", forge.merges)
	}
}

// TestCompletionBatchStopsOnFailure covers a refused merge and a forge with no
// merge capability: both fail the entry, stop the batch, leave every later
// entry unattempted and never resend anything.
func TestCompletionBatchStopsOnFailure(t *testing.T) {
	now := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)

	t.Run("refused", func(t *testing.T) {
		s, item := batchJournal(t, now, 12, 13)
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}, mergeErr: fmt.Errorf("%w: required checks are not green", ErrFactoryMergeRejected)}
		c := batchController(s, forge, forge, func() time.Time { return now })
		authorizeBatch(t, c, item, "merge-refused", "merge_and_complete", "merge", "", 12, 13)

		advanceBatches(t, c)
		if len(forge.merges) != 1 || forge.merges[0].Number != 12 {
			t.Fatalf("merges: %v", forge.merges)
		}
		stopped := currentItem(t, s, item.ID)
		steps := stopped.Completion.Batch.Steps
		if stopped.Completion.Batch.State != "stopped" || steps[0].State != "failed" || !strings.Contains(steps[0].Result, "refused") {
			t.Fatalf("stopped batch: %+v", stopped.Completion.Batch)
		}
		if steps[1].State != "unattempted" {
			t.Fatalf("a later entry moved after a failure: %+v", steps[1])
		}
		if factoryCompletionStatus(stopped) != "open" || len(stopped.Completion.Records) != 0 {
			t.Fatalf("a failed batch completed the item: %+v", stopped.Completion)
		}
		// A stopped batch waits for a new decision and fresh evidence: the
		// driver neither merges nor reconciles it again.
		advanceBatches(t, c)
		advanceBatches(t, c)
		if len(forge.merges) != 1 {
			t.Fatalf("a stopped batch resent a merge: %v", forge.merges)
		}
		again := currentItem(t, s, item.ID)
		if again.Version != stopped.Version {
			t.Fatalf("a stopped batch moved: %+v", again.Completion.Batch)
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		s, item := batchJournal(t, now, 12)
		readable := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
		readOnly := &readOnlyBatchForge{inner: readable}
		c := batchController(s, readOnly, readOnly, func() time.Time { return now })
		authorizeBatch(t, c, item, "merge-unsupported", "merge_pr", "squash", "", 12)

		advanceBatches(t, c)
		unsupported := currentItem(t, s, item.ID)
		step := unsupported.Completion.Batch.Steps[0]
		if unsupported.Completion.Batch.State != "stopped" || step.State != "failed" || !strings.Contains(step.Result, "does not support merging") {
			t.Fatalf("unsupported merge: %+v", unsupported.Completion.Batch)
		}
		if len(readable.merges) != 0 {
			t.Fatalf("a forge without the capability was merged on: %v", readable.merges)
		}
	})
}

// TestCompletionBatchChangedHead covers the evidence a review no longer
// describes: a moved head, a moved base and a closed pull request each fail the
// entry and stop the batch without a merge being sent.
func TestCompletionBatchChangedHead(t *testing.T) {
	now := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		moved func(FactoryPR) FactoryPR
	}{
		{name: "head", moved: func(pr FactoryPR) FactoryPR { pr.HeadSHA = "head-moved"; return pr }},
		{name: "base", moved: func(pr FactoryPR) FactoryPR { pr.BaseRef = "release"; return pr }},
		{name: "node", moved: func(pr FactoryPR) FactoryPR { pr.NodeID = "node-replaced"; return pr }},
		{name: "closed", moved: func(pr FactoryPR) FactoryPR { pr.State = "closed"; return pr }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, item := batchJournal(t, now, 12)
			forge := &batchForge{prs: map[int]FactoryPR{12: tc.moved(reviewedPR(12))}}
			c := batchController(s, forge, forge, func() time.Time { return now })
			authorizeBatch(t, c, item, "merge-changed-"+tc.name, "merge_and_complete", "squash", "", 12)

			advanceBatches(t, c)
			changed := currentItem(t, s, item.ID)
			step := changed.Completion.Batch.Steps[0]
			if changed.Completion.Batch.State != "stopped" || step.State != "failed" {
				t.Fatalf("changed evidence: %+v", changed.Completion.Batch)
			}
			if step.Result == "" {
				t.Fatal("the stop recorded no reason")
			}
			if factoryCompletionStatus(changed) != "open" || len(changed.Completion.Records) != 0 {
				t.Fatalf("changed evidence completed the item: %+v", changed.Completion)
			}
			// A stale review is never adapted into a merge.
			if len(forge.merges) != 0 {
				t.Fatalf("a reviewed-away head was merged: %v", forge.merges)
			}
		})
	}
}

// TestCompletionBatchLostResponse covers a merge whose answer was lost: the
// pull request's own state settles it when it landed, and when it did not the
// entry is uncertain, the batch stops, and the merge is never sent twice.
func TestCompletionBatchLostResponse(t *testing.T) {
	now := time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC)

	t.Run("landed", func(t *testing.T) {
		s, item := batchJournal(t, now, 12)
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}, mergeErr: fmt.Errorf("%w: connection reset", ErrFactoryMergeUncertain), landAnyway: true}
		c := batchController(s, forge, forge, func() time.Time { return now })
		authorizeBatch(t, c, item, "merge-lost-landed", "merge_and_complete", "squash", "", 12)

		advanceBatches(t, c)
		if len(forge.merges) != 1 {
			t.Fatalf("merges: %v", forge.merges)
		}
		landed := currentItem(t, s, item.ID)
		if landed.Completion.Batch.State != "complete" || landed.Completion.Batch.Steps[0].State != "confirmed" {
			t.Fatalf("a proven merge was not confirmed: %+v", landed.Completion.Batch)
		}
		if factoryCompletionStatus(landed) != "complete" || len(landed.Completion.Records) != 1 {
			t.Fatalf("completion: %+v", landed.Completion)
		}
		advanceBatches(t, c)
		if len(forge.merges) != 1 {
			t.Fatalf("the proven merge was resent: %v", forge.merges)
		}
	})

	t.Run("unproven", func(t *testing.T) {
		s, item := batchJournal(t, now, 12, 13)
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}, mergeErr: fmt.Errorf("%w: connection reset", ErrFactoryMergeUncertain)}
		c := batchController(s, forge, forge, func() time.Time { return now })
		authorizeBatch(t, c, item, "merge-lost-unproven", "merge_and_complete", "squash", "", 12, 13)

		advanceBatches(t, c)
		if len(forge.merges) != 1 {
			t.Fatalf("merges: %v", forge.merges)
		}
		unproven := currentItem(t, s, item.ID)
		steps := unproven.Completion.Batch.Steps
		if unproven.Completion.Batch.State != "stopped" || steps[0].State != "uncertain" || !strings.Contains(steps[0].Result, "unproven") {
			t.Fatalf("uncertain merge: %+v", unproven.Completion.Batch)
		}
		if steps[1].State != "unattempted" {
			t.Fatalf("a later entry moved after an uncertain merge: %+v", steps[1])
		}
		if factoryCompletionStatus(unproven) != "open" || len(unproven.Completion.Records) != 0 {
			t.Fatalf("an uncertain merge completed the item: %+v", unproven.Completion)
		}
		// Uncertainty is never resolved by sending the merge again.
		advanceBatches(t, c)
		advanceBatches(t, c)
		if len(forge.merges) != 1 {
			t.Fatalf("an uncertain merge was resent: %v", forge.merges)
		}
	})
}

// TestCompletionBatchRecoveryBoundaries covers the journal each crash boundary
// leaves behind: a prepared entry, a dispatched entry whose merge landed, one
// whose merge did not, and an all-confirmed batch whose completion was never
// written. Each is reconciled from the pull request's own state, and the
// completion is recorded exactly once.
func TestCompletionBatchRecoveryBoundaries(t *testing.T) {
	now := time.Date(2026, 5, 14, 8, 0, 0, 0, time.UTC)

	t.Run("prepared", func(t *testing.T) {
		s, item := batchJournal(t, now, 12)
		seeded := seedBatch(t, s, item, FactoryMergeBatch{ID: "batch-seed", RequestID: "merge-seed", State: "active", CompleteAfter: true, Steps: []FactoryMergeStep{seedStep(12, "prepared", "")}})
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
		c := batchController(s, forge, forge, func() time.Time { return now })

		advanceBatches(t, c)
		if len(forge.merges) != 1 || forge.merges[0].Number != 12 {
			t.Fatalf("a prepared entry was not dispatched: %v", forge.merges)
		}
		recovered := currentItem(t, s, seeded.ID)
		if recovered.Completion.Batch.State != "complete" || recovered.Completion.Batch.Steps[0].State != "confirmed" {
			t.Fatalf("prepared recovery: %+v", recovered.Completion.Batch)
		}
		if factoryCompletionStatus(recovered) != "complete" || len(recovered.Completion.Records) != 1 {
			t.Fatalf("prepared completion: %+v", recovered.Completion)
		}
	})

	t.Run("dispatched merged", func(t *testing.T) {
		s, item := batchJournal(t, now, 12)
		seeded := seedBatch(t, s, item, FactoryMergeBatch{ID: "batch-seed", RequestID: "merge-seed", State: "active", CompleteAfter: true, Steps: []FactoryMergeStep{seedStep(12, "dispatched", "")}})
		merged := reviewedPR(12)
		merged.State = "merged"
		forge := &batchForge{prs: map[int]FactoryPR{12: merged}}
		c := batchController(s, forge, forge, func() time.Time { return now })

		advanceBatches(t, c)
		recovered := currentItem(t, s, seeded.ID)
		if recovered.Completion.Batch.State != "complete" || recovered.Completion.Batch.Steps[0].State != "confirmed" {
			t.Fatalf("dispatched recovery: %+v", recovered.Completion.Batch)
		}
		if factoryCompletionStatus(recovered) != "complete" || len(recovered.Completion.Records) != 1 || recovered.Completion.Records[0].ID != "completion-batch-seed" {
			t.Fatalf("dispatched completion: %+v", recovered.Completion)
		}
		// The merge that landed before the crash is not sent a second time.
		if len(forge.merges) != 0 {
			t.Fatalf("a reconciled merge was resent: %v", forge.merges)
		}
		if len(recovered.Completion.Links) != 1 || recovered.Completion.Links[0].PR.State != "merged" || !recovered.Completion.Links[0].Fresh {
			t.Fatalf("the reconciled link was not refreshed: %+v", recovered.Completion.Links)
		}
	})

	t.Run("dispatched open", func(t *testing.T) {
		s, item := batchJournal(t, now, 12)
		seeded := seedBatch(t, s, item, FactoryMergeBatch{ID: "batch-seed", RequestID: "merge-seed", State: "active", CompleteAfter: true, Steps: []FactoryMergeStep{seedStep(12, "dispatched", "")}})
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
		c := batchController(s, forge, forge, func() time.Time { return now })

		advanceBatches(t, c)
		recovered := currentItem(t, s, seeded.ID)
		steps := recovered.Completion.Batch.Steps
		if recovered.Completion.Batch.State != "stopped" || steps[0].State != "uncertain" {
			t.Fatalf("unprovable dispatch: %+v", recovered.Completion.Batch)
		}
		if factoryCompletionStatus(recovered) != "open" || len(recovered.Completion.Records) != 0 {
			t.Fatalf("an uncertain dispatch completed the item: %+v", recovered.Completion)
		}
		// A dispatched merge whose outcome is unproven is never resent.
		advanceBatches(t, c)
		if len(forge.merges) != 0 {
			t.Fatalf("an unproven dispatch was resent: %v", forge.merges)
		}
	})

	t.Run("dispatched unobservable", func(t *testing.T) {
		s, item := batchJournal(t, now, 12)
		seeded := seedBatch(t, s, item, FactoryMergeBatch{ID: "batch-seed", RequestID: "merge-seed", State: "active", CompleteAfter: true, Steps: []FactoryMergeStep{seedStep(12, "dispatched", "")}})
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}, readErr: errors.New("forge unreachable")}
		c := batchController(s, forge, forge, func() time.Time { return now })

		advanceBatches(t, c)
		recovered := currentItem(t, s, seeded.ID)
		steps := recovered.Completion.Batch.Steps
		if recovered.Completion.Batch.State != "stopped" || steps[0].State != "uncertain" || !strings.Contains(steps[0].Result, "could not be observed") {
			t.Fatalf("unobservable dispatch: %+v", recovered.Completion.Batch)
		}
		if len(forge.merges) != 0 {
			t.Fatalf("an unobservable dispatch was resent: %v", forge.merges)
		}
	})

	t.Run("confirmed without completion", func(t *testing.T) {
		s, item := batchJournal(t, now, 12, 13)
		seeded := seedBatch(t, s, item, FactoryMergeBatch{ID: "batch-seed", RequestID: "merge-seed", State: "active", CompleteAfter: true, RunChoice: "continue", Steps: []FactoryMergeStep{seedStep(12, "confirmed", "merged"), seedStep(13, "confirmed", "merged")}})
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}}
		c := batchController(s, forge, forge, func() time.Time { return now })

		advanceBatches(t, c)
		recovered := currentItem(t, s, seeded.ID)
		if recovered.Completion.Batch.State != "complete" || factoryCompletionStatus(recovered) != "complete" {
			t.Fatalf("all-confirmed recovery: %+v", recovered.Completion)
		}
		if len(recovered.Completion.Records) != 1 || recovered.Completion.Records[0].ID != "completion-batch-seed" || recovered.Completion.Records[0].RunChoice != "continue" {
			t.Fatalf("completion record: %+v", recovered.Completion.Records)
		}
		if len(forge.merges) != 0 || len(forge.gets) != 0 {
			t.Fatalf("a confirmed batch reached the forge: %v %v", forge.merges, forge.gets)
		}
		// The completion is recorded once, and a further pass adds nothing.
		advanceBatches(t, c)
		again := currentItem(t, s, seeded.ID)
		if again.Version != recovered.Version || len(again.Completion.Records) != 1 {
			t.Fatalf("recovery wrote a second completion: %+v", again.Completion.Records)
		}
	})

	t.Run("completion already recorded", func(t *testing.T) {
		// A reopen that landed after the merges keeps its decision: the
		// deterministic record identity is why recovery cannot append a second
		// copy of a completion the journal already holds.
		s, item := batchJournal(t, now, 12)
		seeded, err := s.Update(item.ID, "seed-recorded", item.Version, json.RawMessage(`{"seed":true}`), func(dst *FactoryItem) error {
			completion := factoryCompletionOf(*dst)
			completion.Records = []FactoryCompletionRecord{{ID: "completion-batch-seed", At: now, Actor: "studio", Goal: dst.Title, Criteria: "spec"}}
			completion.Batch = &FactoryMergeBatch{ID: "batch-seed", RequestID: "merge-seed", State: "active", CompleteAfter: true, Steps: []FactoryMergeStep{seedStep(12, "confirmed", "merged")}}
			dst.Completion = &completion
			return nil
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
		c := batchController(s, forge, forge, func() time.Time { return now })

		advanceBatches(t, c)
		recovered := currentItem(t, s, seeded.ID)
		if len(recovered.Completion.Records) != 1 {
			t.Fatalf("recovery duplicated the completion: %+v", recovered.Completion.Records)
		}
		if recovered.Completion.Batch.State != "complete" {
			t.Fatalf("batch: %+v", recovered.Completion.Batch)
		}
		if factoryCompletionStatus(recovered) != "open" {
			t.Fatalf("recovery overrode a later reopen: %+v", recovered.Completion)
		}
	})
}

// TestCompletionBatchExternalMerge covers a reviewed pull request somebody else
// already merged: the entry is confirmed with that provenance, no merge is
// sent, and the item completes from the merge the review authorized.
func TestCompletionBatchExternalMerge(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	s, item := batchJournal(t, now, 12)
	merged := reviewedPR(12)
	merged.State = "merged"
	forge := &batchForge{prs: map[int]FactoryPR{12: merged}}
	c := batchController(s, forge, forge, func() time.Time { return now })
	authorizeBatch(t, c, item, "merge-external", "merge_and_complete", "squash", "", 12)

	advanceBatches(t, c)
	external := currentItem(t, s, item.ID)
	step := external.Completion.Batch.Steps[0]
	if external.Completion.Batch.State != "complete" || step.State != "confirmed" || step.Result != "external merge" {
		t.Fatalf("external merge: %+v", external.Completion.Batch)
	}
	if len(forge.merges) != 0 {
		t.Fatalf("an externally merged pull request was merged again: %v", forge.merges)
	}
	if factoryCompletionStatus(external) != "complete" || len(external.Completion.Records) != 1 {
		t.Fatalf("completion: %+v", external.Completion)
	}
	if len(external.Completion.Links) != 1 || external.Completion.Links[0].PR.State != "merged" || !external.Completion.Links[0].Fresh {
		t.Fatalf("external link: %+v", external.Completion.Links)
	}
}

// TestCompletionBatchConcurrentConfirmation covers what a confirmation is
// refused for: a batch that is still active, evidence that moved, a review the
// journal cannot bind, a missing run choice, and an MCP client. It also covers
// the confirmation that is allowed again once a batch has stopped.
func TestCompletionBatchConcurrentConfirmation(t *testing.T) {
	now := time.Date(2026, 5, 16, 14, 0, 0, 0, time.UTC)
	s, item := batchJournal(t, now, 12, 13)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}}
	c := batchController(s, forge, forge, func() time.Time { return now })
	active := authorizeBatch(t, c, item, "merge-active", "merge_and_complete", "squash", "", 12, 13)

	// A second confirmation while the batch is still active is refused, and
	// journals nothing.
	_, err := c.Command(context.Background(), item.ID, FactoryCommand{RequestID: "merge-second", Kind: "merge_pr", ExpectedVersion: active.Version, Completion: mergeCommand("merge_pr", "merge", "", 13)})
	if !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("concurrent confirmation: %v", err)
	}
	if saved := currentItem(t, s, item.ID); saved.Version != active.Version || saved.Completion.Batch.ID != active.Completion.Batch.ID {
		t.Fatalf("a refused confirmation wrote: %+v", saved.Completion.Batch)
	}

	// A confirmation the journal cannot bind is refused, and the journal keeps
	// exactly the batch it had. Each case starts from its own journal, because
	// a refusal must write nothing at all.
	for _, tc := range []struct {
		name    string
		kind    string
		command FactoryCompletionCommand
		version func(FactoryItem) uint64
		want    error
	}{
		{
			name:    "unlinked",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 99, NodeID: "node-99", HeadSHA: "head-99", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    ErrFactoryConflict,
		},
		{
			name:    "other repository",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "other", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    ErrFactoryConflict,
		},
		{
			name:    "changed head",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-other", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    ErrFactoryConflict,
		},
		{
			name:    "changed base",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "release"}}, Strategy: "squash", CompleteAfter: true},
			want:    ErrFactoryConflict,
		},
		{
			name:    "changed number",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 14, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    ErrFactoryConflict,
		},
		{
			name:    "stale version",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			version: func(i FactoryItem) uint64 { return i.Version - 1 },
			want:    ErrFactoryConflict,
		},
		{
			name:    "no pull requests",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{Strategy: "squash", CompleteAfter: true},
			want:    errFactoryInput,
		},
		{
			name: "duplicate pull request",
			kind: "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{
				{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"},
				{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"},
			}, Strategy: "squash", CompleteAfter: true},
			want: errFactoryInput,
		},
		{
			name:    "no number",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    errFactoryInput,
		},
		{
			name:    "no node",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, HeadSHA: "head-12", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    errFactoryInput,
		},
		{
			name:    "no head",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    errFactoryInput,
		},
		{
			name:    "no base",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12"}}, Strategy: "squash", CompleteAfter: true},
			want:    errFactoryInput,
		},
		{
			name:    "no strategy",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"}}, CompleteAfter: true},
			want:    errFactoryInput,
		},
		{
			name:    "unknown strategy",
			kind:    "merge_and_complete",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"}}, Strategy: "fast-forward", CompleteAfter: true},
			want:    errFactoryInput,
		},
		{
			name:    "contradicted intent",
			kind:    "merge_pr",
			command: FactoryCompletionCommand{PRs: []FactoryCompletionPR{{RepoID: "repo", Number: 12, NodeID: "node-12", HeadSHA: "head-12", BaseRef: "main"}}, Strategy: "squash", CompleteAfter: true},
			want:    errFactoryInput,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, item := batchJournal(t, now, 12, 13)
			c := batchController(s, forge, forge, func() time.Time { return now })
			version := item.Version
			if tc.version != nil {
				version = tc.version(item)
			}
			command := tc.command
			_, err := c.Command(context.Background(), item.ID, FactoryCommand{RequestID: "merge-" + tc.name, Kind: tc.kind, ExpectedVersion: version, Completion: &command})
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: %v", tc.name, err)
			}
			after := currentItem(t, s, item.ID)
			if after.Version != item.Version || after.Completion.Batch != nil {
				t.Fatalf("a refused confirmation wrote: %+v", after.Completion)
			}
		})
	}

	// Confirming reviewed merges is not an action an MCP client may take.
	_, err = c.CommandAuthorized(context.Background(), item.ID, FactoryCommand{RequestID: "mcp-merge", Kind: "merge_and_complete", ExpectedVersion: active.Version, Completion: mergeCommand("merge_and_complete", "squash", "", 12)}, factoryPrincipal{ClientID: "client", Action: "factory.merge_and_complete"})
	if !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an MCP client confirmed a merge batch: %v", err)
	}

	// An active run has to be decided, not assumed: a batch that does not say
	// what happens to the turn it runs beside is refused.
	runStore, runItem := batchJournal(t, now, 12)
	running, err := runStore.Update(runItem.ID, "running", runItem.Version, json.RawMessage(`{"running":true}`), func(dst *FactoryItem) error {
		dst.State = "implementing"
		dst.Workers = []FactoryWorker{{AgentID: "agent", SessionID: "session", Role: "implementer"}}
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	runForge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	runController := batchController(runStore, runForge, runForge, func() time.Time { return now })
	_, err = runController.Command(context.Background(), running.ID, FactoryCommand{RequestID: "merge-undecided", Kind: "merge_and_complete", ExpectedVersion: running.Version, Completion: mergeCommand("merge_and_complete", "squash", "", 12)})
	if !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a batch beside an active run decided nothing: %v", err)
	}
	// The decision the user makes is stored on the batch and carried into the
	// completion it authorizes.
	chosen := authorizeBatch(t, runController, running, "merge-decided", "merge_and_complete", "squash", "continue", 12)
	if chosen.Completion.Batch.RunChoice != "continue" {
		t.Fatalf("run choice: %+v", chosen.Completion.Batch)
	}
	advanceBatches(t, runController)
	done := currentItem(t, runStore, running.ID)
	if factoryCompletionStatus(done) != "complete" || len(done.Completion.Records) != 1 || done.Completion.Records[0].RunChoice != "continue" {
		t.Fatalf("completion after a decided run: %+v", done.Completion)
	}

	// A stopped batch is not a dead end: a new explicit decision with fresh
	// evidence replaces it.
	stopStore, stopItem := batchJournal(t, now, 12)
	stopForge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}, mergeErr: fmt.Errorf("%w: checks are not green", ErrFactoryMergeRejected)}
	stopController := batchController(stopStore, stopForge, stopForge, func() time.Time { return now })
	first := authorizeBatch(t, stopController, stopItem, "merge-first", "merge_and_complete", "squash", "", 12)
	advanceBatches(t, stopController)
	stopped := currentItem(t, stopStore, stopItem.ID)
	if stopped.Completion.Batch.State != "stopped" {
		t.Fatalf("batch: %+v", stopped.Completion.Batch)
	}
	stopForge.mergeErr = nil
	second := authorizeBatch(t, stopController, stopped, "merge-second-attempt", "merge_and_complete", "squash", "", 12)
	if second.Completion.Batch.ID == first.Completion.Batch.ID || second.Completion.Batch.State != "active" {
		t.Fatalf("a new confirmation reused the stopped batch: %+v", second.Completion.Batch)
	}
	advanceBatches(t, stopController)
	retried := currentItem(t, stopStore, stopItem.ID)
	if factoryCompletionStatus(retried) != "complete" || len(retried.Completion.Records) != 1 {
		t.Fatalf("retried completion: %+v", retried.Completion)
	}
	if len(stopForge.merges) != 2 {
		t.Fatalf("merges across the retry: %v", stopForge.merges)
	}
}

// TestCompletionBatchMergeOnly covers a merge-only batch: every reviewed merge
// runs in order and the item is never completed by it.
func TestCompletionBatchMergeOnly(t *testing.T) {
	now := time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	s, item := batchJournal(t, now, 12, 13)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}}
	c := batchController(s, forge, forge, func() time.Time { return now })
	confirmed := authorizeBatch(t, c, item, "merge-only", "merge_pr", "rebase", "", 12, 13)
	if confirmed.Completion.Batch.CompleteAfter {
		t.Fatalf("a merge-only batch completes after: %+v", confirmed.Completion.Batch)
	}

	advanceBatches(t, c)
	advanceBatches(t, c)
	merged := currentItem(t, s, item.ID)
	if len(forge.merges) != 2 || forge.merges[0].Strategy != "rebase" || forge.merges[1].Strategy != "rebase" {
		t.Fatalf("merges: %v", forge.merges)
	}
	if merged.Completion.Batch.State != "complete" {
		t.Fatalf("batch: %+v", merged.Completion.Batch)
	}
	for _, step := range merged.Completion.Batch.Steps {
		if step.State != "confirmed" {
			t.Fatalf("step: %+v", step)
		}
	}
	// Merging is not declaring the work done.
	if factoryCompletionStatus(merged) != "open" || len(merged.Completion.Records) != 0 {
		t.Fatalf("a merge-only batch completed the item: %+v", merged.Completion)
	}
	advanceBatches(t, c)
	if len(forge.merges) != 2 {
		t.Fatalf("a completed merge-only batch merged again: %v", forge.merges)
	}
}
