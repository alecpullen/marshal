package bridge

import (
	"marshal/web/bridge/contract"
)

// The plan-revision types are contract types (F6 decision 1); the bridge
// aliases them here so its code and the journal's bytes are unchanged.
type (
	// FactoryRevisionAssessment is contract.FactoryRevisionAssessment.
	FactoryRevisionAssessment = contract.FactoryRevisionAssessment
	// FactoryRevisionDelivery is contract.FactoryRevisionDelivery.
	FactoryRevisionDelivery = contract.FactoryRevisionDelivery
	// FactoryRevisionProposal is contract.FactoryRevisionProposal.
	FactoryRevisionProposal = contract.FactoryRevisionProposal
	// FactoryRevisionCommand is contract.FactoryRevisionCommand.
	FactoryRevisionCommand = contract.FactoryRevisionCommand
)

// factoryRevisionProposalStates are the proposal lifecycle states.
var factoryRevisionProposalStates = map[string]bool{
	"proposed": true, "awaiting_authorization": true, "authorized": true,
	"activated": true, "rejected": true, "superseded": true,
	"follow_up_pending": true, "followed_up": true,
}

// factoryRevisionAssessmentStates are the assessment lifecycle states. A
// state records what was observed: "pending" is an intent, not proof that a
// request was dispatched.
var factoryRevisionAssessmentStates = map[string]bool{
	"not_requested": true, "pending": true, "assessing": true, "assessed": true,
	"unavailable": true, "uncertain": true, "failed": true,
}

// factoryRevisionClassifications are the assessment classifications.
var factoryRevisionClassifications = map[string]bool{
	"routine_clarification": true, "scope_change": true, "needs_user": true,
}

// factoryRevisionRoutings are the routing choices a proposal may carry, and
// the recommendation an assessment may make.
var factoryRevisionRoutings = map[string]bool{"amend": true, "follow_up": true}

// factoryRevisionStrategies are the delivery strategies.
var factoryRevisionStrategies = map[string]bool{"wait_safe": true, "interrupt": true, "hold": true}

// factoryRevisionDeliveryStates are the delivery evidence states.
var factoryRevisionDeliveryStates = map[string]bool{
	"pending": true, "dispatching": true, "accepted": true, "interrupting": true,
	"waiting_safe_boundary": true, "waiting_for_resume": true, "delivered": true,
	"undelivered": true, "uncertain": true,
}

// factoryRevisionPauseStates are the recorded pause states.
var factoryRevisionPauseStates = map[string]bool{
	"requested": true, "stopping": true, "paused": true, "uncertain": true,
}

// factoryRevisionUnresolved reports whether a proposal still awaits a decision.
// Only one may exist per item, so a replacement has to supersede it.
func factoryRevisionUnresolved(status string) bool {
	switch status {
	case "proposed", "awaiting_authorization", "authorized", "follow_up_pending":
		return true
	}
	return false
}

// factoryRevisionProposalID is the identity a proposal is journalled under. It
// is derived from the item and the request that created it, so a replayed
// request names the same proposal and a different request names a new one.
func factoryRevisionProposalID(itemID, requestID string) string {
	return "rev-" + factoryDigest([]byte(itemID+"\x00"+requestID))
}

// factoryRevisionAssessmentAttemptID is the deterministic identity of a
// proposal's first assessment attempt: the proposal and the attempt are one
// intent, so a client retrying sees the same intent rather than a second one.
func factoryRevisionAssessmentAttemptID(proposalID string) string {
	return proposalID + ":assessment:1"
}
