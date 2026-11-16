package bridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RevisionAssessmentRequest is everything a planner is given to judge one
// proposed revision: the candidate, the approved revision it was written
// against, and the work that is running now.
//
// The candidate hash, the base revision and the attempt id are the values the
// journal recorded, never ones a caller supplied, so a planner's answer can be
// checked against them exactly.
type RevisionAssessmentRequest struct {
	ItemID        string `json:"itemId"`
	ProposalID    string `json:"proposalId"`
	AttemptID     string `json:"attemptId"`
	BaseRevision  uint64 `json:"baseRevision"`
	CandidateHash string `json:"candidateHash"`
	ApprovedPlan  string `json:"approvedPlan"`
	ApprovedSpec  string `json:"approvedSpec"`
	ProposedPlan  string `json:"proposedPlan"`
	ProposedSpec  string `json:"proposedSpec"`
	EffectID      string `json:"effectId,omitempty"`
	WorkerID      string `json:"workerId,omitempty"`
}

// revisionPlanner is the instance-owned seam a bridge uses to have a candidate
// revision judged. It is optional: a bridge that wires none gets the
// unavailable default, which reports itself unavailable and starts nothing, so
// a project's assessment intent is recorded and left for an explicit request
// rather than retried in the background.
//
// Available is asked about one project root, because a planner may be
// configured for some projects and not others. Assess must be called only after
// the journal recorded the attempt, and its answer is advisory: the journal
// records the classification the planner asserted, and authorization rechecks
// who may act on it.
type revisionPlanner interface {
	Available(context.Context, string) (bool, error)
	Assess(context.Context, RevisionAssessmentRequest) (FactoryRevisionAssessment, error)
}

// errFactoryRevisionPlannerUnavailable is what the no-planner default answers
// with, so a caller that ignored Available cannot mistake a missing planner for
// an assessment.
var errFactoryRevisionPlannerUnavailable = errors.New("no plan-revision planner is configured")

// unavailableRevisionPlanner is the default seam: it reports itself
// unavailable, dispatches nothing and owns no goroutine, timer or retry loop.
type unavailableRevisionPlanner struct{}

func (unavailableRevisionPlanner) Available(context.Context, string) (bool, error) {
	return false, nil
}

func (unavailableRevisionPlanner) Assess(context.Context, RevisionAssessmentRequest) (FactoryRevisionAssessment, error) {
	return FactoryRevisionAssessment{}, errFactoryRevisionPlannerUnavailable
}

// revisionPlannerOrDefault is the planner the controller dispatches to. A nil
// seam is the unavailable default, exactly as a nil projectSettings seam is the
// documented assessment preference.
func (c *FactoryController) revisionPlannerOrDefault() revisionPlanner {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.planner == nil {
		return unavailableRevisionPlanner{}
	}
	return c.planner
}

// setRevisionPlanner installs the planner a bridge dispatches to. It is
// instance-owned and never a package global, so two bridges in one process keep
// their own planners.
func (c *FactoryController) setRevisionPlanner(p revisionPlanner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.planner = p
}

// factoryRevisionAssessmentAwaiting reports whether one proposal's assessment
// is still waiting for a planner: the intent was recorded (or an attempt was
// lost) and the proposal itself is unresolved.
func factoryRevisionAssessmentAwaiting(p FactoryRevisionProposal) bool {
	return p.AssessmentState == "pending" && factoryRevisionUnresolved(p.Status)
}

// factoryRevisionAssessmentTarget finds the one proposal on an item whose
// assessment is waiting for a planner.
func factoryRevisionAssessmentTarget(i FactoryItem) (int, bool) {
	for n := range i.Proposals {
		if factoryRevisionAssessmentAwaiting(i.Proposals[n]) {
			return n, true
		}
	}
	return -1, false
}

// factoryRevisionAssessmentRequest builds the planner's input from durable
// state only.
func factoryRevisionAssessmentRequest(i FactoryItem, n int) RevisionAssessmentRequest {
	p := i.Proposals[n]
	req := RevisionAssessmentRequest{
		ItemID: i.ID, ProposalID: p.ID, AttemptID: p.AssessmentAttemptID,
		BaseRevision: p.BaseRevision, CandidateHash: p.CandidateHash,
		ProposedPlan: p.Plan, ProposedSpec: p.Spec,
	}
	if len(i.Approvals) > 0 {
		active := i.Approvals[len(i.Approvals)-1]
		req.ApprovedPlan, req.ApprovedSpec = active.Plan, active.Spec
	}
	if e := factoryLastEffect(i); e != nil {
		req.EffectID = e.ID
	}
	for n := len(i.Workers) - 1; n >= 0; n-- {
		if !i.Workers[n].Parked {
			req.WorkerID = i.Workers[n].AgentID
			break
		}
	}
	return req
}

// factoryRevisionNextAssessmentAttemptID bumps the trailing number of an
// assessment attempt id. A manual retry names a new intent instead of rewriting
// the one that was lost, so an answer to the old attempt cannot land on it.
func factoryRevisionNextAssessmentAttemptID(attemptID string) string {
	cut := strings.LastIndex(attemptID, ":")
	if cut < 0 {
		return attemptID + ":2"
	}
	n, err := strconv.Atoi(attemptID[cut+1:])
	if err != nil || n < 1 {
		return attemptID + ":2"
	}
	return attemptID[:cut+1] + strconv.Itoa(n+1)
}

// factoryRevisionAssessmentAttempt refuses a command naming any attempt but the
// one the journal recorded, so an answer to a replaced attempt cannot land on
// its successor.
func factoryRevisionAssessmentAttempt(p *FactoryRevisionProposal, attempt string) error {
	if strings.TrimSpace(attempt) == "" || strings.TrimSpace(p.AssessmentAttemptID) == "" || attempt != p.AssessmentAttemptID {
		return fmt.Errorf("%w: attempt %q is not proposal %s's current assessment attempt", ErrFactoryConflict, attempt, p.ID)
	}
	return nil
}

// factoryRevisionAssessmentClaimedBy reports whether attempt was claimed by
// clientID. The claim entry names the attempt (which names the proposal) and
// carries the attribution, so the journal itself is the evidence of who may
// deliver the result.
func factoryRevisionAssessmentClaimedBy(i FactoryItem, attempt, clientID string) bool {
	for n := len(i.History) - 1; n >= 0; n-- {
		e := i.History[n]
		if e.Kind != "revision_assessment_claimed" || e.Message != attempt {
			continue
		}
		return e.Provenance != nil && e.Provenance.ClientID == clientID
	}
	return false
}

// factoryRevisionEffectKnown reports whether effectID names an effect this item
// actually carries, which is what an assessment's "affected" claim is checked
// against.
func factoryRevisionEffectKnown(i FactoryItem, effectID string) bool {
	for _, e := range i.Effects {
		if e.ID == effectID {
			return true
		}
	}
	return false
}

// factoryRevisionWorkerKnown reports whether workerID names a worker this item
// actually carries.
func factoryRevisionWorkerKnown(i FactoryItem, workerID string) bool {
	for _, w := range i.Workers {
		if w.AgentID == workerID {
			return true
		}
	}
	return false
}

// factoryRevisionRecordAssessment journals one planner's answer on a proposal
// and records the routing it recommends.
//
// Classification and provenance stay explicit and attributed: the entry names
// the planner's assessor, the classification it asserted and the strategy it
// recommended. Routine authorization is carried out here only when the caller
// established, under the policy lock, that the project delegates it — and only
// for a routine clarification. A scope change or a needs-user answer is left
// awaiting an authenticated Studio user, and a pause is never claimed: the
// recommendation is recorded and nothing is interrupted.
func factoryRevisionRecordAssessment(i *FactoryItem, n int, a FactoryRevisionAssessment, routine bool, authorizedBy string, p *factoryPrincipal, now time.Time) error {
	proposal := &i.Proposals[n]
	switch {
	case !factoryRevisionClassifications[a.Classification]:
		return fmt.Errorf("%w: classification %q is not a plan-revision classification", errFactoryInput, a.Classification)
	case !factoryRevisionRoutings[a.Recommendation]:
		return fmt.Errorf("%w: recommendation %q is not a plan-revision routing", errFactoryInput, a.Recommendation)
	case !factoryRevisionStrategies[a.Strategy]:
		return fmt.Errorf("%w: strategy %q is not a delivery strategy", errFactoryInput, a.Strategy)
	case strings.TrimSpace(a.AssessorID) == "":
		return fmt.Errorf("%w: assessorId is required", errFactoryInput)
	}
	// The assertion has to name what it judged: a stale candidate or a base
	// revision this proposal was not written against is refused rather than
	// recorded against the current one.
	if a.CandidateHash != proposal.CandidateHash || a.BaseRevision != proposal.BaseRevision {
		return fmt.Errorf("%w: the assessment does not name proposal %s's candidate at base revision %d", ErrFactoryConflict, proposal.ID, proposal.BaseRevision)
	}
	if a.AffectedEffectID != "" && !factoryRevisionEffectKnown(*i, a.AffectedEffectID) {
		return fmt.Errorf("%w: affected effect %s is not on this item", ErrFactoryConflict, a.AffectedEffectID)
	}
	if a.AffectedWorkerID != "" && !factoryRevisionWorkerKnown(*i, a.AffectedWorkerID) {
		return fmt.Errorf("%w: affected worker %s is not on this item", ErrFactoryConflict, a.AffectedWorkerID)
	}
	recorded := a
	proposal.Assessment = &recorded
	proposal.AssessmentState = "assessed"
	proposal.Routing = a.Recommendation
	proposal.Version++
	proposal.UpdatedAt = now
	message := "plan revision assessed: " + a.Classification + "/" + a.Recommendation + " (" + a.Strategy + ")"
	e := p.event("revision_assessed", message, "", now)
	e.EffectID = proposal.ID
	i.History = append(i.History, e)
	if routine && a.Classification == "routine_clarification" {
		proposal.Status = "authorized"
		proposal.Authority = factoryAuthorityRoutineRevision
		proposal.AuthorizedBy = authorizedBy
		i.History = append(i.History, p.event("revision_authorized", "routine clarification authorized by the project's designated planner", "", now))
	} else if proposal.Status == "proposed" {
		// A scope change and a needs-user answer both need an authenticated
		// Studio user; without delegated routine authority a routine
		// clarification does too. Recording the assessment moves neither.
		proposal.Status = "awaiting_authorization"
	}
	return nil
}

// factoryApplyRevisionAssessClaim journals that one client has taken the
// assessment of a proposal on, before anything external runs. The attempt it
// names must be the current one, and an attempt already claimed is refused: a
// lost claim is resolved by an explicit retry, never by a second claim.
func factoryApplyRevisionAssessClaim(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	n, err := factoryRevisionProposalAt(i, c.Revision)
	if err != nil {
		return err
	}
	proposal := &i.Proposals[n]
	if !factoryRevisionUnresolved(proposal.Status) {
		return fmt.Errorf("%w: proposal %s is %s", ErrFactoryConflict, proposal.ID, proposal.Status)
	}
	if err := factoryRevisionAssessmentAttempt(proposal, c.Revision.AttemptID); err != nil {
		return err
	}
	switch proposal.AssessmentState {
	case "pending":
	case "assessing":
		return fmt.Errorf("%w: assessment %s is already claimed", ErrFactoryConflict, proposal.AssessmentAttemptID)
	default:
		return fmt.Errorf("%w: assessment of proposal %s is %s", ErrFactoryConflict, proposal.ID, proposal.AssessmentState)
	}
	proposal.AssessmentState = "assessing"
	proposal.Version++
	proposal.UpdatedAt = now
	e := p.event("revision_assessment_claimed", proposal.AssessmentAttemptID, "", now)
	e.EffectID = proposal.ID
	i.History = append(i.History, e)
	return nil
}

// factoryApplyRevisionAssessResult journals the assessment the claiming client
// delivered. Only that client may deliver it, and only against the attempt it
// claimed and the candidate and base revision this proposal carries.
func factoryApplyRevisionAssessResult(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	n, err := factoryRevisionProposalAt(i, c.Revision)
	if err != nil {
		return err
	}
	proposal := &i.Proposals[n]
	if !factoryRevisionUnresolved(proposal.Status) {
		return fmt.Errorf("%w: proposal %s is %s", ErrFactoryConflict, proposal.ID, proposal.Status)
	}
	if err := factoryRevisionAssessmentAttempt(proposal, c.Revision.AttemptID); err != nil {
		return err
	}
	if proposal.AssessmentState != "assessing" {
		return fmt.Errorf("%w: assessment of proposal %s is %s", ErrFactoryConflict, proposal.ID, proposal.AssessmentState)
	}
	if p == nil || strings.TrimSpace(p.ClientID) == "" || !factoryRevisionAssessmentClaimedBy(*i, proposal.AssessmentAttemptID, p.ClientID) {
		// A result is accepted only from the client that claimed this attempt.
		return fmt.Errorf("%w: only the claiming client may deliver this assessment", errFactoryDenied)
	}
	a := c.Revision.Assessment
	if a == nil {
		return fmt.Errorf("%w: revision.assessment required", errFactoryInput)
	}
	// The authorizer sets this authority while the policy lock is held; a
	// caller cannot ask for it, and it is never inferred from the payload.
	routine := p.Authority == factoryAuthorityRoutineRevision
	return factoryRevisionRecordAssessment(i, n, *a, routine, p.ClientID, p, now)
}

// factoryApplyRevisionAssessRetry records one explicit user retry of an
// assessment whose outcome was lost. It retires the attempt it names and
// journals a new intent, so the client that later answers it answers a new
// question.
func factoryApplyRevisionAssessRetry(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	n, err := factoryRevisionProposalAt(i, c.Revision)
	if err != nil {
		return err
	}
	proposal := &i.Proposals[n]
	if !factoryRevisionUnresolved(proposal.Status) {
		return fmt.Errorf("%w: proposal %s is %s", ErrFactoryConflict, proposal.ID, proposal.Status)
	}
	if err := factoryRevisionAssessmentAttempt(proposal, c.Revision.AttemptID); err != nil {
		return err
	}
	switch proposal.AssessmentState {
	case "assessing", "uncertain":
	default:
		return fmt.Errorf("%w: assessment of proposal %s is %s and needs no retry", ErrFactoryConflict, proposal.ID, proposal.AssessmentState)
	}
	proposal.AssessmentAttemptID = factoryRevisionNextAssessmentAttemptID(proposal.AssessmentAttemptID)
	proposal.AssessmentState = "pending"
	proposal.Assessment = nil
	proposal.Version++
	proposal.UpdatedAt = now
	e := p.event("revision_assessment_retried", proposal.AssessmentAttemptID, "", now)
	e.EffectID = proposal.ID
	i.History = append(i.History, e)
	return nil
}

// assessPending dispatches at most one durable assessment intent per pass.
//
// The order is the durable one: the attempt is asked about and the claim is
// journalled before the planner is contacted, so an attempt whose answer is
// lost is uncertain rather than never having existed. Nothing here retries on
// its own — an unavailable planner leaves the intent recorded, and a lost
// answer waits for an explicit retry.
func (c *FactoryController) assessPending(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	planner := c.revisionPlannerOrDefault()
	for _, item := range c.store.List() {
		n, ok := factoryRevisionAssessmentTarget(item)
		if !ok {
			continue
		}
		available, err := planner.Available(ctx, item.ProjectRoot)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil || !available {
			return c.markRevisionAssessmentUnavailable(item, n)
		}
		claimed, err := c.claimRevisionAssessmentForPlanner(item, n)
		if err != nil {
			if errors.Is(err, ErrFactoryConflict) {
				return nil
			}
			return err
		}
		req := factoryRevisionAssessmentRequest(claimed, n)
		assessment, assessErr := planner.Assess(ctx, req)
		if ctx.Err() != nil {
			// A shutdown in the middle of a dispatch is a lost answer, not a
			// reason to requeue: the attempt is journalled uncertain below and
			// only a user retry starts another.
			assessErr = ctx.Err()
		}
		if assessErr != nil {
			return c.markRevisionAssessmentUncertain(claimed, n, req.AttemptID, "the assessment request was dispatched and its outcome is unknown")
		}
		if err := c.recordRevisionAssessment(claimed, n, assessment, req.AttemptID); err != nil {
			return err
		}
		return nil
	}
	return nil
}

// claimRevisionAssessmentForPlanner journals the controller's claim of one
// attempt. It carries no client attribution: the planner that answers it is
// this bridge's own, and the assessor it names is the one the planner reports.
func (c *FactoryController) claimRevisionAssessmentForPlanner(i FactoryItem, n int) (FactoryItem, error) {
	claimed, err := c.internalUpdate(i, "revision_assess_claim", func(dst *FactoryItem) error {
		proposal := &dst.Proposals[n]
		if !factoryRevisionAssessmentAwaiting(*proposal) || strings.TrimSpace(proposal.AssessmentAttemptID) == "" {
			return ErrFactoryConflict
		}
		proposal.AssessmentState = "assessing"
		proposal.Version++
		proposal.UpdatedAt = c.now()
		dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "revision_assessment_claimed", Message: proposal.AssessmentAttemptID, EffectID: proposal.ID})
		return nil
	})
	if err != nil {
		return FactoryItem{}, err
	}
	return claimed, nil
}

// markRevisionAssessmentUnavailable records that no planner could take this
// intent, and stops there: the proposal keeps its attempt and waits for an
// explicit request.
func (c *FactoryController) markRevisionAssessmentUnavailable(i FactoryItem, n int) error {
	_, err := c.internalUpdate(i, "revision_assess_unavailable", func(dst *FactoryItem) error {
		proposal := &dst.Proposals[n]
		if !factoryRevisionAssessmentAwaiting(*proposal) {
			return ErrFactoryConflict
		}
		proposal.AssessmentState = "unavailable"
		proposal.Version++
		proposal.UpdatedAt = c.now()
		dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "revision_assessment_unavailable", Message: "no plan-revision planner is available for this project", EffectID: proposal.ID})
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// markRevisionAssessmentUncertain records that an attempt produced no usable
// answer. It is a terminal state for the attempt: nothing requeues it.
func (c *FactoryController) markRevisionAssessmentUncertain(i FactoryItem, n int, attempt, message string) error {
	_, err := c.internalUpdate(i, "revision_assess_uncertain", func(dst *FactoryItem) error {
		proposal := &dst.Proposals[n]
		if proposal.AssessmentAttemptID != attempt || proposal.AssessmentState != "assessing" {
			return ErrFactoryConflict
		}
		proposal.AssessmentState = "uncertain"
		proposal.Version++
		proposal.UpdatedAt = c.now()
		dst.History = append(dst.History, FactoryEvent{At: c.now(), Kind: "revision_assessment_uncertain", Message: message, EffectID: proposal.ID})
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// recordRevisionAssessment journals the planner's answer. An answer the journal
// refuses (a stale candidate, an effect this item never carried) is journalled
// as an uncertain attempt: it produced no recorded assessment, and only an
// explicit retry starts another.
func (c *FactoryController) recordRevisionAssessment(i FactoryItem, n int, a FactoryRevisionAssessment, attempt string) error {
	_, err := c.internalUpdate(i, "revision_assess_result", func(dst *FactoryItem) error {
		proposal := &dst.Proposals[n]
		if proposal.AssessmentAttemptID != attempt || proposal.AssessmentState != "assessing" {
			return ErrFactoryConflict
		}
		return factoryRevisionRecordAssessment(dst, n, a, false, "", nil, c.now())
	})
	if err == nil {
		return nil
	}
	return c.markRevisionAssessmentUncertain(i, n, attempt, "the assessment did not name this proposal's target")
}
