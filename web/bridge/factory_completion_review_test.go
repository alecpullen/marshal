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

// itemForgeEffects is a controller effects double whose forge opener fails for
// one named item, standing for a revoked credential, an unregistered
// repository, or a fleet that cannot resolve the project. Every other item
// reaches the pinned forge, so one item's broken configuration can be proved
// not to hold another's batch back.
type itemForgeEffects struct {
	*loopEffects
	repo    Repo
	base    Forge
	factory FactoryForge
	failFor string
	err     error
}

func (e *itemForgeEffects) forge(_ context.Context, item FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
	if item.ID == e.failFor {
		return Repo{}, nil, nil, Credential{}, e.err
	}
	return e.repo, e.base, e.factory, Credential{}, nil
}

// secondBatchItem adds an approved item with one linked pull request to an
// existing journal, so a test can watch two items' batches side by side.
func secondBatchItem(t *testing.T, s *FactoryStore, now time.Time, number int) FactoryItem {
	t.Helper()
	req := factoryReq()
	req.RequestID = "create-second"
	req.Title = "second"
	req.Config.TargetBranch = "main"
	created, err := s.Create(req, now)
	if err != nil {
		t.Fatalf("second item: %v", err)
	}
	saved, err := s.Update(created.ID, "second-approve", created.Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		dst.Approvals = []FactoryApproval{{Revision: 1, Plan: req.Plan, Spec: req.Spec, PlanHash: FactoryPlanHash(req.Plan, req.Spec), Config: req.Config, ApprovedAt: now}}
		dst.State = "checking"
		dst.BaseSHA = "base"
		completion := factoryCompletionOf(*dst)
		completion.Links = append(completion.Links, FactoryPRLink{RepoID: "repo", PR: reviewedPR(number), Origin: "user", ObservedAt: now, Fresh: true})
		dst.Completion = &completion
		return nil
	}, now)
	if err != nil {
		t.Fatalf("second approval: %v", err)
	}
	return saved
}

// setStopState writes a stop outcome straight into the journal, which is how a
// failed cancellation or an unconfirmed stop is left behind.
func setStopState(t *testing.T, s *FactoryStore, id, state string, now time.Time) FactoryItem {
	t.Helper()
	current := currentItem(t, s, id)
	saved, err := s.Update(id, "set-stop-"+state, current.Version, json.RawMessage(`{"stop":"`+state+`"}`), func(dst *FactoryItem) error {
		completion := factoryCompletionOf(*dst)
		completion.StopState = state
		dst.Completion = &completion
		return nil
	}, now)
	if err != nil {
		t.Fatalf("set stop state %s: %v", state, err)
	}
	return saved
}

// A merge batch whose forge cannot be opened stops with the reason instead of
// waiting silently. The entry was never dispatched, so it failed rather than
// became uncertain, and no merge is sent.
func TestCompletionBatchUnavailableForgeStops(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, item := batchJournal(t, now, 12)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	effects := &itemForgeEffects{
		loopEffects: &loopEffects{store: s},
		repo:        linkRepo(),
		base:        forge,
		factory:     forge,
		failFor:     item.ID,
		err:         fmt.Errorf("%w: credential unavailable", ErrFactoryRefused),
	}
	c := NewFactoryController(s, effects, func() time.Time { return now })
	c.admitAfterRecovery()

	item = authorizeBatch(t, c, item, "batch-1", "merge_pr", "merge", "", 12)
	if err := c.advanceCompletionBatches(context.Background()); err != nil {
		t.Fatal(err)
	}

	b := currentItem(t, s, item.ID).Completion.Batch
	if b == nil || b.State != "stopped" {
		t.Fatalf("batch after an unopenable forge: %+v", b)
	}
	if b.Steps[0].State != "failed" {
		t.Fatalf("step after an unopenable forge: %+v", b.Steps[0])
	}
	if !strings.Contains(b.Steps[0].Result, "credential unavailable") {
		t.Fatalf("the reason is not the forge's: %q", b.Steps[0].Result)
	}
	if len(forge.merges) != 0 {
		t.Fatalf("a merge was sent for an item whose forge would not open: %+v", forge.merges)
	}
}

// One item whose forge will not open must not spend every pass: a batch that
// cannot make progress sends nothing, so the next item's batch still goes out.
func TestCompletionBatchUnavailableForgeDoesNotBlockOtherItems(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, first := batchJournal(t, now, 12)
	second := secondBatchItem(t, s, now, 13)

	// The store lists items in ID order, and the pass walks that order: pin the
	// failure to whichever item is listed first, so a pass that stopped at it
	// would never reach the other one.
	failingID, healthyID := first.ID, second.ID
	failingNumber, healthyNumber := 12, 13
	if second.ID < first.ID {
		failingID, healthyID = second.ID, first.ID
		failingNumber, healthyNumber = 13, 12
	}

	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12), 13: reviewedPR(13)}}
	effects := &itemForgeEffects{
		loopEffects: &loopEffects{store: s},
		repo:        linkRepo(),
		base:        forge,
		factory:     forge,
		failFor:     failingID,
		err:         fmt.Errorf("%w: %w", ErrFactoryRefused, ErrUnknownRepo),
	}
	c := NewFactoryController(s, effects, func() time.Time { return now })
	c.admitAfterRecovery()

	authorizeBatch(t, c, currentItem(t, s, failingID), "batch-failing", "merge_pr", "merge", "", failingNumber)
	authorizeBatch(t, c, currentItem(t, s, healthyID), "batch-healthy", "merge_pr", "merge", "", healthyNumber)

	for pass := 0; pass < 4; pass++ {
		if err := c.advanceCompletionBatches(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	blocked := currentItem(t, s, failingID).Completion.Batch
	if blocked == nil || blocked.State != "stopped" || blocked.Steps[0].State != "failed" {
		t.Fatalf("the unopenable item's batch: %+v", blocked)
	}
	advanced := currentItem(t, s, healthyID).Completion.Batch
	if advanced == nil || advanced.Steps[0].State != "confirmed" {
		t.Fatalf("the other item's batch never advanced: %+v", advanced)
	}
	if len(forge.merges) != 1 || forge.merges[0].Number != healthyNumber {
		t.Fatalf("merges sent: %+v", forge.merges)
	}
}

// A stop that failed is a new attempt, not a settled decision: the run choice
// recorded on the batch has to reach the run it names. A stop already in
// flight is still the user's own decision and is not delivered twice.
func TestCompletionStopIsRedeliveredAfterFailure(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, item := runJournal(t, now, 12)
	forge := &batchForge{prs: map[int]FactoryPR{12: reviewedPR(12)}}
	c, effects := runController(s, forge, FactoryObservation{}, func() time.Time { return now })

	item = setStopState(t, s, item.ID, "failed", now)
	if err := c.requestCompletionStop(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if len(effects.cancels) != 1 || effects.cancels[0] != item.ID {
		t.Fatalf("a failed stop was not re-delivered: %v", effects.cancels)
	}
	if got := currentItem(t, s, item.ID).Completion.StopState; got != "requested" {
		t.Fatalf("stop state after re-requesting: %q", got)
	}

	// Asking again while the stop is in flight cancels nothing further.
	item = currentItem(t, s, item.ID)
	if err := c.requestCompletionStop(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if len(effects.cancels) != 1 {
		t.Fatalf("an in-flight stop was delivered twice: %v", effects.cancels)
	}
}

// A journal that records nothing of the run running is stopped even when the
// runtime cannot be asked; a run it still shows live needs that observation.
func TestCompletionStopSettlesWhenTheJournalShowsNothingLive(t *testing.T) {
	idle := FactoryItem{State: "checking", Workers: []FactoryWorker{{Parked: true}}}
	if !factoryCompletionStopSettled(idle, FactoryObservation{}, errors.New("no runtime")) {
		t.Fatal("an idle journal stayed requested for ever")
	}

	live := FactoryItem{State: "implementing", Workers: []FactoryWorker{{}}, Effects: []FactoryEffect{{ID: "e1", Status: "dispatched"}}}
	if factoryCompletionStopSettled(live, FactoryObservation{}, errors.New("no runtime")) {
		t.Fatal("a dispatched effect was declared stopped without observation")
	}
	idleObservation := FactoryObservation{RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}
	if !factoryCompletionStopSettled(live, idleObservation, nil) {
		t.Fatal("an idle runtime did not settle the stop")
	}
}

// A forge that could not answer is a configuration problem, not invalid input;
// a forge that rejected the request is the user naming something unlinkable.
func TestCompletionEvidenceErrorClass(t *testing.T) {
	if err := factoryEvidenceError("12", &forgeAPIError{statusCode: 502}); !errors.Is(err, errFactoryConfig) {
		t.Fatalf("a forge outage was not a config problem: %v", err)
	}
	if err := factoryEvidenceError("12", errors.New("connection refused")); !errors.Is(err, errFactoryConfig) {
		t.Fatalf("a transport failure was not a config problem: %v", err)
	}
	if err := factoryEvidenceError("12", &forgeAPIError{statusCode: 404}); !errors.Is(err, errFactoryInput) {
		t.Fatalf("a rejected observation was not input: %v", err)
	}
}

// A link is only usable while it carries the identity a refresh names, and a
// batch is the reviewed set: one entry per pull request.
func TestCompletionLinksValidateTheirIdentity(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	ok := FactoryPRLink{RepoID: "repo", PR: FactoryPR{Number: 12, NodeID: "node-12"}, Origin: "user", ObservedAt: now}
	if err := validateFactoryPRLinks([]FactoryPRLink{ok}); err != nil {
		t.Fatalf("a well-formed link was refused: %v", err)
	}

	noNode := ok
	noNode.PR.NodeID = ""
	if err := validateFactoryPRLinks([]FactoryPRLink{noNode}); err == nil {
		t.Fatal("a link without its immutable identity was accepted")
	}
	if err := validateFactoryPRLinks([]FactoryPRLink{ok, ok}); err == nil {
		t.Fatal("a duplicate link identity was accepted")
	}
	noTime := ok
	noTime.ObservedAt = time.Time{}
	if err := validateFactoryPRLinks([]FactoryPRLink{noTime}); err == nil {
		t.Fatal("a link observed at the zero time was accepted")
	}
}

// Observing the delivery pull request again carries the observation that
// produced it, so a link a failed refresh left stale is fresh again once the
// factory reads it.
func TestCompletionDeliveryObservationRefreshesStaleEvidence(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	_, item := runJournal(t, now, 12)

	completion := factoryCompletionOf(item)
	if len(completion.Links) != 1 {
		t.Fatalf("links: %+v", completion.Links)
	}
	completion.Links[0].Fresh = false
	item.Completion = &completion

	moved := reviewedPR(12)
	moved.HeadSHA = "head-12-moved"
	factoryAssociateDelivery(&item, FactoryObservation{PR: &moved}, now.Add(time.Minute))

	link := item.Completion.Links[0]
	if !link.Fresh {
		t.Fatalf("a freshly observed delivery pull request stayed stale: %+v", link)
	}
	if link.PR.HeadSHA != "head-12-moved" || !link.ObservedAt.After(now) {
		t.Fatalf("the observation did not move with the snapshot: %+v", link)
	}
}
