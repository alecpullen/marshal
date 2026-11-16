package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// revisionFakeEffects is the scripted effect adapter these tests drive. It
// performs no git, forge or runtime work: a test states the observation the
// safe boundary is judged against and, for the one effect it dispatches, the
// outcome the adapter would have confirmed.
type revisionFakeEffects struct {
	mu       sync.Mutex
	obs      FactoryObservation
	obsErr   error
	execute  func(FactoryItem, FactoryEffect) (FactoryOutcome, error)
	executed []string
}

func (e *revisionFakeEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.obs, e.obsErr
}

func (e *revisionFakeEffects) Execute(_ context.Context, i FactoryItem, fx FactoryEffect) (FactoryOutcome, error) {
	e.mu.Lock()
	e.executed = append(e.executed, fx.Kind)
	run := e.execute
	e.mu.Unlock()
	if run != nil {
		return run(i, fx)
	}
	return FactoryOutcome{EffectID: fx.ID, Confirmed: true}, nil
}

// executedEffects is the effects this adapter was asked to dispatch.
func (e *revisionFakeEffects) executedEffects() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executed...)
}

// revisionIdleObservation is the whole conjunction the scheduler and the
// activation gate require, with the item observed as its own reviewed head.
func revisionIdleObservation(i FactoryItem) FactoryObservation {
	obs := FactoryObservation{
		RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true,
		HeadSHA: "head", TreeSHA: "tree", BaseSHA: "base", RemoteHead: "head",
		CI: &FactoryCI{HeadSHA: "head", Complete: true},
	}
	if len(i.PRs) > 0 {
		pr := i.PRs[len(i.PRs)-1]
		obs.PR = &pr
	}
	return obs
}

// newRevisionEffectsFixture is the journal fixture with a scripted effect
// adapter installed, so the activation path can be driven without a fleet.
func newRevisionEffectsFixture(t *testing.T) (*revisionFixture, *revisionFakeEffects) {
	t.Helper()
	f := newRevisionFixture(t)
	fx := &revisionFakeEffects{obs: revisionIdleObservation(f.current())}
	f.ctl.effects = fx
	return f, fx
}

// revisionState moves the approved fixture item into one state in a single
// journal append, which is how the seeded shapes below are built.
func revisionState(t *testing.T, f *revisionFixture, tag string, mutate func(*FactoryItem) error) FactoryItem {
	t.Helper()
	current := f.current()
	saved, err := f.store.Update(current.ID, "revision-fixture-"+tag+"-"+fmt.Sprint(current.Version), current.Version,
		json.RawMessage(`{"kind":"revision_fixture","tag":"`+tag+`"}`), mutate, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed %s: %v", tag, err)
	}
	return saved
}

// revisionImplementingItem seeds an item mid-implementation: one approving
// round whose implement effect settled, which is the shape an authorization is
// preserved through.
func revisionImplementingItem(t *testing.T, f *revisionFixture) FactoryItem {
	t.Helper()
	return revisionState(t, f, "implementing", func(dst *FactoryItem) error {
		active := dst.Approvals[len(dst.Approvals)-1]
		dst.State = "implementing"
		dst.BaseSHA = "base"
		dst.Rounds = []FactoryRound{{Number: 1, ApprovalRevision: active.Revision, HeadSHA: "head", BaseSHA: "base"}}
		dst.Effects = []FactoryEffect{{ID: dst.ID + "-1", Kind: "implement", WorkerRole: "implementer", Status: "completed", ApprovalRevision: active.Revision, Round: 1}}
		return nil
	})
}

// revisionReadyItem seeds a ready item whose PR is a real, non-draft pull
// request, which is the state a revision transition has to return to draft.
func revisionReadyItem(t *testing.T, f *revisionFixture, rounds int) FactoryItem {
	t.Helper()
	return revisionState(t, f, "ready", func(dst *FactoryItem) error {
		active := dst.Approvals[len(dst.Approvals)-1]
		dst.State = "ready"
		dst.Branch = "marshal/factory/" + dst.ID
		dst.BaseSHA = "base"
		dst.PRs = []FactoryPR{{
			Number: 1, NodeID: "node", HeadRef: dst.Branch, BaseRef: active.Config.TargetBranch,
			State: "open", Draft: false, HeadSHA: "head", BaseSHA: "base",
		}}
		dst.Rounds = nil
		for n := 1; n <= rounds; n++ {
			dst.Rounds = append(dst.Rounds, FactoryRound{Number: n, ApprovalRevision: active.Revision, HeadSHA: "head", BaseSHA: "base"})
		}
		// The readiness the old revision earned is a completed set_ready
		// effect; the next derivation must therefore measure fresh work.
		dst.Effects = []FactoryEffect{{
			ID: dst.ID + "-ready", Kind: "set_ready", Status: "completed", ApprovalRevision: active.Revision,
			Outcome: &FactoryOutcome{EffectID: dst.ID + "-ready", Confirmed: true},
		}}
		return nil
	})
}

// authorizeCommand names one proposal's exact candidate for authorization.
func authorizeCommand(p FactoryRevisionProposal, routing, strategy string) FactoryCommand {
	return FactoryCommand{
		RequestID: "authorize-" + p.ID,
		Kind:      "revision_authorize",
		Revision: &FactoryRevisionCommand{
			ProposalID: p.ID, ProposalVersion: p.Version,
			BaseRevision: p.BaseRevision, BaseHash: p.BaseHash,
			CandidateHash: p.CandidateHash,
			Routing:       routing, Strategy: strategy,
		},
	}
}

// TestFactoryRevisionAuthorizeStaleRejected checks authorization is bound to
// the exact proposal, the exact candidate and the approval that is active now:
// every stale or mismatched name conflicts, a second authorization is refused,
// and a missing choice is an input error rather than a guess.
func TestFactoryRevisionAuthorizeStaleRejected(t *testing.T) {
	f := newRevisionFixture(t)
	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	p := saved.Proposals[0]

	attempt := func(requestID string, mutate func(*FactoryRevisionCommand)) error {
		t.Helper()
		cmd := authorizeCommand(p, "amend", "wait_safe")
		cmd.RequestID = requestID
		if mutate != nil {
			mutate(cmd.Revision)
		}
		_, err := f.command(cmd)
		return err
	}

	if err := attempt("stale-version", func(r *FactoryRevisionCommand) { r.ProposalVersion = p.Version + 1 }); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a stale proposal version was authorized: %v", err)
	}
	if err := attempt("stale-candidate", func(r *FactoryRevisionCommand) {
		r.CandidateHash = FactoryPlanHash("another plan\n", "spec v2")
	}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("another candidate was authorized: %v", err)
	}
	if err := attempt("stale-bytes", func(r *FactoryRevisionCommand) {
		r.Plan = "another plan\n"
	}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("candidate bytes the proposal does not carry were authorized: %v", err)
	}
	if err := attempt("stale-base", func(r *FactoryRevisionCommand) { r.BaseHash = FactoryPlanHash("other", "other") }); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a stale base was authorized: %v", err)
	}
	if err := attempt("no-routing", func(r *FactoryRevisionCommand) { r.Routing = "" }); !errors.Is(err, errFactoryInput) {
		t.Fatalf("an authorization without routing was accepted: %v", err)
	}
	if err := attempt("no-strategy", func(r *FactoryRevisionCommand) { r.Strategy = "" }); !errors.Is(err, errFactoryInput) {
		t.Fatalf("an authorization without a strategy was accepted without an assessment: %v", err)
	}
	if got := f.current(); len(got.Proposals) != 1 || got.Proposals[0].Status != "proposed" || got.Approvals[0].Revision != 1 {
		t.Fatalf("a refused authorization moved the journal: %+v", got.Proposals[0])
	}

	authorized, err := f.command(authorizeCommand(p, "amend", "wait_safe"))
	if err != nil {
		t.Fatal(err)
	}
	recorded := authorized.Proposals[0]
	if recorded.Status != "authorized" || recorded.Routing != "amend" || recorded.AuthorizedBy != "studio" || recorded.Delivery == nil || recorded.Delivery.Strategy != "wait_safe" {
		t.Fatalf("authorized proposal = %+v", recorded)
	}
	if len(authorized.Approvals) != 1 || authorized.Approvals[0].Plan != " plan\n" {
		t.Fatalf("authorization appended an approval while the old effect could run: %+v", authorized.Approvals)
	}

	// A second authorization of one proposal is refused, whatever it names.
	second := authorizeCommand(authorized.Proposals[0], "amend", "wait_safe")
	second.RequestID = "authorize-again"
	if _, err := f.command(second); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a second authorization was accepted: %v", err)
	}
}

// TestFactoryRevisionAuthorizeStaleBaseRejected checks an intervening approval
// makes the proposal's base stale: it needs a fresh proposal, not a silent
// rebase onto the newer revision.
func TestFactoryRevisionAuthorizeStaleBaseRejected(t *testing.T) {
	f := newRevisionFixture(t)
	saved, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	p := saved.Proposals[0]
	grown := revisionState(t, f, "second-approval", func(dst *FactoryItem) error {
		dst.Approvals = append(dst.Approvals, FactoryApproval{
			Revision: 2, Plan: "plan v9\n", Spec: "spec v9",
			PlanHash: FactoryPlanHash("plan v9\n", "spec v9"),
			Config:   dst.Draft.Config, ApprovedAt: time.Now().UTC(),
		})
		return nil
	})
	if grown.Approvals[1].Revision != 2 {
		t.Fatalf("seed approvals = %+v", grown.Approvals)
	}
	if _, err := f.command(authorizeCommand(p, "amend", "wait_safe")); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a proposal written against a superseded approval was authorized: %v", err)
	}
}

// TestFactoryRevisionOldTurnKeepsApproval checks the contract's central rule:
// while an old factory effect is live, authorization records the decision and
// changes no approval at all.
func TestFactoryRevisionOldTurnKeepsApproval(t *testing.T) {
	f, fx := newRevisionEffectsFixture(t)
	implementing := revisionImplementingItem(t, f)
	// The old turn is still running: its effect is dispatched.
	live := revisionState(t, f, "live-effect", func(dst *FactoryItem) error {
		dst.Effects = append(dst.Effects, FactoryEffect{ID: dst.ID + "-2", Kind: "publish", Status: "dispatched", ApprovalRevision: 1})
		return nil
	})
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "interrupt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(authorized.Approvals) != 1 || authorized.Approvals[0].Revision != 1 {
		t.Fatalf("authorization changed the active approval: %+v", authorized.Approvals)
	}
	if authorized.Proposals[0].Delivery.Strategy != "interrupt" {
		t.Fatalf("the chosen strategy was not recorded: %+v", authorized.Proposals[0].Delivery)
	}

	// The gate refuses to activate while the old effect has not settled, even
	// with an otherwise perfectly idle observation.
	fx.obs = revisionIdleObservation(live)
	if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); len(got.Approvals) != 1 || got.Proposals[0].Status != "authorized" {
		t.Fatalf("an unsettled effect was activated over: approvals=%+v status=%s", got.Approvals, got.Proposals[0].Status)
	}
	if got := f.current(); len(got.Effects) != 2 || got.Effects[1].Status != "dispatched" {
		t.Fatalf("the in-flight effect lost its identity: %+v", got.Effects)
	}

	// Once it settles and the checkout is idle, the revision activates with
	// exactly one new approval carrying the candidate and the prior config.
	settled := revisionState(t, f, "settled-effect", func(dst *FactoryItem) error {
		dst.Effects[len(dst.Effects)-1].Status = "completed"
		dst.Effects[len(dst.Effects)-1].Outcome = &FactoryOutcome{EffectID: dst.Effects[len(dst.Effects)-1].ID, Confirmed: true, HeadSHA: "head", TreeSHA: "tree"}
		return nil
	})
	fx.obs = revisionIdleObservation(settled)
	if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	activated := f.current()
	if len(activated.Approvals) != 2 {
		t.Fatalf("approvals = %+v, want exactly one new approval", activated.Approvals)
	}
	prior := activated.Approvals[0]
	fresh := activated.Approvals[1]
	if fresh.Revision != 2 || fresh.Plan != "plan v2\n" || fresh.Spec != "spec v2" || fresh.PlanHash != activated.Proposals[0].CandidateHash {
		t.Fatalf("the activated approval is not the exact candidate: %+v", fresh)
	}
	if fresh.Config.RepoID != prior.Config.RepoID || fresh.Config.MaxRounds != prior.Config.MaxRounds || fresh.Config.Native != prior.Config.Native {
		t.Fatalf("the activated approval did not carry the prior config: %+v", fresh.Config)
	}
	if activated.Proposals[0].Status != "activated" {
		t.Fatalf("proposal status = %q, want activated", activated.Proposals[0].Status)
	}
	if got := implementing.ID; activated.Effects[0].ID != got+"-1" || activated.Effects[0].ApprovalRevision != 1 {
		t.Fatalf("the old effect was rewritten: %+v", activated.Effects[0])
	}
	if !revisionHistoryHas(activated, "revision_activated", activated.Proposals[0].ID) {
		t.Fatalf("activation was not journalled with its source: %+v", activated.History)
	}
}

// revisionHistoryHas reports whether one journaled entry of kind exists for
// effectID, which is how these tests read the source and activation records.
func revisionHistoryHas(i FactoryItem, kind, effectID string) bool {
	for _, h := range i.History {
		if h.Kind == kind && h.EffectID == effectID {
			return true
		}
	}
	return false
}

// TestFactoryRevisionActivateOnlyWhenIdle checks every part of the safe
// boundary is required: each missing observation keeps the new approval
// unauthored, and only the full conjunction activates it.
func TestFactoryRevisionActivateOnlyWhenIdle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*FactoryObservation)
	}{
		{"runtime unknown", func(o *FactoryObservation) { o.RuntimeKnown = false }},
		{"turn busy", func(o *FactoryObservation) { o.TurnIdle = false }},
		{"active work unknown", func(o *FactoryObservation) { o.ActiveWorkKnown = false }},
		{"active work busy", func(o *FactoryObservation) { o.WorkIdle = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, fx := newRevisionEffectsFixture(t)
			implementing := revisionImplementingItem(t, f)
			proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe")); err != nil {
				t.Fatal(err)
			}
			obs := revisionIdleObservation(implementing)
			tc.mutate(&obs)
			fx.obs = obs
			if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.current(); len(got.Approvals) != 1 {
				t.Fatalf("%s activated: approvals=%+v", tc.name, got.Approvals)
			}
			fx.obs = revisionIdleObservation(implementing)
			if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := f.current(); len(got.Approvals) != 2 {
				t.Fatalf("the full idle conjunction did not activate: %+v", got.Approvals)
			}
		})
	}

	// An observation error is not an idle observation.
	f, fx := newRevisionEffectsFixture(t)
	implementing := revisionImplementingItem(t, f)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe")); err != nil {
		t.Fatal(err)
	}
	fx.obs, fx.obsErr = revisionIdleObservation(implementing), errors.New("runtime unavailable")
	if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); len(got.Approvals) != 1 {
		t.Fatalf("an unobserved runtime activated the revision: %+v", got.Approvals)
	}
}

// TestFactoryRevisionReadyPRReturnsToDraft checks a ready item's PR is returned
// to draft through the existing journaled, identity-checked set_draft effect,
// tagged as a revision transition, before the new approval is appended.
func TestFactoryRevisionReadyPRReturnsToDraft(t *testing.T) {
	f, fx := newRevisionEffectsFixture(t)
	ready := revisionReadyItem(t, f, 1)
	if ready.PRs[0].Draft {
		t.Fatal("the seeded PR is already a draft")
	}
	fx.obs = revisionIdleObservation(ready)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe"))
	if err != nil {
		t.Fatal(err)
	}
	proposalID := authorized.Proposals[0].ID

	// The first pass journals the transition rather than activating.
	fx.execute = func(i FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
		pr := i.PRs[len(i.PRs)-1]
		pr.Draft = true
		return FactoryOutcome{EffectID: e.ID, Confirmed: true, PR: &pr}, nil
	}
	f.ctl.admitAfterRecovery()
	if err := f.ctl.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	jumped := f.current()
	if len(jumped.Approvals) != 1 {
		t.Fatalf("a ready PR was activated before it was returned to draft: %+v", jumped.Approvals)
	}
	transition := jumped.Effects[len(jumped.Effects)-1]
	if len(jumped.Effects) < 2 || transition.Kind != "set_draft" {
		t.Fatalf("no set_draft transition effect was journalled: %+v", jumped.Effects)
	}
	if !revisionHistoryHas(jumped, "revision_transition", transition.ID) {
		t.Fatalf("the transition was not tagged: %+v", jumped.History)
	}
	if transition.Status != "completed" {
		t.Fatalf("the transition effect did not complete: %+v", transition)
	}
	if jumped.PRs[0].Draft != true {
		t.Fatalf("the PR was not confirmed as a draft: %+v", jumped.PRs[0])
	}
	if jumped.Decision != nil || jumped.State == "needs_input" {
		// A revision transition is not the external-conflict redraft, which
		// records a conflict decision.
		t.Fatalf("the revision transition took the external-conflict path: decision=%+v state=%s", jumped.Decision, jumped.State)
	}
	if !factoryRevisionTransitionConfirmed(jumped, proposalID) {
		t.Fatal("the confirmed transition was not recorded as such")
	}

	// Only after the confirmation does the activation append the approval.
	if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	activated := f.current()
	if len(activated.Approvals) != 2 || activated.Approvals[1].Plan != "plan v2\n" {
		t.Fatalf("the transitioned revision was not activated: %+v", activated.Approvals)
	}
	if activated.Proposals[0].Status != "activated" {
		t.Fatalf("proposal = %+v", activated.Proposals[0])
	}
}

// TestFactoryRevisionReadyDraftFailureUncertain checks a transition whose
// outcome cannot be confirmed leaves the revision pending: the PR is not
// claimed a draft and no approval is appended.
func TestFactoryRevisionReadyDraftFailureUncertain(t *testing.T) {
	f, fx := newRevisionEffectsFixture(t)
	fx.obs = revisionIdleObservation(revisionReadyItem(t, f, 1))
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe")); err != nil {
		t.Fatal(err)
	}
	fx.execute = func(FactoryItem, FactoryEffect) (FactoryOutcome, error) {
		return FactoryOutcome{}, fmt.Errorf("%w: PR mutation acknowledgement lost", ErrFactoryUncertain)
	}
	f.ctl.admitAfterRecovery()
	if err := f.ctl.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := f.current()
	if len(failed.Approvals) != 1 {
		t.Fatalf("an unconfirmed draft appended an approval: %+v", failed.Approvals)
	}
	if failed.Proposals[0].Status != "authorized" {
		t.Fatalf("proposal status = %q, want it left pending", failed.Proposals[0].Status)
	}
	if len(failed.Effects) < 2 || failed.Effects[len(failed.Effects)-1].Status != "uncertain" {
		t.Fatalf("the transition outcome was not recorded uncertain: %+v", failed.Effects)
	}
	if failed.PRs[0].Draft {
		t.Fatal("the PR is claimed a draft from an unconfirmed mutation")
	}
	// Nothing retries automatically from a different path either.
	if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.current(); len(got.Approvals) != 1 || got.Proposals[0].Status != "authorized" {
		t.Fatalf("the unconfirmed transition activated: %+v", got.Approvals)
	}
}

// TestFactoryRevisionExistingAllowanceEnforced checks an amendment adds no
// round allowance: fresh work is measured against the grants that already
// exist, and an exhausted allowance keeps the run's existing limit decision.
func TestFactoryRevisionExistingAllowanceEnforced(t *testing.T) {
	f := newRevisionFixture(t)
	revisionReadyItem(t, f, factoryAllowance(f.current()))
	granted := len(f.current().Grants)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe"))
	if err != nil {
		t.Fatal(err)
	}
	if len(authorized.Grants) != granted {
		t.Fatalf("authorization granted rounds: %+v", authorized.Grants)
	}
	// Activate directly: the round allowance is the question here, not the
	// ready transition.
	activated, err := f.ctl.activateRevision(authorized, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(activated.Grants) != granted {
		t.Fatalf("activation granted rounds: %+v", activated.Grants)
	}
	next, effect, err := AdvanceFactory(activated, revisionIdleObservation(activated), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if effect != nil {
		t.Fatalf("exhausted allowance still dispatched %s", effect.Kind)
	}
	if next.Decision == nil || next.Decision.Kind != "exhausted" {
		t.Fatalf("the existing limit decision was not taken: %+v", next.Decision)
	}
}

// TestFactoryRevisionFollowUpIdempotentRecovery checks the durable follow-up
// intent: one confirmation creates one draft linked to its source, a replay
// creates nothing, and a lost association is reconciled after a restart without
// a duplicate.
func TestFactoryRevisionFollowUpIdempotentRecovery(t *testing.T) {
	f := newRevisionFixture(t)
	source := f.current()
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	followUp := FactoryCreate{
		Title: "follow-up title", Plan: "follow-up plan\n", Spec: "follow-up spec",
		Config: FactoryConfig{RepoID: source.RepoID, TargetBranch: "main", Native: true, NoRequiredCIAcknowledged: true},
	}
	authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "follow_up", "hold"))
	if err != nil {
		t.Fatal(err)
	}
	if authorized.Proposals[0].Status != "follow_up_pending" {
		t.Fatalf("follow-up authorization = %+v", authorized.Proposals[0])
	}
	// A follow-up cannot be confirmed as an amendment, and vice versa.
	if _, err := f.command(authorizeCommand(authorized.Proposals[0], "amend", "hold")); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an already authorized proposal was re-authorized: %v", err)
	}

	confirm := FactoryCommand{
		RequestID: "confirm-follow-up",
		Kind:      "revision_follow_up_confirm",
		Revision: &FactoryRevisionCommand{
			ProposalID: authorized.Proposals[0].ID, ProposalVersion: authorized.Proposals[0].Version,
			CandidateHash: authorized.Proposals[0].CandidateHash, FollowUp: &followUp,
		},
	}
	// The request identity a replay must repeat is the item version and the
	// command it was written against.
	confirm.ExpectedVersion = f.current().Version
	confirmed, err := f.command(confirm)
	if err != nil {
		t.Fatal(err)
	}
	proposal := confirmed.Proposals[0]
	if proposal.Status != "followed_up" || proposal.FollowUpItemID == "" {
		t.Fatalf("the follow-up was not associated: %+v", proposal)
	}
	if proposal.FollowUpRequestID != factoryRevisionFollowUpRequestID(source.ID, proposal.ID) {
		t.Fatalf("follow-upRequestId = %q, want the deterministic identity", proposal.FollowUpRequestID)
	}
	items := f.store.List()
	if len(items) != 2 {
		t.Fatalf("items = %d, want the source and one follow-up", len(items))
	}
	created := followUpItem(t, items, proposal.FollowUpItemID)
	if created.State != "draft" || len(created.Approvals) != 0 || created.Decision != nil {
		t.Fatalf("the follow-up is not an unapproved draft: %+v", created)
	}
	if created.Draft.Plan != "follow-up plan\n" || created.Draft.Spec != "follow-up spec" || created.Draft.Title != "follow-up title" {
		t.Fatalf("the follow-up content is not the confirmed one: %+v", created.Draft)
	}
	if !revisionSourceNames(created, source.ID, proposal.ID) {
		t.Fatalf("the created draft does not name its source: %+v", created.History)
	}
	// The source keeps its own revision.
	if len(confirmed.Approvals) != 1 || confirmed.Approvals[0].Plan != " plan\n" {
		t.Fatalf("a follow-up changed the source's revision: %+v", confirmed.Approvals)
	}

	// A replayed confirmation creates no second draft.
	replayed, err := f.ctl.Command(context.Background(), source.ID, confirm)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.store.List()) != 2 {
		t.Fatalf("a replayed confirmation created a duplicate: %d items", len(f.store.List()))
	}
	if replayed.Proposals[0].FollowUpItemID != proposal.FollowUpItemID {
		t.Fatalf("the replay lost the association: %+v", replayed.Proposals[0])
	}

	// The transport preflights a follow-up as the normal draft it becomes.
	api := NewFactoryAPI(f.store, f.ctl, nil)
	if _, err := api.Command(context.Background(), source.ID, FactoryCommand{
		RequestID: "preflight-required", Kind: "revision_follow_up_confirm",
		ExpectedVersion: f.current().Version,
		Revision:        &FactoryRevisionCommand{ProposalID: proposal.ID, ProposalVersion: proposal.Version, FollowUp: &followUp},
	}); !errors.Is(err, ErrFactoryUnavailable) {
		t.Fatalf("a follow-up without a fleet preflight = %v", err)
	}

	// A crash between the create and its association: the draft is durable and
	// the link is lost. A restarted controller reconciles it without creating
	// a second item.
	lost := f.current()
	if _, err := f.store.Update(lost.ID, "lose-association", lost.Version, json.RawMessage(`{"kind":"fixture","lost":"association"}`), func(dst *FactoryItem) error {
		dst.Proposals[0].FollowUpItemID = ""
		dst.Proposals[0].Status = "follow_up_pending"
		dst.Proposals[0].Version++
		dst.Proposals[0].UpdatedAt = f.ctl.now().UTC()
		return nil
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewFactoryController(reopened, nil, f.ctl.now)
	if err := restarted.reconcileRevisionFollowUps(); err != nil {
		t.Fatal(err)
	}
	if n := len(reopened.List()); n != 2 {
		t.Fatalf("recovery created a duplicate: %d items", n)
	}
	recovered, err := reopened.Get(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Proposals[0].Status != "followed_up" || recovered.Proposals[0].FollowUpItemID != proposal.FollowUpItemID {
		t.Fatalf("the lost association was not restored: %+v", recovered.Proposals[0])
	}
	restored := countRevisionEvents(recovered, "revision_follow_up_linked")
	// A second pass lands nothing twice.
	if err := restarted.reconcileRevisionFollowUps(); err != nil {
		t.Fatal(err)
	}
	again, err := reopened.Get(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRevisionEvents(again, "revision_follow_up_linked"); n != restored {
		t.Fatalf("a second reconciliation wrote another association: %d events, want the %d it found", n, restored)
	}
}

// followUpItem finds one item in a listing.
func followUpItem(t *testing.T, items []FactoryItem, id string) FactoryItem {
	t.Helper()
	for _, i := range items {
		if i.ID == id {
			return i
		}
	}
	t.Fatalf("no item %q in %d items", id, len(items))
	return FactoryItem{}
}

// revisionSourceNames reports whether the created draft names the source item
// and the proposal it came from.
func revisionSourceNames(i FactoryItem, sourceID, proposalID string) bool {
	for _, h := range i.History {
		if h.Kind == "revision_source" && h.EffectID == proposalID && h.Message == sourceID {
			return true
		}
	}
	return false
}

// countRevisionEvents counts the journaled entries of one kind.
func countRevisionEvents(i FactoryItem, kind string) int {
	n := 0
	for _, h := range i.History {
		if h.Kind == kind {
			n++
		}
	}
	return n
}

// TestFactoryRevisionUserChoiceOverridesRecommendation checks the user's
// explicit choice wins while the planner's recommendation stays recorded as
// the evidence it is.
func TestFactoryRevisionUserChoiceOverridesRecommendation(t *testing.T) {
	f := newRevisionFixture(t)
	if _, err := f.propose("rev-1", "plan v2\n", "spec v2"); err != nil {
		t.Fatal(err)
	}
	// Record an assessment that recommends the other routing and strategy.
	assessed := revisionState(t, f, "assessed", func(dst *FactoryItem) error {
		p := &dst.Proposals[0]
		p.Assessment = &FactoryRevisionAssessment{
			Classification: "scope_change", Reason: "it changes the scope",
			Recommendation: "follow_up", Strategy: "hold", AssessorID: "planner-fixture",
			CandidateHash: p.CandidateHash, BaseRevision: p.BaseRevision,
		}
		p.AssessmentState = "assessed"
		p.Routing = "follow_up"
		p.Status = "awaiting_authorization"
		p.Version++
		p.UpdatedAt = f.ctl.now().UTC()
		return nil
	})
	p := assessed.Proposals[0]

	chosen, err := f.command(FactoryCommand{
		RequestID: "delivery-choice", Kind: "revision_delivery_choice",
		Revision: &FactoryRevisionCommand{
			ProposalID: p.ID, ProposalVersion: p.Version,
			CandidateHash: p.CandidateHash, Routing: "amend", Strategy: "wait_safe",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Proposals[0].Routing != "amend" || chosen.Proposals[0].Delivery == nil || chosen.Proposals[0].Delivery.Strategy != "wait_safe" {
		t.Fatalf("the user's choice was not recorded: %+v", chosen.Proposals[0])
	}
	if a := chosen.Proposals[0].Assessment; a == nil || a.Recommendation != "follow_up" || a.Strategy != "hold" {
		t.Fatalf("the recommendation was rewritten: %+v", a)
	}

	// Authorizing explicitly follows the recorded choice, not the
	// recommendation.
	authorized, err := f.command(authorizeCommand(chosen.Proposals[0], "amend", "wait_safe"))
	if err != nil {
		t.Fatal(err)
	}
	if authorized.Proposals[0].Status != "authorized" || authorized.Proposals[0].Routing != "amend" {
		t.Fatalf("authorized = %+v", authorized.Proposals[0])
	}
	if !factoryRevisionActivationPendingBool(authorized) {
		t.Fatalf("the overridden routing is not an activation: %+v", authorized.Proposals[0])
	}
	// A delivery choice cannot move an authorized routing behind the journal.
	if _, err := f.command(FactoryCommand{
		RequestID: "re-route", Kind: "revision_delivery_choice",
		Revision: &FactoryRevisionCommand{ProposalID: p.ID, ProposalVersion: authorized.Proposals[0].Version, Routing: "follow_up", Strategy: "hold"},
	}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an authorized routing was changed by a delivery choice: %v", err)
	}
}

// factoryRevisionActivationPendingBool is the pending-activation predicate as a
// boolean, for tests that state it in an assertion.
func factoryRevisionActivationPendingBool(i FactoryItem) bool {
	_, ok := factoryRevisionActivationPending(i)
	return ok
}

// TestFactoryRevisionRoutineAuthorizationAudited checks a delegated routine
// authorization is accepted under the principal the caller established, is
// journalled with its provenance, and activates at the boundary naming who
// authorized it.
func TestFactoryRevisionRoutineAuthorizationAudited(t *testing.T) {
	f, fx := newRevisionEffectsFixture(t)
	implementing := revisionImplementingItem(t, f)
	fx.obs = revisionIdleObservation(implementing)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	p := proposed.Proposals[0]
	principal := factoryPrincipal{ClientID: "planner-c1", ProjectID: "p1", Action: factoryCapabilityAssessPlanChanges, Authority: factoryAuthorityRoutineRevision}

	claimed, err := f.ctl.CommandAuthorized(context.Background(), proposed.ID, FactoryCommand{
		RequestID: "claim", Kind: "revision_assess_claim", ExpectedVersion: proposed.Version,
		Revision: &FactoryRevisionCommand{ProposalID: p.ID, ProposalVersion: p.Version, AttemptID: p.AssessmentAttemptID},
	}, principal)
	if err != nil {
		t.Fatal(err)
	}
	assessment := FactoryRevisionAssessment{
		Classification: "routine_clarification", Reason: "it only restates a step",
		Recommendation: "amend", Strategy: "wait_safe", AssessorID: "planner-c1",
		CandidateHash: p.CandidateHash, BaseRevision: p.BaseRevision,
	}
	assessed, err := f.ctl.CommandAuthorized(context.Background(), claimed.ID, FactoryCommand{
		RequestID: "assess", Kind: "revision_assess_result", ExpectedVersion: claimed.Version,
		Revision: &FactoryRevisionCommand{
			ProposalID: p.ID, ProposalVersion: claimed.Proposals[0].Version, AttemptID: p.AssessmentAttemptID,
			CandidateHash: assessment.CandidateHash, BaseRevision: assessment.BaseRevision, Assessment: &assessment,
		},
	}, principal)
	if err != nil {
		t.Fatal(err)
	}
	recorded := assessed.Proposals[0]
	if recorded.Status != "authorized" || recorded.Authority != factoryAuthorityRoutineRevision || recorded.AuthorizedBy != "planner-c1" {
		t.Fatalf("routine authorization = %+v", recorded)
	}
	// The audit entry names the client that carried the delegation.
	var audited bool
	for _, h := range assessed.History {
		if h.Kind == "revision_authorized" && h.Provenance != nil && h.Provenance.ClientID == "planner-c1" && h.Provenance.Action == factoryCapabilityAssessPlanChanges {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("the routine authorization was not audited: %+v", assessed.History)
	}

	// The activation names the authorizer, so the appended approval can be
	// traced to the delegation that admitted it.
	if err := f.ctl.activateAuthorizedRevisions(context.Background()); err != nil {
		t.Fatal(err)
	}
	activated := f.current()
	if len(activated.Approvals) != 2 {
		t.Fatalf("a routine authorization did not activate: %+v", activated.Approvals)
	}
	if got := activated.Approvals[1].Plan; got != "plan v2\n" {
		t.Fatalf("activated plan = %q", got)
	}
	var namesAuthorizer bool
	for _, h := range activated.History {
		if h.Kind == "revision_activated" && strings.Contains(h.Message, "planner-c1") {
			namesAuthorizer = true
		}
	}
	if !namesAuthorizer {
		t.Fatalf("activation does not name its authorizer: %+v", activated.History)
	}

	// A client cannot authorize a revision on its own account.
	second := newRevisionFixture(t)
	another, err := second.propose("rev-2", "plan v3\n", "spec v3")
	if err != nil {
		t.Fatal(err)
	}
	cmd := authorizeCommand(another.Proposals[0], "amend", "wait_safe")
	cmd.ExpectedVersion = another.Version
	if _, err := second.ctl.CommandAuthorized(context.Background(), another.ID, cmd, factoryPrincipal{
		ClientID: "c1", ProjectID: "p1", Action: factoryCapabilityApproveAndQueue,
	}); !errors.Is(err, errFactoryDenied) {
		t.Fatalf("a client authorized a revision itself: %v", err)
	}
}

// TestFactoryRevisionSupersedeAfterApprovalRejected checks an authorized
// proposal is frozen until it activates or is decided: it cannot be superseded,
// rejected, re-authorized or replaced by a conflicting retry, while stopping
// the item stays available.
func TestFactoryRevisionSupersedeAfterApprovalRejected(t *testing.T) {
	f, fx := newRevisionEffectsFixture(t)
	implementing := revisionImplementingItem(t, f)
	fx.obs = revisionIdleObservation(implementing)
	proposed, err := f.propose("rev-1", "plan v2\n", "spec v2")
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := f.command(authorizeCommand(proposed.Proposals[0], "amend", "wait_safe"))
	if err != nil {
		t.Fatal(err)
	}
	p := authorized.Proposals[0]

	for _, tc := range []struct {
		name string
		cmd  FactoryCommand
	}{
		{"supersede", FactoryCommand{RequestID: "supersede", Kind: "revision_supersede", Revision: revisionAt(authorized, 0)}},
		{"reject", FactoryCommand{RequestID: "reject", Kind: "revision_reject", Revision: revisionAt(authorized, 0)}},
		{"authorize again", func() FactoryCommand {
			c := authorizeCommand(p, "amend", "wait_safe")
			c.RequestID = "authorize-again"
			return c
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.command(tc.cmd); !errors.Is(err, ErrFactoryConflict) {
				t.Fatalf("%s = %v, want a conflict", tc.name, err)
			}
		})
	}

	// A retry that would replace the plan or move a route is refused while the
	// authorization waits; a plain grant-only retry is not a conflicting
	// change and stays available.
	blocked := revisionState(t, f, "decided", func(dst *FactoryItem) error {
		dst.Decision = &FactoryDecision{ID: "decision-1", Kind: "exhausted", Reason: "round allowance exhausted", CreatedAt: time.Now().UTC()}
		dst.State = "needs_input"
		return nil
	})
	replacement := factoryReq()
	replacement.RequestID = "retry"
	if _, err := f.command(FactoryCommand{
		RequestID: "retry-replace", Kind: "retry", DecisionID: "decision-1",
		AdditionalRounds: 1, Reason: "operator grant", Replacement: &replacement,
	}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("a plan-replacing retry was admitted over an authorized revision: %v", err)
	}
	granted, err := f.command(FactoryCommand{
		RequestID: "retry-grant", Kind: "retry", DecisionID: "decision-1",
		AdditionalRounds: 1, Reason: "operator grant",
	})
	if err != nil {
		t.Fatalf("a grant-only retry was refused: %v", err)
	}
	if len(granted.Grants) != len(blocked.Grants)+1 {
		t.Fatalf("the grant was not recorded: %+v", granted.Grants)
	}
	// Stopping the item stays available while the authorization waits.
	stopped, err := f.command(FactoryCommand{RequestID: "stop", Kind: "stop"})
	if err != nil {
		t.Fatalf("stop was refused: %v", err)
	}
	if stopped.State != "stopping" {
		t.Fatalf("stop did not take effect: %+v", stopped)
	}

	// Once the revision activates, its approval is history: superseding it is
	// refused too.
	activated := newRevisionFixture(t)
	seeded := revisionImplementingItem(t, activated)
	if _, err := activated.command(FactoryCommand{RequestID: "rev-1", Kind: "revision_propose", Revision: &FactoryRevisionCommand{
		BaseRevision: 1, BaseHash: seeded.Approvals[0].PlanHash,
		Plan: "plan v2\n", Spec: "spec v2", CandidateHash: FactoryPlanHash("plan v2\n", "spec v2"),
	}}); err != nil {
		t.Fatal(err)
	}
	proposal := activated.current().Proposals[0]
	if _, err := activated.command(authorizeCommand(proposal, "amend", "wait_safe")); err != nil {
		t.Fatal(err)
	}
	live := activated.current()
	if _, err := activated.ctl.activateRevision(live, 0); err != nil {
		t.Fatal(err)
	}
	final := activated.current()
	if final.Proposals[0].Status != "activated" {
		t.Fatalf("proposal = %+v", final.Proposals[0])
	}
	if _, err := activated.command(FactoryCommand{RequestID: "supersede-activated", Kind: "revision_supersede", Revision: revisionAt(final, 0)}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("an activated proposal was superseded: %v", err)
	}
}
