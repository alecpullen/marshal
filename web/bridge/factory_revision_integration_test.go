package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests prove the plan-revision lifecycle end to end on the same fixture
// the factory delivery tests use: a disposable bare Git remote, a scripted
// forge, real worker sessions on a scripted child transport and the real
// journal (web/bridge/factory_integration_test.go). The planner, the steering
// runtime and the pause settings are the fixtures the assessment and delivery
// slices already own (fakeRevisionPlanner, revisionRuntimeFake,
// revisionDelegation), so nothing here opens a provider endpoint, a real
// keychain or a live daemon.
//
// What is real here: the journal, the scheduling derivation, the Git remote, the
// worker checkouts, and the publish/CI/review chain. What is scripted: the
// planner's judgement, the steering runtime's answers and receipts, and the
// forge. A live planner and a live daemon are untested; the record in
// docs/studio-fleet-factory/factory-plan-revisions-design.md says so plainly.

// The candidate bytes these tests propose. Each phrase is distinctive, so an
// assertion can tell which revision a prompt or an approval carries.
const (
	revisionCandidatePlan = "revised plan\n"
	revisionCandidateSpec = "revised spec"
	revisionAmendedPlan   = "amended plan\n"
	revisionAmendedSpec   = "amended spec"
	revisionFollowUpPlan  = "follow-up plan\n"
	revisionFollowUpSpec  = "follow-up spec"
	// The legacy fixture approves its own plan, which is what an item journalled
	// before proposals existed holds.
	legacyApprovedPlan = "legacy approved plan\n"
	legacyApprovedSpec = "legacy approved spec"
)

// revisionSteerWait bounds the wait for a held turn to be admitted, so a
// scheduler that never dispatches fails the test instead of hanging it.
const revisionSteerWait = 30 * time.Second

// revisionEndToEndAdapter is the effects adapter these tests install: the
// delivery fixture's real adapter for spawn/park/seed/publish/CI, an optional
// hold in front of an effect so a turn can be observed while it runs, and the
// plan-revision runtime seam over the delivery slice's scripted runtime fake.
type revisionEndToEndAdapter struct {
	h    *deliveryHarness
	seam *revisionRuntimeFake
	hold func(FactoryEffect)
}

func (a *revisionEndToEndAdapter) Cancel(ctx context.Context, i FactoryItem) error {
	return deliveryAdapter{a.h}.Cancel(ctx, i)
}

func (a *revisionEndToEndAdapter) Observe(ctx context.Context, i FactoryItem) (FactoryObservation, error) {
	return deliveryAdapter{a.h}.Observe(ctx, i)
}

func (a *revisionEndToEndAdapter) Execute(ctx context.Context, i FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	if a.hold != nil {
		a.hold(e)
	}
	return deliveryAdapter{a.h}.Execute(ctx, i, e)
}

// RevisionSession resolves the session the item is actually running, so the
// journal records the session the harness spawned rather than a fixture
// constant.
func (a *revisionEndToEndAdapter) RevisionSession(_ context.Context, i FactoryItem) (string, error) {
	for n := len(i.Workers) - 1; n >= 0; n-- {
		if w := i.Workers[n]; !w.Parked && w.SessionID != "" {
			return w.SessionID, nil
		}
	}
	return "", errors.New("revision fixture: the item runs no live worker session")
}

func (a *revisionEndToEndAdapter) RevisionSteeringStatus(ctx context.Context, i FactoryItem) (RevisionSteeringState, error) {
	return a.seam.RevisionSteeringStatus(ctx, i)
}

func (a *revisionEndToEndAdapter) RevisionSendNow(ctx context.Context, i FactoryItem, turnID, requestID, text string) (RevisionSteeringReceipt, error) {
	return a.seam.RevisionSendNow(ctx, i, turnID, requestID, text)
}

func (a *revisionEndToEndAdapter) RevisionPause(ctx context.Context, i FactoryItem) error {
	return a.seam.RevisionPause(ctx, i)
}

func (a *revisionEndToEndAdapter) RevisionReceipt(i FactoryItem, requestID string) (RevisionSteeringReceipt, bool) {
	return a.seam.RevisionReceipt(i, requestID)
}

// revisionTurnHold keeps one effect's execution parked until the test releases
// it, which is how "the old turn is still running" is observed instead of
// raced.
type revisionTurnHold struct {
	mu      sync.Mutex
	kind    string
	armed   bool
	started chan struct{}
	release chan struct{}
}

func newRevisionTurnHold(kind string) *revisionTurnHold {
	return &revisionTurnHold{kind: kind, armed: true, started: make(chan struct{}), release: make(chan struct{})}
}

func (h *revisionTurnHold) enter(e FactoryEffect) {
	h.mu.Lock()
	hold := h.armed && e.Kind == h.kind
	if hold {
		h.armed = false
	}
	h.mu.Unlock()
	if !hold {
		return
	}
	close(h.started)
	<-h.release
}

func (h *revisionTurnHold) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-h.started:
	case <-time.After(revisionSteerWait):
		t.Fatal("the fixture never dispatched the turn this test holds")
	}
}

func (h *revisionTurnHold) finish() { close(h.release) }

// revisionEndToEndHarness is the delivery fixture with the plan-revision seams
// installed on a fresh controller: the journal the harness wrote is shared, and
// the planner and steering runtime are the revision slices' own fakes.
type revisionEndToEndHarness struct {
	*deliveryHarness
	seam    *revisionRuntimeFake
	planner *fakeRevisionPlanner
	adapter *revisionEndToEndAdapter
}

func newRevisionEndToEndHarness(t *testing.T) *revisionEndToEndHarness {
	t.Helper()
	h := &revisionEndToEndHarness{deliveryHarness: newDeliveryHarness(t), seam: newRevisionRuntimeFake()}
	h.adapter = &revisionEndToEndAdapter{h: h.deliveryHarness, seam: h.seam}
	h.controller = NewFactoryController(h.store, h.adapter, time.Now)
	h.controller.admitAfterRecovery()
	h.planner = &fakeRevisionPlanner{available: true, store: h.store, itemID: h.id}
	h.planner.assess = func(req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		return judgementFor(req, "routine_clarification", "amend", "interrupt"), nil
	}
	h.controller.setRevisionPlanner(h.planner)
	return h
}

// item reads the journal as it stands now.
func (h *revisionEndToEndHarness) item(t *testing.T) FactoryItem {
	t.Helper()
	i, err := h.store.Get(h.id)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// pass is one controller run iteration: the assessment intent is served before
// scheduling, exactly as Run orders it.
func (h *revisionEndToEndHarness) pass(ctx context.Context, t *testing.T) FactoryItem {
	t.Helper()
	if err := h.controller.assessPending(ctx); err != nil {
		t.Fatalf("assessment pass: %v", err)
	}
	return h.tick()
}

// drive runs passes until stop holds, waiting for an asynchronously dispatched
// turn to settle between passes so the effect a pass observes is completed.
func (h *revisionEndToEndHarness) drive(ctx context.Context, t *testing.T, passes int, stop func(FactoryItem) bool) FactoryItem {
	t.Helper()
	var i FactoryItem
	for n := 0; n < passes; n++ {
		i = h.pass(ctx, t)
		if stop != nil && stop(i) {
			return i
		}
		if e := factoryLastEffect(i); e != nil && e.Status == "dispatched" {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return i
}

// studio runs one Studio command from a fresh read of the item and retries the
// compare-and-swap the journal enforces: a worker turn completing between the
// read and the append is an ordinary conflict, not a test failure.
func (h *revisionEndToEndHarness) studio(ctx context.Context, t *testing.T, build func(FactoryItem) FactoryCommand) FactoryItem {
	t.Helper()
	kind := ""
	for n := 0; n < 6; n++ {
		current := h.item(t)
		cmd := build(current)
		kind = cmd.Kind
		cmd.ExpectedVersion = current.Version
		saved, err := h.controller.Command(ctx, current.ID, cmd)
		if err == nil {
			return saved
		}
		if !errors.Is(err, ErrFactoryConflict) {
			t.Fatalf("%s: %v", cmd.Kind, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: the journal kept moving", kind)
	return FactoryItem{}
}

// proposalAt finds one proposal on an item, or fails.
func proposalAt(t *testing.T, i FactoryItem, id string) FactoryRevisionProposal {
	t.Helper()
	n, ok := factoryRevisionProposalByID(i, id)
	if !ok {
		t.Fatalf("no proposal %q on %d proposals", id, len(i.Proposals))
	}
	return i.Proposals[n]
}

// latestProposal is the newest proposal the journal holds.
func latestProposal(t *testing.T, i FactoryItem) FactoryRevisionProposal {
	t.Helper()
	if len(i.Proposals) == 0 {
		t.Fatal("the item carries no proposal")
	}
	return i.Proposals[len(i.Proposals)-1]
}

// propose saves one candidate against the revision that is active now.
func (h *revisionEndToEndHarness) propose(ctx context.Context, t *testing.T, requestID, plan, spec string) FactoryItem {
	t.Helper()
	return h.studio(ctx, t, func(i FactoryItem) FactoryCommand {
		active := i.Approvals[len(i.Approvals)-1]
		return FactoryCommand{
			RequestID: requestID, Kind: "revision_propose",
			Revision: &FactoryRevisionCommand{
				BaseRevision: active.Revision, BaseHash: active.PlanHash,
				Plan: plan, Spec: spec, CandidateHash: FactoryPlanHash(plan, spec),
			},
		}
	})
}

// authorize is the Studio command: the controller's own path, with no client
// principal, which is the only identity a plan revision is authorized under.
func (h *revisionEndToEndHarness) authorize(ctx context.Context, t *testing.T, proposalID, routing, strategy string) FactoryItem {
	t.Helper()
	return h.studio(ctx, t, func(i FactoryItem) FactoryCommand {
		p := proposalAt(t, i, proposalID)
		return FactoryCommand{
			RequestID: "authorize-" + p.ID, Kind: "revision_authorize",
			Revision: &FactoryRevisionCommand{
				ProposalID: p.ID, ProposalVersion: p.Version,
				BaseRevision: p.BaseRevision, BaseHash: p.BaseHash, CandidateHash: p.CandidateHash,
				Routing: routing, Strategy: strategy,
			},
		}
	})
}

// choose records the delivery strategy a Studio user picked.
func (h *revisionEndToEndHarness) choose(ctx context.Context, t *testing.T, proposalID, strategy string) FactoryItem {
	t.Helper()
	return h.studio(ctx, t, func(i FactoryItem) FactoryCommand {
		p := proposalAt(t, i, proposalID)
		return FactoryCommand{
			RequestID: "choose-" + p.ID, Kind: "revision_delivery_choice",
			Revision: &FactoryRevisionCommand{ProposalID: p.ID, ProposalVersion: p.Version, CandidateHash: p.CandidateHash, Strategy: strategy},
		}
	})
}

// workerTransport is the scripted child transport the item's worker session
// runs on.
func (h *revisionEndToEndHarness) workerTransport(t *testing.T, i FactoryItem) *captureTransport {
	t.Helper()
	for n := len(i.Workers) - 1; n >= 0; n-- {
		rt, err := h.fleet.runtimeForAgent(i.Workers[n].AgentID)
		if err != nil || rt == nil || rt.child == nil {
			continue
		}
		if tr, ok := rt.child.Transport.(*captureTransport); ok {
			return tr
		}
	}
	t.Fatal("the item's worker has no scripted transport")
	return nil
}

// workerFrames reads the frames the worker's child actually received.
func (h *revisionEndToEndHarness) workerFrames(t *testing.T, i FactoryItem) []capturedFrame {
	t.Helper()
	tr := h.workerTransport(t, i)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]capturedFrame(nil), tr.seen...)
}

// workerRegistry is the live registry the item's worker session runs on.
func (h *revisionEndToEndHarness) workerRegistry(t *testing.T, i FactoryItem) (*Registry, string) {
	t.Helper()
	for n := len(i.Workers) - 1; n >= 0; n-- {
		w := i.Workers[n]
		if w.Parked || w.SessionID == "" {
			continue
		}
		rt, err := h.fleet.runtimeForAgent(w.AgentID)
		if err != nil || rt == nil || rt.reg == nil {
			continue
		}
		if _, ok := rt.reg.lookup(w.SessionID); !ok {
			continue
		}
		return rt.reg, w.SessionID
	}
	t.Fatal("the item runs no live worker registry")
	return nil, ""
}

// restart swaps in a fresh controller over the same journal, which is what a
// bridge restart is: the seams are re-installed and nothing else survives.
func (h *revisionEndToEndHarness) restart(t *testing.T) *FactoryController {
	t.Helper()
	ctl := NewFactoryController(h.store, h.adapter, time.Now)
	ctl.setRevisionPlanner(h.planner)
	ctl.admitAfterRecovery()
	h.controller = ctl
	return ctl
}

// revisionPass is one controller iteration for a controller a test built
// itself, as a restarted bridge would run.
func revisionPass(t *testing.T, ctl *FactoryController) error {
	t.Helper()
	if err := ctl.assessPending(context.Background()); err != nil {
		return err
	}
	return ctl.tick(context.Background())
}

// revisionReadyActivationObstacle reports why the activation gate cannot be
// satisfied for this item as it stands, or "" when the idle conjunction it
// requires is observable.
//
// It exists as a regression guard on a shape that used to block: a ready item's
// workers are all parked (the implementer after the last publish, the reviewer
// after its review), and factoryWorkers.Observe once skipped a parked worker
// entirely, so runtimeKnown, turnIdle, activeWorkKnown and workIdle all stayed
// false and activateAuthorizedRevision returned before the ready transition.
// Observe now reports a fully parked item as known-idle, so the conjunction
// holds here and the ready-PR redraft is reachable and asserted below; the
// guard reports the four flags if that ever stops being true.
func revisionReadyActivationObstacle(t *testing.T, h *revisionEndToEndHarness, i FactoryItem) string {
	t.Helper()
	obs, err := h.effects.Observe(context.Background(), i)
	if err != nil {
		return "the item cannot be observed: " + err.Error()
	}
	if obs.RuntimeKnown && obs.TurnIdle && obs.ActiveWorkKnown && obs.WorkIdle {
		return ""
	}
	parked := 0
	for _, w := range i.Workers {
		if w.Parked {
			parked++
		}
	}
	return fmt.Sprintf("a %s item with %d/%d workers parked observes runtimeKnown=%v turnIdle=%v activeWorkKnown=%v workIdle=%v, so activation would return before the ready transition",
		i.State, parked, len(i.Workers), obs.RuntimeKnown, obs.TurnIdle, obs.ActiveWorkKnown, obs.WorkIdle)
}

// followUpConfirm is the one confirmation a follow-up test replays verbatim, so
// the replay carries the request identity and the version it was written
// against.
func (h *revisionEndToEndHarness) followUpConfirm(ctx context.Context, t *testing.T, proposalID string, follow FactoryCreate) FactoryItem {
	t.Helper()
	current := h.item(t)
	p := proposalAt(t, current, proposalID)
	cmd := FactoryCommand{
		RequestID: "confirm-follow-up", Kind: "revision_follow_up_confirm", ExpectedVersion: current.Version,
		Revision: &FactoryRevisionCommand{ProposalID: p.ID, ProposalVersion: p.Version, CandidateHash: p.CandidateHash, FollowUp: &follow},
	}
	before := len(h.store.List())
	saved, err := h.controller.Command(ctx, h.id, cmd)
	if err != nil {
		t.Fatalf("confirm follow-up: %v", err)
	}
	// The client never heard the answer: the same bytes replay the same result
	// and create no second draft.
	replayed, err := h.controller.Command(ctx, h.id, cmd)
	if err != nil {
		t.Fatalf("replayed follow-up confirmation: %v", err)
	}
	if n := len(h.store.List()); n != before+1 {
		t.Fatalf("the confirmation and its replay left %d items, want one new draft", n-before)
	}
	if first, second := proposalAt(t, saved, proposalID), proposalAt(t, replayed, proposalID); first.FollowUpItemID != second.FollowUpItemID || first.FollowUpItemID == "" {
		t.Fatalf("the replay changed the association: %q then %q", first.FollowUpItemID, second.FollowUpItemID)
	}
	return saved
}

// TestFactoryRevisionEndToEnd drives the whole lifecycle on the real delivery
// fixture: an old turn keeps running under the revision it started under, the
// proposal is assessed and authorized without that changing, an interrupt
// delivers the candidate into the live turn with a correlated receipt, the old
// effect settles, the revision activates at the safe boundary, and the fresh
// implementation, CI and review then run under the new approval. No review from
// the old revision may mark the new one ready.
func TestFactoryRevisionEndToEnd(t *testing.T) {
	ctx := context.Background()
	h := newRevisionEndToEndHarness(t)
	hold := newRevisionTurnHold("implement")
	h.adapter.hold = hold.enter

	held := h.drive(ctx, t, 120, func(i FactoryItem) bool {
		e := factoryLastEffect(i)
		return e != nil && e.Kind == "implement" && e.Status == "dispatched"
	})
	hold.waitStarted(t)
	if e := factoryLastEffect(held); e == nil || e.Kind != "implement" || e.ApprovalRevision != 1 {
		t.Fatalf("the old turn is not running under revision 1: %+v", e)
	}
	if len(held.Approvals) != 1 || held.Approvals[0].Plan != "exact plan\n" {
		t.Fatalf("the approved revision before the proposal = %+v", held.Approvals)
	}
	worker := held.Workers[len(held.Workers)-1]
	if worker.SessionID == "" {
		t.Fatalf("the fixture spawned no worker session: %+v", held.Workers)
	}

	// The proposal is journal history: the running turn keeps running, and the
	// revision it was started under stays active.
	proposed := h.propose(ctx, t, "rev-propose", revisionCandidatePlan, revisionCandidateSpec)
	if len(proposed.Proposals) != 1 {
		t.Fatalf("proposals = %+v", proposed.Proposals)
	}
	if p := proposed.Proposals[0]; p.Status != "proposed" || p.AssessmentState != "pending" || !p.AutoAssess {
		t.Fatalf("saved proposal = %+v", p)
	}
	if len(proposed.Approvals) != 1 || proposed.Approvals[0].Plan != "exact plan\n" {
		t.Fatalf("saving a candidate changed the active revision: %+v", proposed.Approvals)
	}
	if e := factoryLastEffect(proposed); e == nil || e.Status != "dispatched" {
		t.Fatalf("saving a candidate disturbed the running turn: %+v", e)
	}

	// Assessment: the intent is claimed before the planner is contacted, and an
	// answer alone authorizes nothing without project delegation.
	assessed := h.pass(ctx, t)
	if h.planner.callCount() != 1 {
		t.Fatalf("planner calls = %d", h.planner.callCount())
	}
	if states := h.planner.dispatchedStates(); len(states) != 1 || states[0] != "assessing" {
		t.Fatalf("the planner was contacted before the claim was durable: %v", states)
	}
	proposal := assessed.Proposals[0]
	if proposal.AssessmentState != "assessed" || proposal.Status != "awaiting_authorization" {
		t.Fatalf("assessed proposal = %+v", proposal)
	}
	if a := proposal.Assessment; a == nil || a.Classification != "routine_clarification" || a.Recommendation != "amend" || a.Strategy != "interrupt" || a.CandidateHash != proposal.CandidateHash {
		t.Fatalf("recorded assessment = %+v", a)
	}
	if len(assessed.Approvals) != 1 {
		t.Fatalf("an assessment authorized without delegation: %+v", assessed.Approvals)
	}
	if proposal.Delivery != nil {
		t.Fatalf("an assessment opened a delivery: %+v", proposal.Delivery)
	}
	if e := factoryLastEffect(assessed); e == nil || e.Status != "dispatched" {
		t.Fatalf("assessment disturbed the running turn: %+v", e)
	}
	if calls := h.seam.steerCalls(); len(calls) != 0 {
		t.Fatalf("an assessment steered the running turn: %+v", calls)
	}

	// Authorization binds the exact candidate and keeps the old approval active
	// while the old effect is still running.
	authorized := h.authorize(ctx, t, proposal.ID, "amend", "interrupt")
	authorizedProposal := latestProposal(t, authorized)
	if authorizedProposal.Status != "authorized" || authorizedProposal.Delivery == nil {
		t.Fatalf("authorized proposal = %+v", authorizedProposal)
	}
	if d := authorizedProposal.Delivery; d.Strategy != "interrupt" || d.State != "pending" || d.CandidateHash != authorizedProposal.CandidateHash {
		t.Fatalf("authorized delivery = %+v", d)
	}
	if len(authorized.Approvals) != 1 || authorized.Approvals[0].Revision != 1 {
		t.Fatalf("an authorization moved the active revision: %+v", authorized.Approvals)
	}
	requestID := factoryRevisionSteerRequestID(h.id, authorizedProposal.ID)

	// Delivery: one Send now request, aimed at the turn the runtime reported,
	// named by the journal's request identity, and recorded as the receipt the
	// runtime returned for it.
	delivered := h.tick()
	delivery := latestProposal(t, delivered).Delivery
	if delivery == nil || delivery.State != "accepted" || delivery.RequestID != requestID {
		t.Fatalf("delivery evidence = %+v", delivery)
	}
	if delivery.TurnID != "turn-1" || delivery.SessionID != worker.SessionID {
		t.Fatalf("delivery names turn %q session %q, want turn-1 and %q", delivery.TurnID, delivery.SessionID, worker.SessionID)
	}
	calls := h.seam.steerCalls()
	if len(calls) != 1 {
		t.Fatalf("Send now calls = %d", len(calls))
	}
	// The fixture records the call's own session field, so the journal's
	// SessionID above is the evidence that it named the live worker session.
	if calls[0].turnID != "turn-1" || calls[0].requestID != requestID {
		t.Fatalf("Send now = %+v", calls[0])
	}
	if !strings.Contains(calls[0].text, revisionCandidatePlan) || !strings.Contains(calls[0].text, authorizedProposal.CandidateHash) {
		t.Fatalf("the steered direction does not carry the candidate: %s", calls[0].text)
	}
	if len(delivered.Approvals) != 1 {
		t.Fatalf("a delivery activated a revision: %+v", delivered.Approvals)
	}

	// A direct steering event is still the ordinary queued steer: no delivery
	// request, no receipt, and no move of the proposal.
	reg, session := h.workerRegistry(t, delivered)
	if err := reg.Steer(ctx, session, "prefer the smaller diff"); err != nil {
		t.Fatalf("a direct steering event was refused: %v", err)
	}
	var direct string
	for _, f := range h.workerFrames(t, delivered) {
		if f.method == "session/steer" && !strings.Contains(f.params, `"delivery"`) {
			direct = f.params
		}
	}
	if direct == "" || !strings.Contains(direct, "prefer the smaller diff") {
		t.Fatalf("the direct steering frame = %q", direct)
	}
	settledPass := h.tick()
	if d := latestProposal(t, settledPass).Delivery; d == nil || d.State != "accepted" {
		t.Fatalf("a queued steer moved the delivery evidence: %+v", d)
	}
	if n := len(h.seam.steerCalls()); n != 1 {
		t.Fatalf("Send now was repeated: %d calls", n)
	}
	if len(settledPass.Approvals) != 1 {
		t.Fatalf("an unsettled old effect was activated over: %+v", settledPass.Approvals)
	}

	// The old turn runs to an outcome, the old effect settles, and only then
	// does the approved revision activate at the safe boundary.
	hold.finish()
	activated := h.drive(ctx, t, 120, func(i FactoryItem) bool { return len(i.Approvals) == 2 })
	if len(activated.Approvals) != 2 {
		t.Fatalf("the revision never activated: state=%s decision=%+v last=%+v", activated.State, activated.Decision, factoryLastEffect(activated))
	}
	for _, e := range activated.Effects {
		if e.Kind == "implement" && e.ApprovalRevision == 1 && e.Status != "completed" {
			t.Fatalf("the revision activated before the old effect settled: %+v", e)
		}
	}
	approval := activated.Approvals[1]
	if approval.Revision != 2 || approval.Plan != revisionCandidatePlan || approval.Spec != revisionCandidateSpec {
		t.Fatalf("activated approval = %+v", approval)
	}
	if approval.PlanHash != FactoryPlanHash(revisionCandidatePlan, revisionCandidateSpec) || approval.Config.TargetBranch != activated.Approvals[0].Config.TargetBranch {
		t.Fatalf("activated approval carries the wrong binding: %+v", approval)
	}
	if old := activated.Approvals[0]; old.Revision != 1 || old.Plan != "exact plan\n" || old.Spec != "exact spec\n" {
		t.Fatalf("the old approval was rewritten: %+v", old)
	}
	if p := latestProposal(t, activated); p.Status != "activated" {
		t.Fatalf("proposal after activation = %+v", p)
	}
	if activated.State == "ready" {
		t.Fatal("activation claimed readiness")
	}
	for _, e := range activated.Effects {
		if e.Kind == "set_ready" && e.ApprovalRevision != 2 {
			t.Fatalf("a readiness was earned under the old revision: %+v", e)
		}
	}
	if !revisionHistoryHas(activated, "revision_activated", latestProposal(t, activated).ID) {
		t.Fatalf("activation was not journaled: %+v", activated.History)
	}

	// The new revision then earns its own implementation, CI and review.
	ready := h.drive(ctx, t, 160, func(i FactoryItem) bool { return i.State == "ready" || i.Decision != nil })
	if ready.State != "ready" || ready.Decision != nil {
		t.Fatalf("the new revision never reached ready: state=%s decision=%+v last=%+v", ready.State, ready.Decision, factoryLastEffect(ready))
	}
	if len(ready.Approvals) != 2 {
		t.Fatalf("approvals = %+v", ready.Approvals)
	}
	if len(ready.Rounds) != 2 {
		t.Fatalf("rounds = %d: %+v", len(ready.Rounds), ready.Rounds)
	}
	if first := ready.Rounds[0]; first.ApprovalRevision != 1 || first.Review == nil {
		t.Fatalf("the old round's review history was lost: %+v", first)
	}
	fresh := ready.Rounds[1]
	if fresh.ApprovalRevision != 2 || fresh.Review == nil || fresh.Implementation == nil {
		t.Fatalf("the new revision's round = %+v", fresh)
	}
	if fresh.Review.HeadSHA != fresh.HeadSHA || fresh.CI == nil || fresh.CI.HeadSHA != fresh.HeadSHA {
		t.Fatalf("the new round's evidence is not bound to its own head: %+v", fresh)
	}
	readies := 0
	for _, e := range ready.Effects {
		if e.Kind == "set_ready" {
			readies++
			if e.ApprovalRevision != 2 {
				t.Fatalf("a readiness was earned under revision %d: %+v", e.ApprovalRevision, e)
			}
		}
		if e.Status != "completed" {
			t.Fatalf("unsettled effect at ready: %+v", e)
		}
	}
	if readies != 1 {
		t.Fatalf("set_ready effects = %d, want exactly the new revision's", readies)
	}
	if len(ready.PRs) != 1 || ready.PRs[0].State != "open" || ready.PRs[0].Draft {
		t.Fatalf("PR after the revision = %+v", ready.PRs)
	}
	if forge := h.forgeState(); forge.creates != 1 {
		t.Fatalf("the revision opened %d pull requests", forge.creates)
	}

	// The prompts prove which plan each implementation round ran under: the
	// old one before activation, the candidate after it.
	var implementer []string
	for _, prompt := range h.prompts {
		if strings.HasPrefix(prompt, "implementer:") {
			implementer = append(implementer, prompt)
		}
	}
	if len(implementer) != 2 {
		t.Fatalf("implementer prompts = %d: %v", len(implementer), implementer)
	}
	if !strings.Contains(implementer[0], "exact plan") || strings.Contains(implementer[0], "revised plan") {
		t.Fatalf("the first implementation did not run under the approved revision: %s", implementer[0])
	}
	if !strings.Contains(implementer[1], "revised plan") || strings.Contains(implementer[1], "exact plan") {
		t.Fatalf("the repaired implementation did not run under the new revision: %s", implementer[1])
	}

	// The journal replays the whole lifecycle: a restart re-derives no second
	// approval and loses no history.
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(filepath.Dir(h.store.lockPath))
	if err != nil {
		t.Fatalf("the revision journal did not replay: %v", err)
	}
	defer reopened.Close()
	persisted, err := reopened.Get(ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Approvals) != 2 || len(persisted.Rounds) != 2 || persisted.Proposals[0].Status != "activated" || persisted.State != "ready" {
		t.Fatalf("the replayed journal lost the lifecycle: %+v", persisted)
	}
	if persisted.Approvals[0].Plan != "exact plan\n" || persisted.Approvals[1].Plan != revisionCandidatePlan {
		t.Fatalf("the replayed approvals = %+v", persisted.Approvals)
	}
}

// TestFactoryRevisionEndToEndInterruptRecovery proves the recovery half of the
// same lifecycle: a lost assessment answer stays uncertain and is retried only
// explicitly, a lost Send now outcome is never resent on its own, an
// unconfirmed delivery blocks activation, a mismatched receipt is not evidence,
// and a restart re-derives the same state without a duplicate request,
// delivery or activation.
func TestFactoryRevisionEndToEndInterruptRecovery(t *testing.T) {
	ctx := context.Background()
	h := newRevisionEndToEndHarness(t)
	hold := newRevisionTurnHold("implement")
	h.adapter.hold = hold.enter

	held := h.drive(ctx, t, 120, func(i FactoryItem) bool {
		e := factoryLastEffect(i)
		return e != nil && e.Kind == "implement" && e.Status == "dispatched"
	})
	hold.waitStarted(t)
	if e := factoryLastEffect(held); e == nil || e.Status != "dispatched" {
		t.Fatalf("the old turn is not running: %+v", e)
	}
	proposed := h.propose(ctx, t, "rev-propose", revisionCandidatePlan, revisionCandidateSpec)
	proposal := proposed.Proposals[0]

	// A lost assessment answer is recorded as uncertain, and nothing retries it.
	h.planner.assess = func(RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		return FactoryRevisionAssessment{}, errors.New("planner connection lost")
	}
	lost := h.pass(ctx, t)
	if p := lost.Proposals[0]; p.AssessmentState != "uncertain" || p.Status != "proposed" {
		t.Fatalf("a lost assessment = %+v", p)
	}
	if countRevisionEvents(lost, "revision_assessment_uncertain") != 1 {
		t.Fatalf("the lost assessment was not journaled: %+v", lost.History)
	}
	h.pass(ctx, t)
	h.pass(ctx, t)
	if n := h.planner.callCount(); n != 1 {
		t.Fatalf("the assessment was retried on its own: %d planner calls", n)
	}

	// Only an explicit retry starts a new attempt, and the retired attempt
	// cannot be answered afterwards.
	stale := h.item(t).Proposals[0]
	retried := h.studio(ctx, t, func(FactoryItem) FactoryCommand {
		return FactoryCommand{
			RequestID: "rev-retry", Kind: "revision_assess_retry",
			Revision: &FactoryRevisionCommand{ProposalID: stale.ID, ProposalVersion: stale.Version, AttemptID: stale.AssessmentAttemptID},
		}
	})
	retriedProposal := retried.Proposals[0]
	if retriedProposal.AssessmentState != "pending" || retriedProposal.AssessmentAttemptID != proposal.ID+":assessment:2" {
		t.Fatalf("retried attempt = %+v", retriedProposal)
	}
	if _, err := h.controller.Command(ctx, h.id, FactoryCommand{
		RequestID: "rev-retry-old", Kind: "revision_assess_retry", ExpectedVersion: h.item(t).Version,
		Revision: &FactoryRevisionCommand{ProposalID: stale.ID, ProposalVersion: retriedProposal.Version, AttemptID: stale.AssessmentAttemptID},
	}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a retired assessment attempt was still current: %v", err)
	}
	h.planner.assess = func(req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		return judgementFor(req, "routine_clarification", "amend", "interrupt"), nil
	}
	assessed := h.pass(ctx, t)
	if p := assessed.Proposals[0]; p.AssessmentState != "assessed" || p.Status != "awaiting_authorization" {
		t.Fatalf("the retried assessment = %+v", p)
	}

	// A Send now whose outcome is lost is uncertain: it is not resent, and it
	// does not let the approval move.
	authorized := h.authorize(ctx, t, proposal.ID, "amend", "interrupt")
	requestID := factoryRevisionSteerRequestID(h.id, proposal.ID)
	h.seam.steerErr = errors.New("send now transport lost")
	lostDelivery := h.tick()
	if d := latestProposal(t, lostDelivery).Delivery; d == nil || d.State != "uncertain" || d.RequestID != requestID {
		t.Fatalf("lost Send now = %+v", d)
	}
	if n := len(h.seam.steerCalls()); n != 1 {
		t.Fatalf("Send now calls after a lost outcome = %d", n)
	}
	if n := countRevisionEvents(lostDelivery, "revision_delivery_intent"); n != 1 {
		t.Fatalf("delivery intents = %d", n)
	}
	for n := 0; n < 2; n++ {
		again := h.tick()
		if len(again.Approvals) != 1 {
			t.Fatalf("an unproven delivery let the revision activate: %+v", again.Approvals)
		}
		if n := len(h.seam.steerCalls()); n != 1 {
			t.Fatalf("an unproven delivery was resent: %d Send now calls", n)
		}
	}
	if factoryRevisionDeliverySettled(h.item(t).Proposals[0]) {
		t.Fatal("an uncertain delivery was read as settled")
	}
	if len(authorized.Approvals) != 1 {
		t.Fatalf("authorization moved the approval before any delivery: %+v", authorized.Approvals)
	}

	// A restart re-derives the same state: no resend, no second intent.
	restarted := h.restart(t)
	for n := 0; n < 3; n++ {
		if err := revisionPass(t, restarted); err != nil {
			t.Fatalf("restarted pass: %v", err)
		}
	}
	afterRestart := h.item(t)
	if n := len(h.seam.steerCalls()); n != 1 {
		t.Fatalf("the restart resent Send now: %d calls", n)
	}
	if d := afterRestart.Proposals[0].Delivery; d == nil || d.State != "uncertain" {
		t.Fatalf("the restart changed the delivery evidence: %+v", d)
	}
	if n := countRevisionEvents(afterRestart, "revision_delivery_intent"); n != 1 {
		t.Fatalf("the restart wrote another intent: %d", n)
	}

	// An explicit Studio choice starts the new delivery; a receipt naming
	// another turn is not evidence, and only the correlated one settles it.
	h.seam.steerErr = nil
	chosen := h.choose(ctx, t, proposal.ID, "interrupt")
	if d := latestProposal(t, chosen).Delivery; d == nil || d.State != "pending" {
		t.Fatalf("explicit delivery choice = %+v", d)
	}
	accepted := h.tick()
	if d := latestProposal(t, accepted).Delivery; d == nil || d.State != "accepted" || d.RequestID != requestID {
		t.Fatalf("redelivered evidence = %+v", d)
	}
	calls := h.seam.steerCalls()
	if len(calls) != 2 {
		t.Fatalf("Send now calls = %d, want the one explicit resend", len(calls))
	}
	if calls[1].requestID != requestID || calls[1].turnID != "turn-1" {
		t.Fatalf("the resend named %+v, want the same request identity", calls[1])
	}
	h.seam.observeReceipt(RevisionSteeringReceipt{Version: 2, RequestID: requestID, TurnID: "turn-9", Status: "delivered"})
	if d := latestProposal(t, h.tick()).Delivery; d == nil || d.State != "accepted" {
		t.Fatalf("a receipt for another turn was accepted: %+v", d)
	}
	h.seam.observeReceipt(RevisionSteeringReceipt{Version: 3, RequestID: requestID, TurnID: "turn-1", Status: "delivered"})
	correlated := h.tick()
	if d := latestProposal(t, correlated).Delivery; d == nil || d.State != "delivered" {
		t.Fatalf("the correlated receipt was not recorded: %+v", d)
	}
	if len(correlated.Approvals) != 1 {
		t.Fatalf("a delivery activated a revision: %+v", correlated.Approvals)
	}

	// The old turn finishes and the revision activates at the safe boundary,
	// granting no new round.
	hold.finish()
	activated := h.drive(ctx, t, 120, func(i FactoryItem) bool { return len(i.Approvals) == 2 })
	if len(activated.Approvals) != 2 {
		t.Fatalf("the revision never activated: state=%s decision=%+v", activated.State, activated.Decision)
	}
	if d := latestProposal(t, activated).Delivery; d == nil || d.State != "delivered" {
		t.Fatalf("activation forgot the delivery evidence: %+v", d)
	}
	if len(activated.Grants) != 1 {
		t.Fatalf("activating a revision changed the round allowance: %+v", activated.Grants)
	}

	// The work under the new revision completes, and the whole recovery
	// replays: one intent, one activation, the delivered evidence, no second
	// steer and no duplicate activation.
	recovered := h.drive(ctx, t, 160, func(i FactoryItem) bool { return i.State == "ready" || i.Decision != nil })
	if recovered.State != "ready" || recovered.Decision != nil {
		t.Fatalf("the recovered item never settled: state=%s decision=%+v", recovered.State, recovered.Decision)
	}
	restarted = NewFactoryController(h.store, h.adapter, time.Now)
	restarted.setRevisionPlanner(h.planner)
	restarted.admitAfterRecovery()
	for n := 0; n < 3; n++ {
		if err := revisionPass(t, restarted); err != nil {
			t.Fatalf("restarted pass: %v", err)
		}
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(filepath.Dir(h.store.lockPath))
	if err != nil {
		t.Fatalf("the recovery journal did not replay: %v", err)
	}
	defer reopened.Close()
	persisted, err := reopened.Get(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Approvals) != 2 || countRevisionEvents(persisted, "revision_activated") != 1 {
		t.Fatalf("the replayed journal duplicated the activation: %+v", persisted.Approvals)
	}
	if n := countRevisionEvents(persisted, "revision_delivery_intent"); n != 2 {
		// The lost attempt and the explicit resend are two intents, and the
		// replay added none.
		t.Fatalf("the replayed journal holds %d delivery intents, want the lost one and its explicit resend", n)
	}
	if d := persisted.Proposals[0].Delivery; d == nil || d.State != "delivered" {
		t.Fatalf("the replayed delivery evidence = %+v", d)
	}
	if n := len(h.seam.steerCalls()); n != 2 {
		t.Fatalf("the replay steered again: %d calls", n)
	}
	replayed := NewFactoryController(reopened, h.adapter, time.Now)
	replayed.setRevisionPlanner(h.planner)
	replayed.admitAfterRecovery()
	for n := 0; n < 3; n++ {
		if err := revisionPass(t, replayed); err != nil {
			t.Fatalf("replayed pass: %v", err)
		}
	}
	settled, err := reopened.Get(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRevisionEvents(settled, "revision_delivery_intent"); n != 2 {
		t.Fatalf("the replayed pass wrote another intent: %d", n)
	}
	if countRevisionEvents(settled, "revision_activated") != 1 || len(settled.Approvals) != 2 {
		t.Fatalf("the replayed pass duplicated an activation: %+v", settled.Approvals)
	}
	if n := len(h.seam.steerCalls()); n != 2 {
		t.Fatalf("the replayed pass steered again: %d calls", n)
	}
}

// TestFactoryRevisionEndToEndFollowUp proves the ready half: a follow-up is
// created once, linked to its source, and a lost association is reconciled
// after a restart; an authorized amendment of a ready item returns the pull
// request to draft before it activates; and the prior approval and review
// history stays intact throughout.
func TestFactoryRevisionEndToEndFollowUp(t *testing.T) {
	ctx := context.Background()
	h := newRevisionEndToEndHarness(t)
	h.planner.assess = func(req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		if strings.Contains(req.ProposedPlan, "follow-up") {
			return judgementFor(req, "scope_change", "follow_up", "hold"), nil
		}
		return judgementFor(req, "scope_change", "amend", "wait_safe"), nil
	}

	ready := h.drive(ctx, t, 160, func(i FactoryItem) bool { return i.State == "ready" || i.Decision != nil })
	if ready.State != "ready" || ready.Decision != nil {
		t.Fatalf("the fixture never reached ready: state=%s decision=%+v", ready.State, ready.Decision)
	}
	if len(ready.PRs) != 1 || ready.PRs[0].Draft {
		t.Fatalf("the ready PR = %+v", ready.PRs)
	}
	if len(ready.Approvals) != 1 || len(ready.Rounds) != 2 {
		t.Fatalf("the ready item = approvals %d rounds %d", len(ready.Approvals), len(ready.Rounds))
	}
	readyMutations := h.forgeState().mutations

	// A follow-up is authorized, confirmed and created as an unapproved draft
	// that names its source. The source keeps its own ready revision.
	h.propose(ctx, t, "rev-follow-up", revisionFollowUpPlan, revisionFollowUpSpec)
	assessedFollowUp := h.pass(ctx, t)
	followProposal := latestProposal(t, assessedFollowUp)
	if followProposal.Routing != "follow_up" || followProposal.AssessmentState != "assessed" {
		t.Fatalf("the follow-up proposal = %+v", followProposal)
	}
	pending := h.authorize(ctx, t, followProposal.ID, "follow_up", "hold")
	if p := latestProposal(t, pending); p.Status != "follow_up_pending" {
		t.Fatalf("follow-up authorization = %+v", p)
	}
	if len(pending.Approvals) != 1 || pending.State != "ready" {
		t.Fatalf("a follow-up authorization moved the source: state=%s approvals=%d", pending.State, len(pending.Approvals))
	}
	follow := FactoryCreate{
		Title: "follow-up title", Plan: revisionFollowUpPlan, Spec: revisionFollowUpSpec,
		Config: FactoryConfig{RepoID: pending.RepoID, TargetBranch: "main", Native: true, NoRequiredCIAcknowledged: true},
	}
	confirmed := h.followUpConfirm(ctx, t, followProposal.ID, follow)
	linked := proposalAt(t, confirmed, followProposal.ID)
	if linked.Status != "followed_up" || linked.FollowUpItemID == "" {
		t.Fatalf("the follow-up was not associated: %+v", linked)
	}
	if linked.FollowUpRequestID != factoryRevisionFollowUpRequestID(h.id, linked.ID) {
		t.Fatalf("followUpRequestId = %q", linked.FollowUpRequestID)
	}
	if len(confirmed.Approvals) != 1 || confirmed.State != "ready" || len(confirmed.Rounds) != 2 {
		t.Fatalf("a follow-up changed the source: state=%s approvals=%d rounds=%d", confirmed.State, len(confirmed.Approvals), len(confirmed.Rounds))
	}
	items := h.store.List()
	if len(items) != 2 {
		t.Fatalf("items = %d, want the source and one follow-up draft", len(items))
	}
	created := followUpItem(t, items, linked.FollowUpItemID)
	if created.State != "draft" || len(created.Approvals) != 0 || created.Decision != nil {
		t.Fatalf("the follow-up is not an unapproved draft: %+v", created)
	}
	if created.Draft.Plan != revisionFollowUpPlan || created.Draft.Spec != revisionFollowUpSpec || created.Draft.Title != "follow-up title" {
		t.Fatalf("the follow-up content = %+v", created.Draft)
	}
	if !revisionSourceNames(created, h.id, followProposal.ID) {
		t.Fatalf("the created draft does not name its source: %+v", created.History)
	}

	// The amendment is assessed and authorized without touching the PR.
	proposed := h.propose(ctx, t, "rev-amend", revisionAmendedPlan, revisionAmendedSpec)
	if p := proposed.Proposals[len(proposed.Proposals)-1]; p.AssessmentState != "pending" || p.BaseRevision != 1 {
		t.Fatalf("the saved amendment = %+v", p)
	}
	assessed := h.pass(ctx, t)
	amendment := latestProposal(t, assessed)
	if amendment.AssessmentState != "assessed" || amendment.Routing != "amend" {
		t.Fatalf("the amendment's assessment = %+v", amendment)
	}
	if a := amendment.Assessment; a == nil || a.Strategy != "wait_safe" {
		t.Fatalf("the amendment's strategy = %+v", a)
	}
	authorized := h.authorize(ctx, t, amendment.ID, "amend", "wait_safe")
	if len(authorized.Approvals) != 1 {
		t.Fatalf("an authorization moved the active revision: %+v", authorized.Approvals)
	}
	if got := h.forgeState(); got.mutations != readyMutations || got.draft {
		t.Fatalf("the authorization touched the forge: %+v", got)
	}

	// wait_safe claims no delivery at all: the candidate reaches the worker
	// through the next factory prompt, after the approval moves.
	if err := h.controller.deliverAuthorizedRevisions(ctx); err != nil {
		t.Fatal(err)
	}
	waiting := h.item(t)
	if d := latestProposal(t, waiting).Delivery; d == nil || d.State != "waiting_safe_boundary" || d.RequestID != "" {
		t.Fatalf("wait_safe delivery = %+v", d)
	}
	if len(waiting.Approvals) != 1 || waiting.State != "ready" {
		t.Fatalf("wait_safe moved a ready item: state=%s approvals=%d", waiting.State, len(waiting.Approvals))
	}
	if calls := h.seam.steerCalls(); len(calls) != 0 {
		t.Fatalf("wait_safe steered the running turn: %+v", calls)
	}

	// The ready PR has to return to draft before the approval may move: the
	// transition is journaled, dispatched and confirmed on one pass, and only
	// the following pass activates the revision.
	wantApprovals, wantRounds := 1, 2
	t.Run("ready PR returns to draft before activation", func(t *testing.T) {
		if obstacle := revisionReadyActivationObstacle(t, h, h.item(t)); obstacle != "" {
			// The activation gate requires the idle conjunction whether or not
			// the item is fully parked, and it holds for this shape. A
			// regression has to fail rather than skip: the ready transition is
			// reachable, and the assertions below are its evidence.
			t.Fatalf("the ready activation gate is not observable: %s", obstacle)
		}
		transitioned := h.tick()
		if transitioned.Decision != nil {
			t.Fatalf("the ready transition decision = %+v", transitioned.Decision)
		}
		last := factoryLastEffect(transitioned)
		if last == nil || last.Kind != "set_draft" || last.Status != "completed" {
			t.Fatalf("the ready transition effect = %+v", last)
		}
		if !revisionHistoryHas(transitioned, "revision_transition_drafted", last.ID) {
			t.Fatalf("the transition was not confirmed: %+v", transitioned.History)
		}
		if len(transitioned.Approvals) != 1 {
			t.Fatalf("the revision activated before the PR was a draft: %+v", transitioned.Approvals)
		}
		if forge := h.forgeState(); !forge.draft || forge.mutations != readyMutations+1 {
			t.Fatalf("the PR was not returned to draft: %+v", forge)
		}
		activated := h.tick()
		if len(activated.Approvals) != 2 || activated.Approvals[1].Plan != revisionAmendedPlan {
			t.Fatalf("the ready revision never activated: %+v", activated.Approvals)
		}
		if p := proposalAt(t, activated, amendment.ID); p.Status != "activated" {
			t.Fatalf("the amendment's state after activation = %+v", p)
		}
		if old := activated.Approvals[0]; old.Plan != "exact plan\n" || old.Revision != 1 {
			t.Fatalf("the ready revision's approval was rewritten: %+v", old)
		}
		if len(activated.Rounds) != 2 || activated.Rounds[0].Review == nil || activated.Rounds[1].Review == nil {
			t.Fatalf("the ready revision lost its review history: %+v", activated.Rounds)
		}
		wantApprovals, wantRounds = 2, 3

		// The source keeps working under its new revision; that review history
		// is the evidence a follow-up must never overwrite.
		restReady := h.drive(ctx, t, 160, func(i FactoryItem) bool { return i.State == "ready" || i.Decision != nil })
		if restReady.State != "ready" || restReady.Decision != nil {
			t.Fatalf("the amended item never settled: state=%s decision=%+v", restReady.State, restReady.Decision)
		}
		if len(restReady.Rounds) != 3 || restReady.Rounds[2].ApprovalRevision != 2 || restReady.Rounds[2].Review == nil {
			t.Fatalf("the amended item's rounds = %+v", restReady.Rounds)
		}
		readies := map[uint64]int{}
		for _, e := range restReady.Effects {
			if e.Kind == "set_ready" {
				readies[e.ApprovalRevision]++
			}
		}
		// The readiness the old revision earned stays in history, and the new
		// revision earned exactly one of its own.
		if readies[1] != 1 || readies[2] != 1 {
			t.Fatalf("set_ready effects by revision = %v, want one per revision", readies)
		}
	})

	// A crash between the create and its association: the draft is durable and
	// the link is lost. The restarted reconciliation restores it and creates
	// nothing.
	lost := h.item(t)
	if _, err := h.store.Update(lost.ID, "lose-association", lost.Version, json.RawMessage(`{"kind":"fixture","lost":"association"}`), func(dst *FactoryItem) error {
		n, ok := factoryRevisionProposalByID(*dst, followProposal.ID)
		if !ok {
			return ErrFactoryConflict
		}
		// Only the link is lost: the confirmation's own record survives, which
		// is what makes the deterministic request identity the lookup.
		dst.Proposals[n].FollowUpItemID = ""
		dst.Proposals[n].Version++
		dst.Proposals[n].UpdatedAt = time.Now().UTC()
		return nil
	}, time.Now().UTC()); err != nil {
		t.Fatalf("losing the association: %v", err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(filepath.Dir(h.store.lockPath))
	if err != nil {
		t.Fatalf("the follow-up journal did not replay: %v", err)
	}
	defer reopened.Close()
	restarted := NewFactoryController(reopened, nil, time.Now)
	if err := restarted.reconcileRevisionFollowUps(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n := len(reopened.List()); n != 2 {
		t.Fatalf("reconciliation created a duplicate: %d items", n)
	}
	recovered, err := reopened.Get(h.id)
	if err != nil {
		t.Fatal(err)
	}
	restoredProposal := proposalAt(t, recovered, followProposal.ID)
	if restoredProposal.Status != "followed_up" || restoredProposal.FollowUpItemID != linked.FollowUpItemID {
		t.Fatalf("the lost association was not restored: %+v", restoredProposal)
	}
	if len(recovered.Approvals) != wantApprovals || recovered.Approvals[0].Plan != "exact plan\n" {
		t.Fatalf("reconciliation disturbed the approvals: %+v", recovered.Approvals)
	}
	if wantApprovals == 2 && recovered.Approvals[1].Plan != revisionAmendedPlan {
		t.Fatalf("reconciliation disturbed the activated approval: %+v", recovered.Approvals)
	}
	if len(recovered.Rounds) != wantRounds {
		t.Fatalf("reconciliation disturbed the rounds: %+v", recovered.Rounds)
	}
	restored := countRevisionEvents(recovered, "revision_follow_up_linked")
	if err := restarted.reconcileRevisionFollowUps(); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	again, err := reopened.Get(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRevisionEvents(again, "revision_follow_up_linked"); n != restored {
		t.Fatalf("a second reconciliation wrote another association: %d events", n)
	}
}

// TestFactoryRevisionEndToEndLegacyReplay proves an item journalled before
// proposals existed keeps its bytes and accepts the whole lifecycle afterwards,
// including the manual path a project with no planner is left with: the
// assessment intent is recorded as unavailable, the user chooses the delivery
// strategy explicitly, and the candidate activates at the safe boundary. An
// activation grants no round, so the existing allowance still decides whether a
// new implementation may run.
func TestFactoryRevisionEndToEndLegacyReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := factoryReq()
	req.Plan, req.Spec = legacyApprovedPlan, legacyApprovedSpec
	req.Config.MaxRounds = 1
	created, err := store.Create(req, now)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := store.Update(created.ID, "approve", created.Version, json.RawMessage(`{"kind":"approve"}`), func(dst *FactoryItem) error {
		dst.Approvals = append(dst.Approvals, FactoryApproval{
			Revision: 1, Plan: dst.Draft.Plan, Spec: dst.Draft.Spec,
			PlanHash: FactoryPlanHash(dst.Draft.Plan, dst.Draft.Spec), Config: dst.Draft.Config, ApprovedAt: now,
		})
		dst.Grants = append(dst.Grants, FactoryGrant{ID: "approve", Reason: "initial approval", AdditionalRounds: 1, At: now})
		dst.State = "queued"
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	legacyRaw, err := json.Marshal(legacy.Approvals[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.Proposals) != 0 {
		t.Fatalf("the legacy item carries proposals: %+v", legacy.Proposals)
	}

	// The implementer has just been resumed, so the next effect this item
	// derives is an implementation under whatever revision is active then:
	// that is the shape a revision is authorized against, and the shape the
	// round allowance is measured on.
	implementing, err := store.Update(legacy.ID, "fixture-implementing", legacy.Version, json.RawMessage(`{"kind":"fixture","tag":"implementing"}`), func(dst *FactoryItem) error {
		dst.State = "implementing"
		dst.BaseSHA = "base"
		dst.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: 1, HeadSHA: "head", BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{ID: dst.ID + "-1", Kind: "resume", WorkerRole: "implementer", Status: "completed", ApprovalRevision: 1}}
		return nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	ctl := NewFactoryController(store, nil, func() time.Time { return now })
	fx := &revisionFakeEffects{obs: revisionIdleObservation(implementing)}
	ctl.effects = fx
	if _, ok := ctl.revisionPlannerOrDefault().(unavailableRevisionPlanner); !ok {
		t.Fatal("the controller does not default to the unavailable planner")
	}

	active := implementing.Approvals[len(implementing.Approvals)-1]
	proposed, err := ctl.Command(ctx, implementing.ID, FactoryCommand{
		RequestID: "rev-propose", Kind: "revision_propose", ExpectedVersion: implementing.Version,
		Revision: &FactoryRevisionCommand{
			BaseRevision: active.Revision, BaseHash: active.PlanHash,
			Plan: revisionCandidatePlan, Spec: revisionCandidateSpec,
			CandidateHash: FactoryPlanHash(revisionCandidatePlan, revisionCandidateSpec),
		},
	})
	if err != nil {
		t.Fatalf("propose on a legacy item: %v", err)
	}
	if p := proposed.Proposals[0]; !p.AutoAssess || p.AssessmentState != "pending" {
		t.Fatalf("the legacy item's proposal = %+v", p)
	}

	// No planner is configured: the intent is recorded unavailable and nothing
	// is contacted or retried.
	if err := ctl.assessPending(ctx); err != nil {
		t.Fatal(err)
	}
	unavailable := mustRevisionItem(t, store, implementing.ID)
	if p := unavailable.Proposals[0]; p.AssessmentState != "unavailable" || p.Status != "proposed" {
		t.Fatalf("a project with no planner = %+v", p)
	}
	if countRevisionEvents(unavailable, "revision_assessment_unavailable") != 1 {
		t.Fatalf("the unavailable assessment was not journaled: %+v", unavailable.History)
	}
	if err := ctl.assessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if again := mustRevisionItem(t, store, implementing.ID); again.Version != unavailable.Version {
		t.Fatal("an unavailable planner was retried in the background")
	}

	// Without an assessment there is no recommended strategy, so the user has
	// to choose one; the choice is recorded and then authorized.
	if _, err := ctl.Command(ctx, implementing.ID, FactoryCommand{
		RequestID: "rev-authorize-no-strategy", Kind: "revision_authorize", ExpectedVersion: unavailable.Version,
		Revision: &FactoryRevisionCommand{
			ProposalID: unavailable.Proposals[0].ID, ProposalVersion: unavailable.Proposals[0].Version,
			BaseRevision: unavailable.Proposals[0].BaseRevision, BaseHash: unavailable.Proposals[0].BaseHash,
			CandidateHash: unavailable.Proposals[0].CandidateHash, Routing: "amend",
		},
	}); !errors.Is(err, errFactoryInput) {
		t.Fatalf("an authorization without a strategy = %v", err)
	}
	chosen, err := ctl.Command(ctx, implementing.ID, FactoryCommand{
		RequestID: "rev-choice", Kind: "revision_delivery_choice", ExpectedVersion: unavailable.Version,
		Revision: &FactoryRevisionCommand{ProposalID: unavailable.Proposals[0].ID, ProposalVersion: unavailable.Proposals[0].Version, CandidateHash: unavailable.Proposals[0].CandidateHash, Strategy: "wait_safe"},
	})
	if err != nil {
		t.Fatalf("manual delivery choice: %v", err)
	}
	if d := chosen.Proposals[0].Delivery; d == nil || d.Strategy != "wait_safe" || d.State != "pending" {
		t.Fatalf("manual delivery choice = %+v", d)
	}
	authorized, err := ctl.Command(ctx, implementing.ID, FactoryCommand{
		RequestID: "rev-authorize", Kind: "revision_authorize", ExpectedVersion: chosen.Version,
		Revision: &FactoryRevisionCommand{
			ProposalID: chosen.Proposals[0].ID, ProposalVersion: chosen.Proposals[0].Version,
			BaseRevision: chosen.Proposals[0].BaseRevision, BaseHash: chosen.Proposals[0].BaseHash,
			CandidateHash: chosen.Proposals[0].CandidateHash, Routing: "amend", Strategy: "wait_safe",
		},
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if len(authorized.Approvals) != 1 {
		t.Fatalf("an authorization moved the active revision: %+v", authorized.Approvals)
	}

	// wait_safe claims no delivery: the candidate arrives in the next factory
	// prompt, after the approval moves at the safe boundary.
	if err := ctl.deliverAuthorizedRevisions(ctx); err != nil {
		t.Fatal(err)
	}
	waiting := mustRevisionItem(t, store, implementing.ID)
	if d := waiting.Proposals[0].Delivery; d == nil || d.State != "waiting_safe_boundary" || d.RequestID != "" {
		t.Fatalf("wait_safe delivery = %+v", d)
	}
	if err := ctl.activateRevisionAtSafeBoundary(ctx); err != nil {
		t.Fatal(err)
	}
	activated := mustRevisionItem(t, store, implementing.ID)
	if len(activated.Approvals) != 2 {
		t.Fatalf("the candidate never activated: %+v", activated.Approvals)
	}
	if p := activated.Proposals[0]; p.Status != "activated" {
		t.Fatalf("proposal after activation = %+v", p)
	}
	if got := activated.Approvals[1]; got.Plan != revisionCandidatePlan || got.Spec != revisionCandidateSpec || got.PlanHash != FactoryPlanHash(revisionCandidatePlan, revisionCandidateSpec) {
		t.Fatalf("activated approval = %+v", got)
	}
	gotRaw, err := json.Marshal(activated.Approvals[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRaw) != string(legacyRaw) {
		t.Fatalf("the legacy approval changed:\n%s\n%s", legacyRaw, gotRaw)
	}
	prompt, err := BuildFactoryPrompt(activated, FactoryEffect{ID: activated.ID + "-2", Kind: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "revised plan") || strings.Contains(prompt, "legacy approved plan") {
		t.Fatalf("the next factory prompt does not carry the candidate: %s", prompt)
	}

	// The activation grants no round: the existing allowance decides, and it is
	// exhausted, so no implementation runs under the new revision.
	next, effect, err := AdvanceFactory(activated, revisionIdleObservation(activated), now)
	if err != nil {
		t.Fatal(err)
	}
	if effect != nil {
		t.Fatalf("the exhausted allowance dispatched %s", effect.Kind)
	}
	if next.Decision == nil || next.Decision.Kind != "exhausted" {
		t.Fatalf("the existing allowance decision = %+v", next.Decision)
	}
	if len(next.Grants) != len(activated.Grants) {
		t.Fatalf("activation granted rounds: %+v", next.Grants)
	}

	// The legacy journal replays every byte of the lifecycle it accumulated.
	beforeRaw, err := json.Marshal(activated)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatalf("the legacy journal did not replay: %v", err)
	}
	defer reopened.Close()
	replayed, err := reopened.Get(implementing.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw, err := json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeRaw) != string(afterRaw) {
		t.Fatalf("the replay changed the item:\n%s\n%s", beforeRaw, afterRaw)
	}
	if len(replayed.Proposals) != 1 || replayed.Proposals[0].Status != "activated" {
		t.Fatalf("the replayed proposals = %+v", replayed.Proposals)
	}
	// A fresh controller over the replayed journal sees nothing left to do.
	restarted := NewFactoryController(reopened, fx, func() time.Time { return now })
	if err := restarted.deliverAuthorizedRevisions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.activateRevisionAtSafeBoundary(ctx); err != nil {
		t.Fatal(err)
	}
	settled, err := reopened.Get(implementing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settled.Approvals) != 2 || countRevisionEvents(settled, "revision_activated") != 1 {
		t.Fatalf("the restart duplicated the activation: %+v", settled.Approvals)
	}
}

// mustRevisionItem reads one item from a store or fails.
func mustRevisionItem(t *testing.T, store *FactoryStore, id string) FactoryItem {
	t.Helper()
	i, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return i
}
