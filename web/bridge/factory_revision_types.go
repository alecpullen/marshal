package bridge

import "time"

// FactoryRevisionAssessment is a planner's recorded judgement about one
// proposed revision. It is advisory: it names the classification it asserts,
// the work it believes is affected, the routing it recommends and whether the
// proposal can wait for a safe boundary or needs the running turn interrupted.
//
// Classification and strategy are assertions by a trusted planner, not proofs:
// the journal records what was claimed, and authorization rechecks who may act
// on it.
type FactoryRevisionAssessment struct {
	Classification   string `json:"classification"` // routine_clarification | scope_change | needs_user
	Reason           string `json:"reason"`
	AffectedEffectID string `json:"affectedEffectId,omitempty"`
	AffectedWorkerID string `json:"affectedWorkerId,omitempty"`
	Recommendation   string `json:"recommendation"` // amend | follow_up
	Strategy         string `json:"strategy"`       // wait_safe | interrupt | hold
	AssessorID       string `json:"assessorId"`
	CandidateHash    string `json:"candidateHash"`
	BaseRevision     uint64 `json:"baseRevision"`
}

// FactoryRevisionDelivery is the recorded intent and evidence for handing an
// authorized revision to the worker that is running, or will run, the item.
//
// It is evidence, not a request log: a state here was observed, or the outcome
// of the attempt is explicitly unknown. "delivered" means the runtime accepted
// the input, never that the agent followed it.
type FactoryRevisionDelivery struct {
	Strategy      string `json:"strategy"` // wait_safe | interrupt | hold
	State         string `json:"state"`    // pending | dispatching | accepted | interrupting | waiting_safe_boundary | waiting_for_resume | delivered | undelivered | uncertain
	SessionID     string `json:"sessionId,omitempty"`
	TurnID        string `json:"turnId,omitempty"`
	RequestID     string `json:"requestId,omitempty"`
	CandidateHash string `json:"candidateHash"`
}

// FactoryRevisionProposal is one immutable plan/specification revision
// proposed against an approved factory revision.
//
// The candidate bytes, the base it was proposed against, its hash and its
// creator are journal history: an edit is a new proposal, and replacing an
// unresolved one explicitly supersedes it rather than rewriting it. Only
// state, assessment, authority and delivery move afterwards, and every one of
// those moves bumps Version so a caller writing from a stale read is refused.
//
// While an amendment is authorized but not yet activated, the item's active
// approval stays the last entry of FactoryItem.Approvals: an in-flight worker
// keeps the revision it was started under.
type FactoryRevisionProposal struct {
	ID                  string                     `json:"id"`
	Version             uint64                     `json:"version"`
	BaseRevision        uint64                     `json:"baseRevision"`
	BaseHash            string                     `json:"baseHash"`
	Plan                string                     `json:"plan"`
	Spec                string                     `json:"spec"`
	CandidateHash       string                     `json:"candidateHash"`
	CreatedBy           string                     `json:"createdBy"`
	AutoAssess          bool                       `json:"autoAssess"`
	Status              string                     `json:"status"`          // proposed | awaiting_authorization | authorized | activated | rejected | superseded | follow_up_pending | followed_up
	AssessmentState     string                     `json:"assessmentState"` // not_requested | pending | assessing | assessed | unavailable | uncertain | failed
	AssessmentAttemptID string                     `json:"assessmentAttemptId,omitempty"`
	Assessment          *FactoryRevisionAssessment `json:"assessment,omitempty"`
	AuthorizedBy        string                     `json:"authorizedBy,omitempty"`
	Authority           string                     `json:"authority,omitempty"`
	Routing             string                     `json:"routing,omitempty"`    // amend | follow_up
	PauseState          string                     `json:"pauseState,omitempty"` // requested | stopping | paused | uncertain
	PauseRequestID      string                     `json:"pauseRequestId,omitempty"`
	Delivery            *FactoryRevisionDelivery   `json:"delivery,omitempty"`
	FollowUpRequestID   string                     `json:"followUpRequestId,omitempty"`
	FollowUpItemID      string                     `json:"followUpItemId,omitempty"`
	CreatedAt           time.Time                  `json:"createdAt"`
	UpdatedAt           time.Time                  `json:"updatedAt"`
}

// FactoryRevisionCommand is one plan-revision command's structured payload.
//
// A candidate is named by its exact bytes and the hash that binds them, and
// every mutation names the proposal and the version the caller read, so a
// stale command conflicts instead of silently rebasing. BaseRevision and
// BaseHash name the approved revision the candidate was written against.
type FactoryRevisionCommand struct {
	ProposalID      string                     `json:"proposalId,omitempty"`
	ProposalVersion uint64                     `json:"proposalVersion,omitempty"`
	BaseRevision    uint64                     `json:"baseRevision,omitempty"`
	BaseHash        string                     `json:"baseHash,omitempty"`
	Plan            string                     `json:"plan,omitempty"`
	Spec            string                     `json:"spec,omitempty"`
	CandidateHash   string                     `json:"candidateHash,omitempty"`
	AttemptID       string                     `json:"attemptId,omitempty"`
	Assessment      *FactoryRevisionAssessment `json:"assessment,omitempty"`
	Routing         string                     `json:"routing,omitempty"`
	Strategy        string                     `json:"strategy,omitempty"`
	FollowUp        *FactoryCreate             `json:"followUp,omitempty"`
}

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
