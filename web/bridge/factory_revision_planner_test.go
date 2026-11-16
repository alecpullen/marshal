package bridge

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeRevisionPlanner is a planner that answers from memory. It never opens a
// connection, so these tests prove that a normal assessment run contacts no
// provider endpoint; it also records, at the moment of dispatch, the assessment
// state the journal held, which is how the tests prove the claim was durable
// before anything external ran.
type fakeRevisionPlanner struct {
	mu           sync.Mutex
	available    bool
	availableErr error
	assess       func(RevisionAssessmentRequest) (FactoryRevisionAssessment, error)
	calls        []RevisionAssessmentRequest
	states       []string
	store        *FactoryStore
	itemID       string
}

func (f *fakeRevisionPlanner) Available(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.available, f.availableErr
}

func (f *fakeRevisionPlanner) Assess(_ context.Context, req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	if f.store != nil {
		if item, err := f.store.Get(f.itemID); err == nil {
			for _, p := range item.Proposals {
				if p.ID == req.ProposalID {
					f.states = append(f.states, p.AssessmentState)
				}
			}
		}
	}
	answer := f.assess
	f.mu.Unlock()
	if answer == nil {
		return FactoryRevisionAssessment{}, nil
	}
	return answer(req)
}

func (f *fakeRevisionPlanner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRevisionPlanner) dispatchedStates() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.states...)
}

// judgementFor is the assessment a planner returns for one request, bound to
// the exact candidate and base the request named.
func judgementFor(req RevisionAssessmentRequest, classification, recommendation, strategy string) FactoryRevisionAssessment {
	return FactoryRevisionAssessment{
		Classification: classification,
		Reason:         "the candidate only restates an existing step",
		Recommendation: recommendation,
		Strategy:       strategy,
		AssessorID:     "planner-fixture",
		CandidateHash:  req.CandidateHash,
		BaseRevision:   req.BaseRevision,
	}
}

// TestFactoryRevisionAssessmentDefaultPending checks the default path end to
// end: a saved proposal carries one durable assessment intent, the planner's
// claim is journalled before the dispatch, the answer is recorded with the
// classification and routing the planner asserted, and nothing is paused or
// authorized without delegation.
func TestFactoryRevisionAssessmentDefaultPending(t *testing.T) {
	f := newRevisionFixture(t)
	planner := &fakeRevisionPlanner{available: true, store: f.store, itemID: f.item.ID}
	planner.assess = func(req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		return judgementFor(req, "routine_clarification", "amend", "interrupt"), nil
	}
	f.ctl.setRevisionPlanner(planner)

	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	intent := saved.Proposals[0]
	if intent.AssessmentState != "pending" || intent.AssessmentAttemptID != factoryRevisionAssessmentAttemptID(intent.ID) {
		t.Fatalf("the saved proposal does not carry its assessment intent: %+v", intent)
	}
	if planner.callCount() != 0 {
		t.Fatal("saving a proposal dispatched an assessment")
	}

	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if states := planner.dispatchedStates(); len(states) != 1 || states[0] != "assessing" {
		t.Fatalf("the dispatch saw the assessment in state %v, want one \"assessing\"", states)
	}
	if planner.callCount() != 1 {
		t.Fatalf("planner calls = %d, want one", planner.callCount())
	}
	if got := planner.calls[0]; got.ItemID != f.item.ID || got.ProposalID != intent.ID || got.AttemptID != intent.AssessmentAttemptID {
		t.Fatalf("the planner was given %+v", got)
	}

	item := f.current()
	proposal := item.Proposals[0]
	if proposal.AssessmentState != "assessed" || proposal.Assessment == nil {
		t.Fatalf("the answer was not recorded: %+v", proposal)
	}
	a := proposal.Assessment
	if a.Classification != "routine_clarification" || a.Recommendation != "amend" || a.Strategy != "interrupt" || a.AssessorID != "planner-fixture" {
		t.Fatalf("the recorded assessment lost what the planner asserted: %+v", a)
	}
	if a.CandidateHash != intent.CandidateHash || a.BaseRevision != intent.BaseRevision {
		t.Fatalf("the recorded assessment is not bound to the candidate: %+v", a)
	}
	if proposal.Routing != "amend" {
		t.Fatalf("routing = %q, want the recommendation", proposal.Routing)
	}
	// A pause recommendation is recorded and claims nothing: no pause state,
	// no interruption, and no authority without delegation.
	if proposal.PauseState != "" || proposal.PauseRequestID != "" {
		t.Fatalf("a recommendation claimed a pause: %+v", proposal)
	}
	if proposal.Status != "awaiting_authorization" || proposal.Authority != "" {
		t.Fatalf("status/authority = %q/%q, want an undecided proposal", proposal.Status, proposal.Authority)
	}
	if proposal.Version != intent.Version+2 {
		t.Fatalf("proposal version = %d, want one claim and one result", proposal.Version)
	}
	// The item itself was not touched: assessment is advisory.
	if item.State != "queued" || len(item.Effects) != 0 || item.Decision != nil {
		t.Fatalf("assessing a proposal moved the item: %+v", item)
	}

	// One intent, one attempt: a second pass dispatches nothing.
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if planner.callCount() != 1 {
		t.Fatalf("planner calls after a second pass = %d, want one", planner.callCount())
	}
	if got := f.current().Proposals[0]; got.Version != proposal.Version {
		t.Fatalf("a second pass mutated the proposal: %+v", got)
	}
}

// TestFactoryRevisionAssessmentDisabledManualRequest checks a project that
// turned automatic assessment off: the proposal is saved without an intent,
// nothing is dispatched, and the manual request journals the same deterministic
// attempt the automatic path would have used.
func TestFactoryRevisionAssessmentDisabledManualRequest(t *testing.T) {
	f := newRevisionFixture(t)
	f.ctl.projectSettings = func(string) (ProjectSettings, bool) {
		return ProjectSettings{AutoAssessPlanChanges: boolPtr(false)}, true
	}
	planner := &fakeRevisionPlanner{available: true, store: f.store, itemID: f.item.ID}
	planner.assess = func(req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		return judgementFor(req, "scope_change", "follow_up", "hold"), nil
	}
	f.ctl.setRevisionPlanner(planner)

	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if p := saved.Proposals[0]; p.AutoAssess || p.AssessmentState != "not_requested" || p.AssessmentAttemptID != "" {
		t.Fatalf("a disabled project saved an intent: %+v", p)
	}
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if planner.callCount() != 0 {
		t.Fatal("a proposal with no intent was dispatched")
	}
	if p := f.current().Proposals[0]; p.AssessmentState != "not_requested" {
		t.Fatalf("a pass with no intent moved the proposal: %+v", p)
	}

	requested, err := f.command(FactoryCommand{RequestID: "rev-assess", Kind: "revision_assess_request", Revision: revisionAt(saved, 0)})
	if err != nil {
		t.Fatal(err)
	}
	intent := requested.Proposals[0]
	if intent.AssessmentState != "pending" || intent.AssessmentAttemptID != factoryRevisionAssessmentAttemptID(intent.ID) {
		t.Fatalf("the manual request did not journal the attempt: %+v", intent)
	}
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	assessed := f.current().Proposals[0]
	if assessed.AssessmentState != "assessed" || assessed.Assessment == nil || assessed.Assessment.Classification != "scope_change" {
		t.Fatalf("the requested assessment was not recorded: %+v", assessed)
	}
	// A scope change is never authorized by a planner: it needs a user.
	if assessed.Status != "awaiting_authorization" || assessed.Routing != "follow_up" {
		t.Fatalf("a scope change moved to %q/%q", assessed.Status, assessed.Routing)
	}
}

// TestFactoryRevisionPlannerUnavailableNoWake checks the default seam and a
// planner that reports itself unavailable: the intent is recorded as
// unavailable, no assessment is dispatched, nothing is retried and the
// scheduler is not woken.
func TestFactoryRevisionPlannerUnavailableNoWake(t *testing.T) {
	f := newRevisionFixture(t)
	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	// The proposal command wakes the scheduler itself; drain that so the
	// assertion below is about what the planner does.
	select {
	case <-f.ctl.wake:
	default:
	}
	if _, ok := f.ctl.revisionPlannerOrDefault().(unavailableRevisionPlanner); !ok {
		t.Fatal("a controller without a planner does not default to the unavailable one")
	}
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	unavailable := f.current().Proposals[0]
	if unavailable.AssessmentState != "unavailable" || unavailable.AssessmentAttemptID != factoryRevisionAssessmentAttemptID(unavailable.ID) {
		t.Fatalf("an unavailable planner did not record the state: %+v", unavailable)
	}
	select {
	case <-f.ctl.wake:
		t.Fatal("an unavailable planner woke the scheduler")
	default:
	}
	// The unavailable state is terminal for the intent: no second pass, no
	// retry, no version move.
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := f.current().Proposals[0]; again.Version != unavailable.Version {
		t.Fatalf("an unavailable planner retried: %+v", again)
	}

	// A planner that exists but reports this project unavailable behaves the
	// same, and is never asked to assess anything.
	silent := &fakeRevisionPlanner{available: false}
	f.ctl.setRevisionPlanner(silent)
	requested, err := f.command(FactoryCommand{RequestID: "rev-assess", Kind: "revision_assess_request", Revision: revisionAt(f.current(), 0)})
	if err != nil {
		t.Fatal(err)
	}
	if p := requested.Proposals[0]; p.AssessmentState != "pending" {
		t.Fatalf("the explicit request did not journal an intent: %+v", p)
	}
	select {
	case <-f.ctl.wake:
	default:
	}
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if silent.callCount() != 0 {
		t.Fatal("an unavailable planner was asked to assess")
	}
	if p := f.current().Proposals[0]; p.AssessmentState != "unavailable" {
		t.Fatalf("state = %q, want unavailable", p.AssessmentState)
	}
	select {
	case <-f.ctl.wake:
		t.Fatal("an unavailable planner woke the scheduler")
	default:
	}
	if saved.Proposals[0].Assessment != nil {
		t.Fatal("the unavailable path recorded an assessment")
	}
}

// TestFactoryRevisionAssessmentResultStaleTarget checks every exactness rule a
// delivered assessment has to satisfy: the attempt has to be the current one,
// the candidate hash and base revision have to name this proposal, and an
// "affected" claim has to name work the item carries. The refused attempts
// leave the claim in place, so the claiming client can correct itself.
func TestFactoryRevisionAssessmentResultStaleTarget(t *testing.T) {
	f := newRevisionFixture(t)
	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	principal := factoryPrincipal{ClientID: "c1", ProjectID: "p1", Action: factoryCapabilityAssessPlanChanges, RequestID: "claim"}
	claimed, err := f.ctl.CommandAuthorized(context.Background(), saved.ID, FactoryCommand{
		RequestID: "claim", Kind: "revision_assess_claim", ExpectedVersion: saved.Version,
		Revision: &FactoryRevisionCommand{
			ProposalID: saved.Proposals[0].ID, ProposalVersion: saved.Proposals[0].Version,
			AttemptID: saved.Proposals[0].AssessmentAttemptID,
		},
	}, principal)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimed.Proposals[0]
	if claim.AssessmentState != "assessing" {
		t.Fatalf("the claim was not journalled: %+v", claim)
	}

	deliver := func(requestID, attempt string, a FactoryRevisionAssessment) error {
		t.Helper()
		current := f.current()
		_, err := f.ctl.CommandAuthorized(context.Background(), current.ID, FactoryCommand{
			RequestID: requestID, Kind: "revision_assess_result", ExpectedVersion: current.Version,
			Revision: &FactoryRevisionCommand{
				ProposalID: claim.ID, ProposalVersion: current.Proposals[0].Version, AttemptID: attempt,
				CandidateHash: a.CandidateHash, BaseRevision: a.BaseRevision, Assessment: &a,
			},
		}, principal)
		return err
	}
	good := FactoryRevisionAssessment{
		Classification: "routine_clarification", Reason: "restates a step", Recommendation: "amend",
		Strategy: "wait_safe", AssessorID: "planner-fixture",
		CandidateHash: claim.CandidateHash, BaseRevision: claim.BaseRevision,
	}

	// An attempt this proposal is not on.
	if err := deliver("assess-wrong-attempt", "rev-other:assessment:1", good); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a result for a replaced attempt was accepted: %v", err)
	}
	// A candidate this proposal does not carry.
	staleCandidate := good
	staleCandidate.CandidateHash = FactoryPlanHash("some other plan\n", "spec v2")
	if err := deliver("assess-wrong-hash", claim.AssessmentAttemptID, staleCandidate); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an assessment of another candidate was accepted: %v", err)
	}
	// A base revision this proposal was not written against.
	staleBase := good
	staleBase.BaseRevision = claim.BaseRevision + 1
	if err := deliver("assess-wrong-base", claim.AssessmentAttemptID, staleBase); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an assessment of another base revision was accepted: %v", err)
	}
	// Work this item never carried.
	unobserved := good
	unobserved.AffectedEffectID = saved.ID + "-9"
	if err := deliver("assess-unobserved", claim.AssessmentAttemptID, unobserved); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an assessment naming an unobserved effect was accepted: %v", err)
	}
	// Every refusal left the claim exactly where it was.
	if p := f.current().Proposals[0]; p.AssessmentState != "assessing" || p.Assessment != nil || p.Version != claim.Version {
		t.Fatalf("a refused result moved the proposal: %+v", p)
	}

	if err := deliver("assess-ok", claim.AssessmentAttemptID, good); err != nil {
		t.Fatalf("the matching assessment was refused: %v", err)
	}
	recorded := f.current().Proposals[0]
	if recorded.AssessmentState != "assessed" || recorded.Assessment == nil || recorded.Assessment.Classification != "routine_clarification" {
		t.Fatalf("the assessment was not recorded: %+v", recorded)
	}
}

// TestFactoryRevisionAssessmentLostResponseUncertain checks transport loss: the
// attempt is journalled uncertain, nothing requeues it, and only an explicit
// retry starts a new attempt — which the planner is then asked about.
func TestFactoryRevisionAssessmentLostResponseUncertain(t *testing.T) {
	f := newRevisionFixture(t)
	planner := &fakeRevisionPlanner{available: true, store: f.store, itemID: f.item.ID}
	planner.assess = func(RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		return FactoryRevisionAssessment{}, errors.New("planner endpoint went away")
	}
	f.ctl.setRevisionPlanner(planner)

	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	intent := saved.Proposals[0]
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	lost := f.current().Proposals[0]
	if lost.AssessmentState != "uncertain" || lost.AssessmentAttemptID != intent.AssessmentAttemptID || lost.Assessment != nil {
		t.Fatalf("a lost dispatch = %+v, want uncertain on the same attempt", lost)
	}
	// The uncertainty is not requeued by the loop.
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if planner.callCount() != 1 {
		t.Fatalf("planner calls = %d, want no automatic retry", planner.callCount())
	}
	if again := f.current().Proposals[0]; again.AssessmentState != "uncertain" || again.Version != lost.Version {
		t.Fatalf("the loop retried a lost attempt: %+v", again)
	}

	// An explicit retry retires the attempt and journals a new one.
	retrying := f.current()
	retried, err := f.command(FactoryCommand{RequestID: "retry-1", Kind: "revision_assess_retry", Revision: &FactoryRevisionCommand{
		ProposalID: retrying.Proposals[0].ID, ProposalVersion: retrying.Proposals[0].Version,
		AttemptID: retrying.Proposals[0].AssessmentAttemptID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	next := retried.Proposals[0]
	if next.AssessmentState != "pending" || next.AssessmentAttemptID == intent.AssessmentAttemptID {
		t.Fatalf("the retry did not name a new attempt: %+v", next)
	}
	if next.AssessmentAttemptID != factoryRevisionNextAssessmentAttemptID(intent.AssessmentAttemptID) {
		t.Fatalf("retried attempt = %q, want the bumped one", next.AssessmentAttemptID)
	}
	// A result naming the retired attempt cannot land on the new one.
	if _, err := f.command(FactoryCommand{RequestID: "late-result", Kind: "revision_assess_result", Revision: &FactoryRevisionCommand{
		ProposalID: next.ID, ProposalVersion: next.Version, AttemptID: intent.AssessmentAttemptID,
	}}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a result for the retired attempt was accepted: %v", err)
	}

	// The retried attempt is dispatched like any other, and recorded under the
	// attempt the journal now holds.
	planner.mu.Lock()
	planner.assess = func(req RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
		return judgementFor(req, "routine_clarification", "amend", "wait_safe"), nil
	}
	planner.mu.Unlock()
	if err := f.ctl.assessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	assessed := f.current().Proposals[0]
	if assessed.AssessmentState != "assessed" || assessed.AssessmentAttemptID != next.AssessmentAttemptID || assessed.Assessment == nil {
		t.Fatalf("the retried attempt was not recorded: %+v", assessed)
	}
	if planner.callCount() != 2 {
		t.Fatalf("planner calls = %d, want the retried dispatch", planner.callCount())
	}
}
