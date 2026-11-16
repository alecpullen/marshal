package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// runEffects is the controller's effects double for the completion's run
// choice: a pinned forge and observation, plus a Cancel that records every call
// it received, so a test can prove how many stops were asked for and for whom.
type runEffects struct {
	*loopEffects
	repo      Repo
	base      Forge
	factory   FactoryForge
	cancels   []string
	cancelErr error
}

func (e *runEffects) forge(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
	return e.repo, e.base, e.factory, Credential{}, nil
}

func (e *runEffects) Cancel(_ context.Context, item FactoryItem) error {
	e.cancels = append(e.cancels, item.ID)
	return e.cancelErr
}

// runController is a controller over s whose forge, observation and
// cancellations the test owns.
func runController(s *FactoryStore, forge *batchForge, observation FactoryObservation, clock func() time.Time) (*FactoryController, *runEffects) {
	effects := &runEffects{
		loopEffects: &loopEffects{store: s, observation: observation},
		repo:        linkRepo(),
		base:        forge,
		factory:     forge,
	}
	c := NewFactoryController(s, effects, clock)
	c.admitAfterRecovery()
	return c, effects
}

// runJournal opens a journal holding one item whose delivery pull request is
// linked and whose run is still active: an implementer that is not parked and
// an implemented effect in flight.
func runJournal(t *testing.T, now time.Time, number int) (*FactoryStore, FactoryItem) {
	t.Helper()
	s, item := batchJournal(t, now, number)
	delivery := reviewedPR(number)
	delivery.State = "open"
	saved, err := s.Update(item.ID, "run-active", item.Version, json.RawMessage(`{"run":true}`), func(dst *FactoryItem) error {
		dst.State = "implementing"
		dst.Branch = delivery.HeadRef
		dst.PRs = []FactoryPR{delivery}
		dst.Workers = []FactoryWorker{{AgentID: dst.ID + "-implementer", SessionID: "session", Role: "implementer"}}
		dst.Effects = []FactoryEffect{{ID: dst.ID + "-effect-1", Kind: "implement", Status: "dispatched"}}
		return nil
	}, now)
	if err != nil {
		t.Fatalf("run journal: %v", err)
	}
	return s, saved
}

// runOtherItem creates a second, unrelated item whose run is active, so a stop
// asked of one item can be proved not to reach another.
func runOtherItem(t *testing.T, s *FactoryStore, now time.Time) FactoryItem {
	t.Helper()
	req := factoryReq()
	req.RequestID = "other-create"
	req.Title = "unrelated"
	req.Config.TargetBranch = "main"
	created, err := s.Create(req, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Update(created.ID, "other-active", created.Version, json.RawMessage(`{"other":true}`), func(dst *FactoryItem) error {
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: now}}
		dst.State = "implementing"
		dst.Branch = "marshal/factory/other"
		dst.Workers = []FactoryWorker{{AgentID: "other-implementer", SessionID: "session", Role: "implementer"}}
		dst.Effects = []FactoryEffect{{ID: "other-effect-1", Kind: "implement", Status: "dispatched"}}
		return nil
	}, now)
	if err != nil {
		t.Fatalf("other item: %v", err)
	}
	return saved
}

// settleRunEffect journals the in-flight effect completing, which is the
// boundary a continued run waits for before it is asked what comes next.
func settleRunEffect(t *testing.T, s *FactoryStore, id string, now time.Time) {
	t.Helper()
	current := currentItem(t, s, id)
	_, err := s.Update(id, "run-effect-completed", current.Version, json.RawMessage(`{"effect":"completed"}`), func(dst *FactoryItem) error {
		for n := range dst.Effects {
			if dst.Effects[n].Status == "dispatched" || dst.Effects[n].Status == "uncertain" {
				dst.Effects[n].Status = "completed"
			}
		}
		return nil
	}, now)
	if err != nil {
		t.Fatalf("settle effect: %v", err)
	}
}

// preparedRun turns the fixture's in-flight effect into one that was prepared
// but never dispatched, with its worker already parked.
func preparedRun(t *testing.T, s *FactoryStore, item FactoryItem, now time.Time) FactoryItem {
	t.Helper()
	saved, err := s.Update(item.ID, "run-prepared", item.Version, json.RawMessage(`{"prepared":true}`), func(dst *FactoryItem) error {
		dst.Workers[0].Parked = true
		dst.Effects[0].Status = "prepared"
		return nil
	}, now)
	if err != nil {
		t.Fatalf("prepared run: %v", err)
	}
	return saved
}

// completedRun journals an item the user already called done, which is what a
// stop that cannot be delivered must leave exactly as it found it.
func completedRun(t *testing.T, s *FactoryStore, item FactoryItem, now time.Time) FactoryItem {
	t.Helper()
	saved, err := s.Update(item.ID, "run-completed", item.Version, json.RawMessage(`{"completed":true}`), func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		completion.Status = "complete"
		completion.Records = append(completion.Records, FactoryCompletionRecord{ID: "completion-" + dst.ID, At: now, Actor: factoryStudioRevisionActor, Goal: dst.Title})
		dst.Completion = &completion
		return nil
	}, now)
	if err != nil {
		t.Fatalf("completed run: %v", err)
	}
	return saved
}

// runHistory reports whether the item journalled an event of this kind.
func runHistory(i FactoryItem, kind string) bool {
	for _, e := range i.History {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// runTick is one pass of the controller's tick.
func runTick(t *testing.T, c *FactoryController) {
	t.Helper()
	if err := c.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// TestCompletionContinueRun covers the run the user explicitly continued: a
// delivery pull request the completion merged does not close it. The effect in
// flight settles first, the checkout and its outputs stay, nothing is
// cancelled, and only then is the run asked what it does next — which is what
// blocks any new dispatch or publish against the merged pull request.
func TestCompletionContinueRun(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	s, item := runJournal(t, now, 12)
	item = seedBatch(t, s, item, FactoryMergeBatch{
		ID: "batch-run", RequestID: "merge-run", State: "complete", CompleteAfter: true, RunChoice: "continue",
		Steps: []FactoryMergeStep{seedStep(12, "confirmed", "merged")},
	})
	merged := reviewedPR(12)
	merged.State = "merged"
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	c, effects := runController(s, forge, FactoryObservation{
		RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, ObservedAt: now, PR: &merged,
	}, func() time.Time { return now })

	runTick(t, c)
	inFlight := currentItem(t, s, item.ID)
	if inFlight.State != "implementing" || inFlight.Decision != nil {
		t.Fatalf("continued closure moved the run: %s %+v", inFlight.State, inFlight.Decision)
	}
	if len(inFlight.Workers) != 1 || inFlight.Workers[0].Parked || len(inFlight.Effects) != 1 || inFlight.Effects[0].Status != "dispatched" {
		t.Fatalf("continued closure touched the checkout: %+v %+v", inFlight.Workers, inFlight.Effects)
	}
	if len(effects.cancels) != 0 {
		t.Fatalf("continued run cancelled: %v", effects.cancels)
	}
	if !runHistory(inFlight, "delivery_merge_continued") || runHistory(inFlight, "pr_closed") {
		t.Fatalf("continued closure history: %+v", inFlight.History)
	}

	settleRunEffect(t, s, item.ID, now)
	runTick(t, c)
	decided := currentItem(t, s, item.ID)
	if decided.State != "needs_input" || decided.Decision == nil || decided.Decision.Kind != "delivery_decision" {
		t.Fatalf("continued run decision: %s %+v", decided.State, decided.Decision)
	}
	if len(decided.Workers) != 1 || decided.Workers[0].Parked || len(decided.Effects) != 1 || decided.Effects[0].Status != "completed" {
		t.Fatalf("continued run lost its checkout: %+v %+v", decided.Workers, decided.Effects)
	}
	if len(effects.cancels) != 0 {
		t.Fatalf("continued run cancelled after settling: %v", effects.cancels)
	}
	if len(decided.PRs) != 1 || decided.PRs[0].State != "merged" || decided.Completion == nil || decided.Completion.Batch == nil || decided.Completion.Batch.RunChoice != "continue" {
		t.Fatalf("continued run altered delivery: %+v", decided)
	}
}

// TestCompletionContinueAfterDeliveryMerge drives the same choice through the
// real batch: the completion merges the delivery pull request, and the run the
// user kept is asked what comes next only once the in-flight effect settled.
func TestCompletionContinueAfterDeliveryMerge(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	s, item := runJournal(t, now, 12)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	c, effects := runController(s, forge, FactoryObservation{ObservedAt: now}, func() time.Time { return now })

	authorized := authorizeBatch(t, c, item, "merge-continue", "merge_and_complete", "squash", "continue", 12)
	if authorized.Completion.Batch == nil || authorized.Completion.Batch.RunChoice != "continue" {
		t.Fatalf("continue choice: %+v", authorized.Completion)
	}
	advanceBatches(t, c)
	merged := forge.prs[12]
	if len(forge.merges) != 1 || merged.State != "merged" {
		t.Fatalf("completion did not merge the delivery pull request: %+v %+v", forge.merges, merged)
	}
	done := currentItem(t, s, item.ID)
	if factoryCompletionStatus(done) != "complete" || len(done.Completion.Records) != 1 || done.State != "implementing" {
		t.Fatalf("completion after merge: %s %+v", done.State, done.Completion)
	}

	effects.observation = FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, ObservedAt: now, PR: &merged}
	runTick(t, c)
	inFlight := currentItem(t, s, item.ID)
	if inFlight.Decision != nil || inFlight.State != "implementing" || inFlight.Effects[0].Status != "dispatched" {
		t.Fatalf("continued closure did not wait for the effect: %s %+v", inFlight.State, inFlight.Decision)
	}
	if len(effects.cancels) != 0 || len(forge.merges) != 1 {
		t.Fatalf("continued run reached the forge or the cancellation path: %v %+v", effects.cancels, forge.merges)
	}

	settleRunEffect(t, s, item.ID, now)
	runTick(t, c)
	decided := currentItem(t, s, item.ID)
	if decided.Decision == nil || decided.Decision.Kind != "delivery_decision" || decided.State != "needs_input" {
		t.Fatalf("continued run decision: %s %+v", decided.State, decided.Decision)
	}
	if len(decided.Workers) != 1 || decided.Workers[0].Parked || len(decided.PRs) != 1 || len(forge.merges) != 1 {
		t.Fatalf("continued run lost its checkout or opened a replacement: %+v %+v", decided, forge.merges)
	}
	if factoryCompletionStatus(decided) != "complete" || len(decided.Completion.Records) != 1 || len(effects.cancels) != 0 {
		t.Fatalf("continued run altered completion: %+v %v", decided.Completion, effects.cancels)
	}
}

// TestCompletionStopObserved covers a run the user asked to stop: the intent is
// journalled and delivered once, a restart while stopping settles it from the
// journal without a second cancellation, and another item's run is untouched.
func TestCompletionStopObserved(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	s, item := runJournal(t, now, 12)
	other := runOtherItem(t, s, now)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	c, effects := runController(s, forge, FactoryObservation{ObservedAt: now}, func() time.Time { return now })

	authorizeBatch(t, c, item, "merge-stop", "merge_and_complete", "squash", "request_stop", 12)
	if len(effects.cancels) != 1 || effects.cancels[0] != item.ID {
		t.Fatalf("stop cancels: %v", effects.cancels)
	}
	requested := currentItem(t, s, item.ID)
	if requested.Completion == nil || requested.Completion.StopState != "requested" {
		t.Fatalf("stop intent: %+v", requested.Completion)
	}

	// A restart while stopping: the fresh controller settles the intent it finds
	// and never asks a second time.
	restarted, restartedEffects := runController(s, forge, FactoryObservation{
		RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, ObservedAt: now,
	}, func() time.Time { return now })
	runTick(t, restarted)
	stopped := currentItem(t, s, item.ID)
	if stopped.Completion.StopState != "stopped" {
		t.Fatalf("observed stop: %+v", stopped.Completion)
	}
	if len(restartedEffects.cancels) != 0 {
		t.Fatalf("restart cancelled again: %v", restartedEffects.cancels)
	}
	if !runHistory(stopped, "completion_stop_requested") || !runHistory(stopped, "completion_stop_stopped") {
		t.Fatalf("stop history: %+v", stopped.History)
	}
	if len(stopped.Workers) != 1 || stopped.Workers[0].Parked || len(stopped.Effects) != 1 {
		t.Fatalf("stop rewrote the run's ownership: %+v %+v", stopped.Workers, stopped.Effects)
	}

	// A settled stop neither replays nor is rewritten by a later tick.
	runTick(t, restarted)
	if again := currentItem(t, s, item.ID); again.Completion.StopState != "stopped" || len(restartedEffects.cancels) != 0 {
		t.Fatalf("stop replayed: %+v %v", again.Completion, restartedEffects.cancels)
	}
	untouched := currentItem(t, s, other.ID)
	if untouched.State != other.State || len(untouched.Workers) != 1 || len(untouched.Effects) != 1 || untouched.Effects[0].Status != "dispatched" || untouched.Completion != nil {
		t.Fatalf("unrelated item touched by another item's stop: %+v", untouched)
	}
	for _, id := range restartedEffects.cancels {
		if id == other.ID {
			t.Fatalf("stop reached the unrelated item: %v", restartedEffects.cancels)
		}
	}
}

// TestCompletionStopFailure covers a cancellation that cannot be delivered: the
// failure is journaled, never rewritten by a later observation, and the
// completion the user asked for is retained.
func TestCompletionStopFailure(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	s, item := runJournal(t, now, 12)
	item = completedRun(t, s, item, now)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	c, effects := runController(s, forge, FactoryObservation{ObservedAt: now}, func() time.Time { return now })
	effects.cancelErr = errors.New("the run's runtime would not stop")

	authorizeBatch(t, c, item, "merge-stop-failed", "merge_and_complete", "squash", "request_stop", 12)
	failed := currentItem(t, s, item.ID)
	if failed.Completion.StopState != "failed" {
		t.Fatalf("failed stop: %+v", failed.Completion)
	}
	if factoryCompletionStatus(failed) != "complete" || len(failed.Completion.Records) != 1 {
		t.Fatalf("failed stop lost the completion: %+v", failed.Completion)
	}
	if !runHistory(failed, "completion_stop_requested") || !runHistory(failed, "completion_stop_failed") {
		t.Fatalf("failed stop history: %+v", failed.History)
	}
	if len(effects.cancels) != 1 || effects.cancels[0] != item.ID {
		t.Fatalf("failed stop cancels: %v", effects.cancels)
	}

	// A journaled failure stays failed, and nothing asks the run again.
	effects.observation = FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, ObservedAt: now}
	runTick(t, c)
	if again := currentItem(t, s, item.ID); again.Completion.StopState != "failed" || len(effects.cancels) != 1 {
		t.Fatalf("failure rewritten: %+v %v", again.Completion, effects.cancels)
	}
}

// TestCompletionExternalClosureUnchanged proves the ordinary closure path is
// untouched: a delivery pull request closed or merged outside the completion —
// or a batch that merged a different pull request — still closes the item,
// completes its prepared work, and cancels its run exactly once.
func TestCompletionExternalClosureUnchanged(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name  string
		batch *FactoryMergeBatch
	}{
		{name: "no completion batch"},
		{name: "batch merged another pull request", batch: &FactoryMergeBatch{
			ID: "batch-external", RequestID: "merge-external", State: "complete", CompleteAfter: true, RunChoice: "continue",
			Steps: []FactoryMergeStep{seedStep(99, "confirmed", "merged")},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, item := runJournal(t, now, 12)
			if tc.batch != nil {
				item = seedBatch(t, s, item, *tc.batch)
			}
			merged := reviewedPR(12)
			merged.State = "merged"
			forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
			c, effects := runController(s, forge, FactoryObservation{
				RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, ObservedAt: now, PR: &merged,
			}, func() time.Time { return now })

			runTick(t, c)
			closed := currentItem(t, s, item.ID)
			if closed.State != "stopping" || closed.Decision != nil {
				t.Fatalf("external closure: %s %+v", closed.State, closed.Decision)
			}
			if !runHistory(closed, "pr_closed") || runHistory(closed, "delivery_merge_continued") {
				t.Fatalf("external closure history: %+v", closed.History)
			}
			if len(closed.Effects) != 1 || closed.Effects[0].Status != "dispatched" {
				t.Fatalf("external closure completed an in-flight effect: %+v", closed.Effects)
			}
			if len(effects.cancels) != 1 || effects.cancels[0] != item.ID {
				t.Fatalf("external closure cancels: %v", effects.cancels)
			}
		})
	}

	t.Run("prepared effect", func(t *testing.T) {
		s, item := runJournal(t, now, 12)
		item = preparedRun(t, s, item, now)
		merged := reviewedPR(12)
		merged.State = "merged"
		forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
		c, effects := runController(s, forge, FactoryObservation{
			RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true, ObservedAt: now, PR: &merged,
		}, func() time.Time { return now })

		runTick(t, c)
		closed := currentItem(t, s, item.ID)
		if closed.State != "closed" || closed.Decision != nil {
			t.Fatalf("external closure: %s %+v", closed.State, closed.Decision)
		}
		if len(closed.Effects) != 1 || closed.Effects[0].Status != "completed" || closed.Effects[0].Outcome == nil || closed.Effects[0].Outcome.Error != "PR closed before dispatch" {
			t.Fatalf("prepared effect not completed by closure: %+v", closed.Effects)
		}
		if len(effects.cancels) != 0 {
			t.Fatalf("closed item cancelled: %v", effects.cancels)
		}
	})
}
