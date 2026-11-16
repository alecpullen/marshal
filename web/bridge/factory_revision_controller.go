package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file owns the authorization, activation and follow-up half of plan
// revisions: the part that binds an authorization to exact candidate bytes,
// keeps the old approval active while an old effect is still running, moves to
// the new approval only at a verified safe boundary, and creates the linked
// follow-up draft an explicitly confirmed proposal asked for.
//
// Nothing here delivers a revision to a running worker. An authorization
// records the strategy the user chose; handing the direction to a worker and
// observing a receipt is the delivery slice, and this file never claims a
// delivery happened.

// factoryRevisionFollowUpRequestID is the journal identity of the factory draft
// an authorized follow-up creates. It is derived from the source item and the
// proposal, so re-confirming after a lost response names the same draft while a
// different proposal names its own.
func factoryRevisionFollowUpRequestID(itemID, proposalID string) string {
	return "rev-follow-up-" + factoryDigest([]byte(itemID+"\x00"+proposalID))
}

// factoryRevisionProposalByID finds one proposal without a version check, which
// is what the follow-up confirmation and the controller's own passes need: the
// command's expected versions are checked by the journal mutation itself.
func factoryRevisionProposalByID(i FactoryItem, id string) (int, bool) {
	if strings.TrimSpace(id) == "" {
		return -1, false
	}
	for n := range i.Proposals {
		if i.Proposals[n].ID == id {
			return n, true
		}
	}
	return -1, false
}

// factoryRevisionActivationPending finds the one proposal whose authorization
// waits for the safe boundary: an amendment the journal recorded as authorized
// and that has not activated. A follow-up routing is excluded because it never
// appends an approval to this item.
func factoryRevisionActivationPending(i FactoryItem) (int, bool) {
	for n := range i.Proposals {
		p := i.Proposals[n]
		if p.Status == "authorized" && p.Routing == "amend" {
			return n, true
		}
	}
	return -1, false
}

// factoryRevisionTransition names the proposal an effect returns a ready PR to
// draft for. The event is the tag that keeps this path apart from the existing
// external-conflict redraft, which is why no new effect kind exists.
func factoryRevisionTransition(i FactoryItem, effectID string) (string, bool) {
	if strings.TrimSpace(effectID) == "" {
		return "", false
	}
	for n := len(i.History) - 1; n >= 0; n-- {
		h := i.History[n]
		if h.Kind == "revision_transition" && h.EffectID == effectID {
			return h.Message, true
		}
	}
	return "", false
}

// factoryRevisionTransitionConfirmed reports whether the transition journaled
// for one proposal has returned its PR to draft. Only the newest event for the
// proposal counts, so a failed transition is not read as a confirmed one.
func factoryRevisionTransitionConfirmed(i FactoryItem, proposalID string) bool {
	for n := len(i.History) - 1; n >= 0; n-- {
		h := i.History[n]
		if h.Message != proposalID {
			continue
		}
		switch h.Kind {
		case "revision_transition_drafted":
			return true
		case "revision_transition", "revision_transition_failed":
			return false
		}
	}
	return false
}

// factoryRevisionTransitionRecorded reports whether a transition for this
// proposal was already journaled. A transition that failed is not retried
// automatically: the journal keeps the intent and the item stays at the
// boundary until a user resolves it.
func factoryRevisionTransitionRecorded(i FactoryItem, proposalID string) bool {
	for _, h := range i.History {
		switch h.Kind {
		case "revision_transition", "revision_transition_drafted", "revision_transition_failed":
			if h.Message == proposalID {
				return true
			}
		}
	}
	return false
}

// factoryRevisionSourceLinked reports whether the created item already names
// the source it was confirmed from, so a reconciliation never writes it twice.
func factoryRevisionSourceLinked(i FactoryItem, proposalID string) bool {
	for _, h := range i.History {
		if h.Kind == "revision_source" && h.EffectID == proposalID {
			return true
		}
	}
	return false
}

// factoryRevisionAuthorize is the authorization half of an amendment or a
// follow-up. It binds the user's decision to the exact candidate the journal
// already holds and to the approval that is active now, and it changes no
// approval: an in-flight effect keeps the revision it started under.
func factoryApplyRevisionAuthorize(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	if p != nil {
		// Routine authorization is carried by the assessment result the policy
		// lock admits. A client cannot authorize a revision on its own
		// account, and the Studio path is the one identity-free caller.
		return fmt.Errorf("%w: a plan revision is authorized by an authenticated Studio user or by a delegated routine assessment", errFactoryDenied)
	}
	if err := factoryRevisionSavable(*i); err != nil {
		return err
	}
	r := c.Revision
	if r == nil {
		return fmt.Errorf("%w: revision payload required", errFactoryInput)
	}
	n, err := factoryRevisionProposalAt(i, r)
	if err != nil {
		return err
	}
	proposal := &i.Proposals[n]
	switch proposal.Status {
	case "proposed", "awaiting_authorization":
	default:
		return fmt.Errorf("%w: proposal %s is %s and cannot be authorized again", ErrFactoryConflict, proposal.ID, proposal.Status)
	}
	if r.CandidateHash != proposal.CandidateHash || r.BaseRevision != proposal.BaseRevision || r.BaseHash != proposal.BaseHash {
		return fmt.Errorf("%w: the authorization does not name proposal %s's exact candidate and base revision", ErrFactoryConflict, proposal.ID)
	}
	if (r.Plan != "" || r.Spec != "") && (r.Plan != proposal.Plan || r.Spec != proposal.Spec) {
		return fmt.Errorf("%w: the authorization names candidate bytes this proposal does not carry", ErrFactoryConflict)
	}
	if len(i.Approvals) == 0 {
		return fmt.Errorf("%w: the item has no approved revision", ErrFactoryConflict)
	}
	active := i.Approvals[len(i.Approvals)-1]
	if proposal.BaseRevision != active.Revision || proposal.BaseHash != active.PlanHash {
		// An intervening approval makes the proposal's base stale; it needs a
		// fresh proposal rather than a silent rebase.
		return fmt.Errorf("%w: proposal %s was written against revision %d, which is no longer active", ErrFactoryConflict, proposal.ID, proposal.BaseRevision)
	}
	if !factoryRevisionRoutings[r.Routing] {
		return fmt.Errorf("%w: routing must be amend or follow_up", errFactoryInput)
	}
	// The assessment supplies the strategy only when it judged this exact
	// candidate at this exact base; a stale or absent assessment leaves the
	// choice to the user, who must make it explicitly.
	strategy := r.Strategy
	if strategy == "" {
		if a := proposal.Assessment; a != nil && a.CandidateHash == proposal.CandidateHash && a.BaseRevision == proposal.BaseRevision {
			strategy = a.Strategy
		}
	}
	if !factoryRevisionStrategies[strategy] {
		return fmt.Errorf("%w: choose wait_safe, interrupt or hold", errFactoryInput)
	}
	proposal.Routing = r.Routing
	proposal.AuthorizedBy = factoryStudioRevisionActor
	proposal.Delivery = &FactoryRevisionDelivery{Strategy: strategy, State: "pending", CandidateHash: proposal.CandidateHash}
	proposal.Version++
	proposal.UpdatedAt = now
	if r.Routing == "amend" {
		proposal.Status = "authorized"
	} else {
		proposal.Status = "follow_up_pending"
	}
	i.History = append(i.History, FactoryEvent{
		At: now, Kind: "revision_authorized", EffectID: proposal.ID,
		Message: "plan revision authorized for " + r.Routing + " (" + strategy + ")",
	})
	return nil
}

// factoryApplyRevisionDeliveryChoice records the strategy the user chose and,
// before the proposal is authorized, the routing that overrides the
// assessment's recommendation. The assessment itself is untouched: what the
// planner recommended stays recorded as evidence.
//
// This records an intent only. Nothing is steered, interrupted or paused here,
// and no state of the delivery evidence moves past "pending".
func factoryApplyRevisionDeliveryChoice(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	if p != nil {
		return fmt.Errorf("%w: delivery choice belongs to an authenticated Studio user", errFactoryDenied)
	}
	if err := factoryRevisionSavable(*i); err != nil {
		return err
	}
	r := c.Revision
	if r == nil {
		return fmt.Errorf("%w: revision payload required", errFactoryInput)
	}
	n, err := factoryRevisionProposalAt(i, r)
	if err != nil {
		return err
	}
	proposal := &i.Proposals[n]
	switch proposal.Status {
	case "proposed", "awaiting_authorization":
		if r.Routing != "" {
			if !factoryRevisionRoutings[r.Routing] {
				return fmt.Errorf("%w: routing must be amend or follow_up", errFactoryInput)
			}
			// The user's choice overrides the recommendation; the assessment
			// keeps the recommendation it made.
			proposal.Routing = r.Routing
		}
	case "authorized":
		if r.Routing != "" && r.Routing != proposal.Routing {
			return fmt.Errorf("%w: proposal %s is authorized for %s; supersede it to change the routing", ErrFactoryConflict, proposal.ID, proposal.Routing)
		}
	default:
		return fmt.Errorf("%w: proposal %s is %s and has no delivery to choose", ErrFactoryConflict, proposal.ID, proposal.Status)
	}
	if r.CandidateHash != "" && r.CandidateHash != proposal.CandidateHash {
		return fmt.Errorf("%w: the choice does not name proposal %s's candidate", ErrFactoryConflict, proposal.ID)
	}
	if !factoryRevisionStrategies[r.Strategy] {
		return fmt.Errorf("%w: choose wait_safe, interrupt or hold", errFactoryInput)
	}
	proposal.Delivery = &FactoryRevisionDelivery{Strategy: r.Strategy, State: "pending", CandidateHash: proposal.CandidateHash}
	proposal.Version++
	proposal.UpdatedAt = now
	i.History = append(i.History, FactoryEvent{
		At: now, Kind: "revision_delivery_chosen", EffectID: proposal.ID,
		Message: "delivery strategy " + r.Strategy + " chosen",
	})
	return nil
}

// factoryApplyRevisionFollowUpConfirm journals the durable create intent for an
// authorized follow-up: the exact confirmed plan/specification/configuration is
// validated as the normal factory draft it becomes, and the deterministic
// request identity is recorded before anything is created. The created item is
// linked afterwards, and only once its journal record is confirmed.
func factoryApplyRevisionFollowUpConfirm(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	if p != nil {
		return fmt.Errorf("%w: a follow-up draft is confirmed by an authenticated Studio user", errFactoryDenied)
	}
	if err := factoryRevisionSavable(*i); err != nil {
		return err
	}
	r := c.Revision
	if r == nil {
		return fmt.Errorf("%w: revision payload required", errFactoryInput)
	}
	n, err := factoryRevisionProposalAt(i, r)
	if err != nil {
		return err
	}
	proposal := &i.Proposals[n]
	if proposal.Routing != "follow_up" {
		return fmt.Errorf("%w: proposal %s is authorized for %q, not a follow-up", ErrFactoryConflict, proposal.ID, proposal.Routing)
	}
	if proposal.Status != "follow_up_pending" && proposal.Status != "authorized" {
		return fmt.Errorf("%w: proposal %s is %s and has no follow-up to confirm", ErrFactoryConflict, proposal.ID, proposal.Status)
	}
	if r.CandidateHash != "" && r.CandidateHash != proposal.CandidateHash {
		return fmt.Errorf("%w: the confirmation does not name proposal %s's candidate", ErrFactoryConflict, proposal.ID)
	}
	if r.FollowUp == nil {
		return fmt.Errorf("%w: revision.followUp required", errFactoryInput)
	}
	follow, err := factoryFollowUpCandidate(*r.FollowUp)
	if err != nil {
		return err
	}
	proposal.Status = "follow_up_pending"
	proposal.FollowUpRequestID = factoryRevisionFollowUpRequestID(i.ID, proposal.ID)
	proposal.Version++
	proposal.UpdatedAt = now
	i.History = append(i.History, FactoryEvent{
		At: now, Kind: "revision_follow_up_confirmed", EffectID: proposal.ID,
		Message: "follow-up draft confirmed: " + follow.Title,
	})
	return nil
}

// activateAuthorizedRevisions advances every authorized amendment to the safe
// boundary. It journals at most one transition effect per item and leaves the
// actual dispatch to the ordinary scheduler, so no second execution path
// exists.
func (c *FactoryController) activateAuthorizedRevisions(ctx context.Context) error {
	if c.store == nil || c.effects == nil {
		return nil
	}
	for _, listed := range c.store.List() {
		if _, ok := factoryRevisionActivationPending(listed); !ok {
			continue
		}
		if err := c.activateAuthorizedRevision(ctx, listed.ID); err != nil {
			if errors.Is(err, ErrFactoryConflict) {
				continue
			}
			return err
		}
	}
	return nil
}

// activateAuthorizedRevision is the gate one authorized amendment passes. It
// rechecks the target and the active approval, that every earlier effect
// settled, and the whole idle conjunction the scheduler uses, and it never
// activates while a ready PR has not been confirmed back to draft.
func (c *FactoryController) activateAuthorizedRevision(ctx context.Context, itemID string) error {
	item, err := c.store.Get(itemID)
	if err != nil {
		return err
	}
	n, ok := factoryRevisionActivationPending(item)
	if !ok || factoryRevisionSavable(item) != nil || item.Decision != nil || len(item.Approvals) == 0 {
		return nil
	}
	proposal := item.Proposals[n]
	active := item.Approvals[len(item.Approvals)-1]
	if proposal.BaseRevision != active.Revision || proposal.BaseHash != active.PlanHash {
		// An intervening approval makes the base stale: authorize a fresh
		// proposal rather than quietly rebasing this one.
		return c.retireStaleRevision(item, n)
	}
	for _, e := range item.Effects {
		if e.Status != "completed" {
			// The old effect has not settled, so the old revision stays
			// active and the in-flight worker keeps the revision it started
			// under.
			return nil
		}
	}
	obs, err := c.effects.Observe(ctx, item)
	if err != nil || !obs.RuntimeKnown || !obs.TurnIdle || !obs.ActiveWorkKnown || !obs.WorkIdle {
		return nil
	}
	if item.State == "ready" && !factoryRevisionTransitionConfirmed(item, proposal.ID) {
		if factoryRevisionTransitionRecorded(item, proposal.ID) {
			// The transition could not confirm the PR is a draft, so the
			// authorized revision stays pending and the PR is not claimed safe
			// to change.
			return nil
		}
		if obs.PR == nil {
			return nil
		}
		if !obs.PR.Draft {
			return c.journalRevisionTransition(item, n)
		}
		// The PR is already a draft, so there is no readiness to revoke: the
		// activation below is the whole transition.
	}
	activated, err := c.activateRevision(item, n)
	if err != nil {
		return err
	}
	if activated.Version != item.Version {
		c.Wake()
	}
	return nil
}

// retireStaleRevision retires an authorized amendment whose base is no longer
// the active approval: an intervening approval requires a fresh proposal and a
// fresh authorization, which is the one honest outcome.
func (c *FactoryController) retireStaleRevision(item FactoryItem, n int) error {
	proposal := item.Proposals[n]
	active := item.Approvals[len(item.Approvals)-1]
	if proposal.BaseRevision == active.Revision && proposal.BaseHash == active.PlanHash {
		return nil
	}
	_, err := c.internalUpdate(item, "revision_authorization_stale", func(dst *FactoryItem) error {
		if n >= len(dst.Proposals) || dst.Proposals[n].ID != proposal.ID || dst.Proposals[n].Status != "authorized" {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		p.Status = "rejected"
		p.Version++
		p.UpdatedAt = c.now()
		dst.History = append(dst.History, FactoryEvent{
			At: c.now(), Kind: "revision_authorization_stale", EffectID: p.ID,
			Message: fmt.Sprintf("the approved revision moved to %d; authorize a fresh proposal", active.Revision),
		})
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// journalRevisionTransition journals the existing set_draft effect for a ready
// item, tagged as a revision transition. The ordinary dispatcher executes it.
func (c *FactoryController) journalRevisionTransition(item FactoryItem, n int) error {
	proposal := item.Proposals[n]
	_, err := c.internalUpdate(item, "revision_transition_draft", func(dst *FactoryItem) error {
		if n >= len(dst.Proposals) || dst.Proposals[n].ID != proposal.ID {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		if p.Status != "authorized" || p.Routing != "amend" {
			return ErrFactoryConflict
		}
		if len(dst.Approvals) == 0 {
			return ErrFactoryConflict
		}
		active := dst.Approvals[len(dst.Approvals)-1]
		if p.BaseRevision != active.Revision || p.BaseHash != active.PlanHash {
			return ErrFactoryConflict
		}
		if len(dst.Effects) > 0 && dst.Effects[len(dst.Effects)-1].Status != "completed" {
			return ErrFactoryConflict
		}
		e := factoryNext(dst, "set_draft", "", 0, c.now())
		dst.State = "checking"
		dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "revision_transition", EffectID: e.ID, Message: p.ID})
		return nil
	})
	if err == nil {
		c.Wake()
	}
	return err
}

// activateRevision appends the one new approval the gate authorized: the
// proposal's exact candidate bytes under the configuration that was already in
// force, with the source and the activation event journaled in the same
// mutation. The proposal is marked activated so it can never be authorized or
// superseded again.
func (c *FactoryController) activateRevision(item FactoryItem, n int) (FactoryItem, error) {
	proposal := item.Proposals[n]
	now := c.now().UTC()
	return c.internalUpdate(item, "revision_activate", func(dst *FactoryItem) error {
		if n >= len(dst.Proposals) || dst.Proposals[n].ID != proposal.ID {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		if p.Status != "authorized" || p.Routing != "amend" {
			return ErrFactoryConflict
		}
		if len(dst.Approvals) == 0 {
			return ErrFactoryConflict
		}
		active := dst.Approvals[len(dst.Approvals)-1]
		if p.BaseRevision != active.Revision || p.BaseHash != active.PlanHash {
			return ErrFactoryConflict
		}
		if len(dst.Effects) > 0 && dst.Effects[len(dst.Effects)-1].Status != "completed" {
			return ErrFactoryConflict
		}
		approval := FactoryApproval{
			Revision:   active.Revision + 1,
			Plan:       p.Plan,
			Spec:       p.Spec,
			PlanHash:   p.CandidateHash,
			Config:     active.Config,
			ApprovedAt: now,
		}
		dst.Approvals = append(dst.Approvals, approval)
		p.Status = "activated"
		p.Version++
		p.UpdatedAt = now
		if dst.State == "ready" || (dst.State == "needs_input" && dst.Decision == nil) {
			// The readiness the old approval earned does not survive the revision
			// it was earned under. A decision-less needs_input is the same case:
			// it is where an older redraft left an item whose activation had to
			// wait, and the fresh work is the scheduler's to start, not a
			// person's to answer.
			dst.State = "checking"
		}
		dst.History = append(dst.History, FactoryEvent{
			At: now, Kind: "revision_activated", EffectID: p.ID,
			Message: fmt.Sprintf("revision %d activated from proposal %s authorized by %s", approval.Revision, p.ID, p.AuthorizedBy),
		})
		return nil
	})
}

// confirmRevisionFollowUp creates the draft an authorized follow-up confirmed
// and links it to its source. The create names the deterministic request
// identity the intent recorded, so a lost response replays the draft it already
// produced instead of creating a second one.
func (c *FactoryController) confirmRevisionFollowUp(item FactoryItem, r *FactoryRevisionCommand) (FactoryItem, error) {
	if c.store == nil || r == nil || r.FollowUp == nil {
		return item, nil
	}
	current, err := c.store.Get(item.ID)
	if err != nil {
		return item, err
	}
	n, ok := factoryRevisionProposalByID(current, r.ProposalID)
	if !ok {
		return item, nil
	}
	proposal := current.Proposals[n]
	if proposal.FollowUpRequestID == "" {
		return item, fmt.Errorf("%w: proposal %s carries no follow-up intent", ErrFactoryConflict, proposal.ID)
	}
	if proposal.FollowUpItemID != "" {
		// A replay whose association already landed lands nothing new.
		return current, nil
	}
	follow, err := factoryFollowUpCandidate(*r.FollowUp)
	if err != nil {
		return item, err
	}
	follow.RequestID = proposal.FollowUpRequestID
	created, err := c.store.CreateResolved(follow, follow, c.now().UTC())
	if err != nil {
		return item, err
	}
	return c.linkRevisionFollowUp(current, proposal.ID, created.ID)
}

// linkRevisionFollowUp records the association and the source link. Both are
// idempotent, so a reconciliation after a crash lands the same two entries the
// first attempt would have.
func (c *FactoryController) linkRevisionFollowUp(item FactoryItem, proposalID, createdID string) (FactoryItem, error) {
	linked, err := c.internalUpdate(item, "revision_follow_up_linked", func(dst *FactoryItem) error {
		n, ok := factoryRevisionProposalByID(*dst, proposalID)
		if !ok {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		if p.FollowUpItemID == createdID && p.Status == "followed_up" {
			return ErrFactoryConflict
		}
		p.FollowUpItemID = createdID
		p.Status = "followed_up"
		p.Version++
		p.UpdatedAt = c.now()
		dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "revision_follow_up_linked", EffectID: p.ID, Message: createdID})
		return nil
	})
	if err != nil {
		return item, err
	}
	return linked, c.recordRevisionSource(createdID, item.ID, proposalID)
}

// recordRevisionSource writes the one source link the created draft carries, so
// both histories name the association. A replayed request lands nothing twice.
func (c *FactoryController) recordRevisionSource(createdID, sourceID, proposalID string) error {
	created, err := c.store.Get(createdID)
	if err != nil {
		return err
	}
	if factoryRevisionSourceLinked(created, proposalID) {
		return nil
	}
	_, err = c.store.Update(createdID, "revision-source-"+proposalID, created.Version, json.RawMessage(`{"kind":"revision_source"}`), func(dst *FactoryItem) error {
		dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "revision_source", EffectID: proposalID, Message: sourceID})
		return nil
	}, c.now())
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// reconcileRevisionFollowUps links a follow-up whose create committed while its
// association was lost. The intent is the durable record, the deterministic
// request identity is the lookup, and a follow-up whose create never committed
// stays pending for an explicit re-confirmation rather than being invented.
func (c *FactoryController) reconcileRevisionFollowUps() error {
	if c.store == nil {
		return nil
	}
	for _, item := range c.store.List() {
		for n := range item.Proposals {
			proposal := item.Proposals[n]
			if proposal.FollowUpRequestID == "" {
				continue
			}
			if proposal.FollowUpItemID != "" {
				// The association committed. The source link is a second
				// mutation, so a crash between the two would otherwise leave the
				// created draft without the link in its own history for ever.
				// Recording it is idempotent and replays harmlessly.
				if err := c.recordRevisionSource(proposal.FollowUpItemID, item.ID, proposal.ID); err != nil {
					if errors.Is(err, ErrFactoryNotFound) || errors.Is(err, ErrFactoryConflict) {
						continue
					}
					return err
				}
				continue
			}
			created, ok := c.store.ItemForRequest(proposal.FollowUpRequestID)
			if !ok || created.ID == item.ID {
				continue
			}
			if _, err := c.linkRevisionFollowUp(item, proposal.ID, created.ID); err != nil {
				if errors.Is(err, ErrFactoryConflict) {
					continue
				}
				return err
			}
			break
		}
	}
	return nil
}
