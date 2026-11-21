package bridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"marshal/web/bridge/contract"
)

// The assignment path: one coordinated task's work handed to one project.
//
// A dispatch is the one operation that starts work elsewhere, and it has three
// jobs that must not be separated.
//
// First, identity. The assignment intent and the ID that is its dispatch
// identity are written to the coordinator store before the dispatch has any side
// effect (coordinatorStore.BeginDispatch). That ID is then the same ID
// everywhere: the pending submission a person confirms, the spawn claim that
// settles the worker's identity before the spawn, and the tag the agent record
// carries. It is the factory effect identity (factory_workers.go,
// factory_effects.go) without a factory work item: ordinary delegation is not
// factory work, but a lost spawn response is the same problem, so a dispatch
// whose answer never arrived is reconciled from the worker's own record rather
// than by starting a second one.
//
// Second, permission. Every target is checked at dispatch time, under the policy
// lock, against the task's effective access on that project — the project's
// stored defaults with the coordinator session's override applied. A target the
// task cannot read is denied, a target it may read but not create work in is
// denied, and a target whose access carries standing approval authority starts
// immediately because that policy already authorises it. Anything less becomes
// pending intake: the operator's usual confirmation, never an operator identity
// the model claimed. One target failing is one assignment failing; its siblings
// still progress.
//
// Third, scope. Targets are canonical project IDs this daemon minted, never
// display names, and a linked thread must belong to the project it is linked to.
// The project agent identities an assignment refers to stay inside the project
// that owns them.

// coordinatorDispatchIsImmediate reports whether one effective access value is
// the project's own configured authority to start work without asking a person
// again. Only standing authority is: user-directed authority owes a
// confirmation per operation, which intake's pending submission is.
func coordinatorDispatchStartsNow(access MarshalFactoryAccess) bool {
	return marshalAccessAllows(access, factoryCapabilityApproveAndQueue) &&
		access.ApprovalAuthority != nil && *access.ApprovalAuthority == factoryAuthorityStanding
}

// coordinatorBinding is the session binding one task's access is resolved
// through for a target project.
//
// The identity is the task's coordinator session, which the daemon minted; the
// project is the target. Nothing here comes from a request body, so a caller
// cannot widen the scope it is checked against.
func (s *Server) coordinatorBinding(task contract.CoordinatorTask, projectID string) (MarshalSessionBinding, error) {
	instanceID, generation, ok := s.marshalInstance()
	if !ok || strings.TrimSpace(task.SessionID) == "" || strings.TrimSpace(projectID) == "" {
		return MarshalSessionBinding{}, errFactoryDenied
	}
	return MarshalSessionBinding{
		InstanceID: instanceID,
		ProjectID:  projectID,
		SessionID:  task.SessionID,
		Generation: generation,
	}, nil
}

// coordinatorTargetStatus resolves the task's effective access on one target
// project, under the policy lock, exactly as the attention read resolves it: the
// project's defaults, then this session's override. It is read again on every
// dispatch and every observation, so a revoked grant needs no invalidation to
// take effect.
func (s *Server) coordinatorTargetStatus(task contract.CoordinatorTask, projectID string) (MarshalConnectionStatus, error) {
	binding, err := s.coordinatorBinding(task, projectID)
	if err != nil {
		return MarshalConnectionStatus{}, err
	}
	if s.factoryAuth == nil {
		return MarshalConnectionStatus{}, errFactoryDenied
	}
	return s.factoryAuth.marshalStatus(binding)
}

// coordinatorScopeNames reports whether one project is inside the task's scope.
// A task that bound no projects is host-local: every project this daemon
// registered is in scope, and the access check still decides each one.
func (s *Server) coordinatorScopeNames(task contract.CoordinatorTask, projectID string) bool {
	for _, id := range s.coordinatorScope(task) {
		if id == projectID {
			return true
		}
	}
	return false
}

// coordinatorAuthorizeSteering admits the targets that ask to steer an existing
// thread before anything is recorded.
//
// A linked thread is observed by default; directing it is an operation that acts
// on a person's instruction, so it is admitted exactly like one: the project's
// own standing authority admits it, user-directed authority needs the receipt
// this bridge minted for that thread and that a trusted confirmation presented,
// and no configured authority denies it. Nothing here reads the direction from a
// tool argument — a model's claim proves nothing, which is why the receipt
// travels beside the arguments and is verified here.
func (s *Server) coordinatorAuthorizeSteering(task contract.CoordinatorTask, targets []contract.CoordinatorAssignTarget, requestID, receiptID string) error {
	if s.factoryAuth == nil {
		return errFactoryDenied
	}
	for _, target := range targets {
		if !target.Steer {
			continue
		}
		binding, err := s.coordinatorBinding(task, target.ProjectID)
		if err != nil {
			return err
		}
		// The receipt covers exactly this task's steering of exactly this thread.
		operation := strings.Join([]string{"coordinator.steer", task.ID, target.ProjectID, target.ThreadID}, "\x00")
		if _, err := s.factoryAuth.WithAuthorizedMarshalUserDirection(
			binding, "", factoryCapabilityApproveAndQueue, receiptID, operation, requestID,
			func(factoryPrincipal) (FactoryItem, error) { return FactoryItem{}, nil },
		); err != nil {
			return err
		}
	}
	return nil
}

// coordinatorDispatch records one task-scoped dispatch and resolves every one of
// its targets.
//
// It is the entry point task 7's handoff tool and the coordinator's own
// assignment tool both call. The task is the identity: its session supplies the
// scope every target is checked against, so a dispatch can never act as a
// project session, an external client, or a different task.
func (s *Server) coordinatorDispatch(ctx context.Context, task contract.CoordinatorTask, req contract.CoordinatorAssignRequest, receiptID string) (contract.CoordinatorAssignResult, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return contract.CoordinatorAssignResult{}, err
	}
	if s.fleet == nil || s.fleet.ws == nil {
		return contract.CoordinatorAssignResult{}, ErrCoordinatorUnavailable
	}
	// A stopped task dispatches nothing. The stop itself arrives with the stop
	// scope; refusing here is what keeps this phase from widening it.
	if task.Status != contract.CoordinatorTaskOpen {
		return contract.CoordinatorAssignResult{}, ErrCoordinatorConflict
	}
	targets, err := canonicalCoordinatorTargets(req.Targets)
	if err != nil {
		return contract.CoordinatorAssignResult{}, err
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return contract.CoordinatorAssignResult{}, fmt.Errorf("%w: requestId is required", ErrCoordinatorInput)
	}
	digest := coordinatorDispatchDigest(targets)

	// A retry of a request this task already dispatched is not a second
	// dispatch: its assignments are resumed or observed below, and the person's
	// direction for a steering target was verified when the request was first
	// admitted. Only a fresh request has to verify it, and it does so before
	// anything is recorded.
	existing, replay, err := svc.store.DispatchRequest(task.ID, requestID)
	if err != nil {
		return contract.CoordinatorAssignResult{}, err
	}
	if replay && existing.Digest != digest {
		return contract.CoordinatorAssignResult{}, ErrCoordinatorConflict
	}
	if !replay {
		if err := s.coordinatorAuthorizeSteering(task, targets, requestID, receiptID); err != nil {
			return contract.CoordinatorAssignResult{}, err
		}
	}
	records, _, err := svc.store.BeginDispatch(task.ID, requestID, digest, targets)
	if err != nil {
		return contract.CoordinatorAssignResult{}, err
	}
	byProject := make(map[string]contract.CoordinatorAssignTarget, len(targets))
	for _, target := range targets {
		byProject[target.ProjectID] = target
	}
	out := contract.CoordinatorAssignResult{
		TaskID:      task.ID,
		Assignments: make([]contract.CoordinatorAssignment, 0, len(records)),
	}
	// Each target is resolved on its own. A failure is that assignment's, and
	// the ones beside it still dispatch; only a store this bridge cannot write
	// is an error for the whole request.
	for _, rec := range records {
		updated, rerr := s.coordinatorReconcileAssignment(ctx, task, rec, byProject[rec.Assignment.ProjectID], true)
		if rerr != nil {
			if errors.Is(rerr, ErrCoordinatorUnavailable) {
				return out, rerr
			}
			return out, rerr
		}
		out.Assignments = append(out.Assignments, updated.Assignment)
	}
	return out, nil
}

// coordinatorTaskAssignments reads one task's assignments, reconciling each
// against what the daemon now holds.
//
// The read starts and sends nothing: every assignment is resolved from durable
// evidence (the worker's own record, the intake queue) and an unresolved one
// stays dispatching for the next dispatch to resume. That is what lets a client
// snapshot a reconnected screen without the snapshot itself starting work.
func (s *Server) coordinatorTaskAssignments(ctx context.Context, task contract.CoordinatorTask) ([]contract.CoordinatorAssignment, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return nil, err
	}
	records, err := svc.store.Assignments(task.ID)
	if err != nil {
		return nil, err
	}
	out := make([]contract.CoordinatorAssignment, 0, len(records))
	for _, rec := range records {
		observed, oerr := s.coordinatorReconcileAssignment(ctx, task, rec, contract.CoordinatorAssignTarget{}, false)
		if oerr != nil {
			return nil, oerr
		}
		out = append(out, observed.Assignment)
	}
	return out, nil
}

// coordinatorReconcileAssignment resolves one assignment from durable evidence
// and, when resume is set and it is still dispatching, attempts its dispatch.
//
// It is the one place an assignment's state is decided, so a fresh dispatch, a
// retry, a reconnect and a restart cannot disagree: the evidence is the same and
// the dispatch identity that would be resumed is the same.
//
// Resuming takes the assignment's dispatch lock and re-reads the assignment
// under it. Two callers that both found it dispatching — a retry racing its
// original, or two identical requests — therefore run one after the other, and
// the second sees the outcome the first wrote instead of attempting it again.
func (s *Server) coordinatorReconcileAssignment(ctx context.Context, task contract.CoordinatorTask, rec coordinatorAssignmentRecord, target contract.CoordinatorAssignTarget, resume bool) (coordinatorAssignmentRecord, error) {
	observed, err := s.coordinatorObserveAssignment(task, rec)
	if err != nil {
		return rec, err
	}
	if observed.Assignment.State != contract.CoordinatorAssignmentDispatching || !resume {
		return observed, nil
	}
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return observed, err
	}
	release := svc.lockAssignment(observed.Assignment.ID)
	defer release()
	// Re-read under the lock: another attempt may have resolved this assignment
	// between the observation and here, and its outcome is the one to report.
	current, held, err := svc.store.Assignment(task.ID, observed.Assignment.ID)
	if err != nil {
		return observed, err
	}
	if !held {
		return observed, nil
	}
	again, err := s.coordinatorObserveAssignment(task, current)
	if err != nil {
		return observed, err
	}
	if again.Assignment.State != contract.CoordinatorAssignmentDispatching {
		return again, nil
	}
	return s.coordinatorResolveAssignment(ctx, task, again, target)
}

// coordinatorObserveAssignment writes what the daemon holds about one
// assignment: the worker its identity started, the submission it is waiting on,
// or the thread it linked.
func (s *Server) coordinatorObserveAssignment(task contract.CoordinatorTask, rec coordinatorAssignmentRecord) (coordinatorAssignmentRecord, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return rec, err
	}
	now := s.coordinatorNow()
	return svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
		return s.coordinatorObserveInto(task, r, now)
	})
}

// coordinatorObserveInto is the observation itself, applied to a record under
// the store's lock. It reports whether it changed anything, so an observation
// that learns nothing new costs no write.
//
// Every change is recorded field by field rather than by comparing the whole
// assignment: two assignments holding equal-but-distinct workers are the same
// observation, and a read that sees one must not look like progress.
func (s *Server) coordinatorObserveInto(task contract.CoordinatorTask, rec *coordinatorAssignmentRecord, now time.Time) bool {
	if s.fleet == nil || s.fleet.ws == nil {
		return false
	}
	// A worker is the strongest evidence there is, and it is found by the
	// identity the dispatch wrote to the agent record itself — never by asking
	// whether a spawn "probably" worked.
	if worker, ok := s.coordinatorWorkerFor(task.ID, rec.Assignment.ID); ok {
		changed := false
		a := &rec.Assignment
		if a.Worker == nil || a.Worker.AgentID != worker.ID || a.Worker.SessionID != worker.SessionID {
			a.Worker = &contract.CoordinatorWorkerRef{AgentID: worker.ID, SessionID: worker.SessionID}
			changed = true
		}
		// Acknowledgment is the session and the instruction: a worker whose
		// session exists has not run the assignment until it was handed over.
		// The durable claim is that hand-off's record, so an assignment whose
		// submission a person confirmed records it here — the delivery happened
		// once, whatever path started the worker.
		recorded := s.coordinatorDeliveryRecorded(rec)
		if recorded && !rec.Prompted {
			rec.Prompted = true
			changed = true
		}
		state := contract.CoordinatorAssignmentDispatching
		if worker.SessionID != "" && recorded {
			state = contract.CoordinatorAssignmentRunning
		}
		if a.State != state {
			a.State = state
			changed = true
		}
		if a.IntakeID != "" {
			a.IntakeID = ""
			changed = true
		}
		if a.Failure != "" || a.Reason != "" {
			a.Failure, a.Reason = "", ""
			changed = true
		}
		if s.coordinatorAdvanceResult(rec, worker) {
			changed = true
		}
		return changed
	}
	if rec.Assignment.Relationship == contract.CoordinatorAssignmentThread {
		return s.coordinatorObserveLinked(rec)
	}
	// An unconfirmed assignment is intake: it is pending while the submission
	// exists, and its outcome once it does not.
	if pending, ok := s.fleet.ws.PendingByID(rec.Assignment.ID); ok {
		changed := false
		a := &rec.Assignment
		if !pending.ExpiresAt.IsZero() && !now.Before(pending.ExpiresAt) {
			// The queue sweeps expired submissions; one that is still here but
			// past its deadline is reported as lapsed rather than pending.
			if a.State != contract.CoordinatorAssignmentExpired {
				a.State = contract.CoordinatorAssignmentExpired
				changed = true
			}
			if a.Failure != "" {
				a.Failure = ""
				changed = true
			}
			if a.Reason != "the submission lapsed before anyone confirmed it" {
				a.Reason = "the submission lapsed before anyone confirmed it"
				changed = true
			}
			if a.IntakeID != "" {
				a.IntakeID = ""
				changed = true
			}
			return changed
		}
		if a.State != contract.CoordinatorAssignmentPending {
			a.State = contract.CoordinatorAssignmentPending
			changed = true
		}
		if a.IntakeID != pending.ID {
			a.IntakeID = pending.ID
			changed = true
		}
		if a.Failure != "" || a.Reason != "" {
			a.Failure, a.Reason = "", ""
			changed = true
		}
		if !rec.IntakeExpiresAt.Equal(pending.ExpiresAt) {
			rec.IntakeExpiresAt = pending.ExpiresAt
			changed = true
		}
		return changed
	}
	if rec.Assignment.State == contract.CoordinatorAssignmentPending || rec.Assignment.IntakeID != "" {
		// The submission is gone and no worker carries this identity: it was
		// withdrawn or denied before its deadline, or it lapsed.
		a := &rec.Assignment
		changed := false
		if rec.IntakeExpiresAt.IsZero() || now.Before(rec.IntakeExpiresAt) {
			if a.State != contract.CoordinatorAssignmentFailed || a.Failure != contract.CoordinatorFailureUnknownProject {
				changed = true
			}
			a.State = contract.CoordinatorAssignmentFailed
			a.Failure = contract.CoordinatorFailureUnknownProject
			a.Reason = "the submission was withdrawn or denied before it was confirmed"
		} else {
			if a.State != contract.CoordinatorAssignmentExpired || a.Failure != "" {
				changed = true
			}
			a.State = contract.CoordinatorAssignmentExpired
			a.Failure = ""
			a.Reason = "the submission lapsed before anyone confirmed it"
		}
		if a.IntakeID != "" {
			a.IntakeID = ""
			changed = true
		}
		return changed
	}
	return false
}

// coordinatorObserveLinked writes what a linked existing thread's record says.
//
// A link is observation: nothing was started and nothing was sent unless a
// person directed that thread. A thread this project no longer holds is a
// failure of the link, not a quiet one: an observation of a thread that moved
// elsewhere would be an observation of nothing.
func (s *Server) coordinatorObserveLinked(rec *coordinatorAssignmentRecord) bool {
	if rec.Steer && !rec.Prompted {
		// A person directed this thread and the instruction has not gone out yet.
		// The link is not resolved: the dispatch that carries the direction
		// completes it, so an observation cannot leave a directed thread
		// silently un-steered.
		return false
	}
	thread := rec.Assignment.Thread
	if thread == nil {
		rec.Assignment.State = contract.CoordinatorAssignmentFailed
		rec.Assignment.Failure = contract.CoordinatorFailureUnknownThread
		rec.Assignment.Reason = "the assignment names no thread to observe"
		return true
	}
	root, ok := s.fleet.ws.ProjectRootByID(rec.Assignment.ProjectID)
	agent, held := s.fleet.ws.Agent(thread.ID)
	if !ok || !held || agent.Project != root {
		rec.Assignment.State = contract.CoordinatorAssignmentFailed
		rec.Assignment.Failure = contract.CoordinatorFailureUnknownThread
		rec.Assignment.Reason = "the target project does not hold that thread"
		return true
	}
	changed := false
	if rec.Assignment.State != contract.CoordinatorAssignmentLinked {
		rec.Assignment.State = contract.CoordinatorAssignmentLinked
		rec.Assignment.Failure, rec.Assignment.Reason = "", ""
		changed = true
	}
	if s.coordinatorAdvanceResult(rec, agent) {
		changed = true
	}
	return changed
}

// coordinatorAdvanceResult records the newest observed result of one worker or
// linked thread.
//
// The revision advances only when the observed shape changes, so a client can
// tell a worker that has done nothing new (the revision stands) from a record
// nobody has looked at (ObservedAt stands).
func (s *Server) coordinatorAdvanceResult(rec *coordinatorAssignmentRecord, agent Agent) bool {
	digest := coordinatorResultDigest(agent)
	if digest == rec.ObservedDigest {
		return false
	}
	rec.ObservedDigest = digest
	rec.Assignment.ResultRevision++
	return true
}

// coordinatorResultDigest is the shape of one worker's result: what it has
// changed, where it pushed, whether it is waiting or interrupted. It is a digest
// of observable facts, not of the agent's live status text, so a turn that
// narrates without changing anything does not bump a result revision.
func coordinatorResultDigest(agent Agent) string {
	return factoryDigest([]byte(fmt.Sprintf(
		"session=%s\x00branch=%s\x00target=%s\x00pr=%s\x00pushed=%d\x00interrupted=%t\x00gate=%t\x00workspace=%v",
		agent.SessionID, agent.Branch, agent.TargetBranch, agent.PRUrl, agent.PushedAt.Unix(),
		agent.Interrupted, agent.GateOverride != nil, agent.Workspace)))
}

// coordinatorDeliveryRecorded reports whether one assignment's instruction was
// handed to its worker.
//
// The durable spawn claim is the record of that hand-off: SpawnClaimed marks it
// before the prompt goes out, so a confirmation that started the worker and a
// dispatch that resumed after a restart read the same answer, and neither can
// send the instruction a second time. The assignment's own flag is the fast path
// for a delivery this bridge recorded itself.
func (s *Server) coordinatorDeliveryRecorded(rec *coordinatorAssignmentRecord) bool {
	if rec.Prompted {
		return true
	}
	if s.fleet == nil || s.fleet.ws == nil {
		return false
	}
	root, ok := s.fleet.ws.ProjectRootByID(rec.Assignment.ProjectID)
	if !ok {
		return false
	}
	claim, ok := s.fleet.ws.SpawnClaim(spawnRequestScope(OriginCoordinator, root), rec.Assignment.ID)
	return ok && claim.Prompted
}

// coordinatorWorkerFor finds the agent one assignment started, by the identity
// the dispatch wrote onto the agent record. An agent that carries another
// assignment's identity is not this assignment's worker, however similar its
// name is.
func (s *Server) coordinatorWorkerFor(taskID, assignmentID string) (Agent, bool) {
	if s.fleet == nil || s.fleet.ws == nil || strings.TrimSpace(assignmentID) == "" {
		return Agent{}, false
	}
	for _, agent := range s.fleet.ws.Agents() {
		if agent.CoordinatorAssignmentID == assignmentID && agent.CoordinatorTaskID == taskID {
			return agent, true
		}
	}
	return Agent{}, false
}

// coordinatorResolveAssignment attempts one assignment's dispatch, writes the
// outcome, and returns the record as the store now holds it.
//
// Every branch here ends in the store before it is reported: an assignment that
// says pending has a submission a person can confirm, one that says running has
// a worker that acknowledged, and one that says failed has a reason someone can
// act on.
func (s *Server) coordinatorResolveAssignment(ctx context.Context, task contract.CoordinatorTask, rec coordinatorAssignmentRecord, target contract.CoordinatorAssignTarget) (coordinatorAssignmentRecord, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return rec, err
	}
	fail := func(code, reason string) (coordinatorAssignmentRecord, error) {
		return svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
			r.Assignment.State = contract.CoordinatorAssignmentFailed
			r.Assignment.Failure = code
			r.Assignment.Reason = reason
			r.Assignment.IntakeID = ""
			return true
		})
	}
	projectID := rec.Assignment.ProjectID
	root, ok := s.fleet.ws.ProjectRootByID(projectID)
	if !ok {
		// A project ID this daemon did not mint names no target. It is the
		// caller's input problem and its own assignment's failure.
		return fail(contract.CoordinatorFailureUnknownProject, "this host does not hold that project")
	}
	if !s.coordinatorScopeNames(task, projectID) {
		return fail(contract.CoordinatorFailureUnknownProject, "the task's scope does not name this project")
	}
	status, err := s.coordinatorTargetStatus(task, projectID)
	if err != nil {
		return fail(contract.CoordinatorFailureUnauthorized, "the task's access to the project could not be resolved")
	}
	// Reading the project is the floor: an assignment to a project the task
	// cannot read is denied outright, which is what a revoked grant yields.
	if !marshalAccessAllows(status.Access, factoryCapabilityRead) {
		return fail(contract.CoordinatorFailureUnauthorized, "the task's access to the project does not include reading it")
	}
	if rec.Assignment.Relationship == contract.CoordinatorAssignmentThread {
		return s.coordinatorLinkThread(task, rec, root)
	}
	// A worker is new work in the target project, so it needs the capability
	// that creates work there.
	if !marshalAccessAllows(status.Access, factoryCapabilityCreateDraft) {
		return fail(contract.CoordinatorFailureUnauthorized, "the task's access to the project does not include creating work there")
	}
	dispatch := AssignmentDispatch{
		TaskID:       task.ID,
		AssignmentID: rec.Assignment.ID,
		Project:      root,
		Title:        agentTitle(rec.Assignment.Instruction),
		Prompt:       rec.Assignment.Instruction,
	}
	if !coordinatorDispatchStartsNow(status.Access) {
		// The project's own policy does not authorize this session to start work
		// here, so the person is asked. The submission is stored under the
		// assignment's identity, and its state is pending — never running —
		// until a worker exists.
		pending, qerr := s.fleet.QueueAssignment(dispatch)
		if qerr != nil {
			return fail(contract.CoordinatorFailureUnavailable, "the submission awaiting confirmation could not be stored")
		}
		return svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
			r.Assignment.State = contract.CoordinatorAssignmentPending
			r.Assignment.IntakeID = pending.ID
			r.Assignment.Failure, r.Assignment.Reason = "", ""
			r.IntakeExpiresAt = pending.ExpiresAt
			return true
		})
	}
	// Whether the instruction still has to go out is decided by the assignment's
	// own record, not by the claim alone: a dispatch that claimed the delivery
	// and died before writing it here left the instruction unsent, and resuming
	// it is what completes the one hand-off. The claim then keeps a resumed
	// dispatch from sending it twice.
	agentID, _, serr := s.fleet.StartAssignment(ctx, dispatch)
	if agentID != "" && !rec.Prompted {
		if _, uerr := svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
			if r.Prompted {
				return false
			}
			r.Prompted = true
			return true
		}); uerr != nil {
			return rec, uerr
		}
		s.fleet.deliverInstruction(agentID, rec.Assignment.Instruction)
	}
	if agentID == "" {
		reason := "the worker could not be started"
		if serr != nil {
			reason += ": " + serr.Error()
		}
		return fail(contract.CoordinatorFailureUnavailable, reason)
	}
	// The worker exists; its state is written from the same evidence a later
	// read would see, so running means acknowledged and nothing else.
	observed, oerr := s.coordinatorObserveAssignment(task, rec)
	if oerr != nil {
		return rec, oerr
	}
	if serr != nil && observed.Assignment.State == contract.CoordinatorAssignmentFailed {
		return fail(contract.CoordinatorFailureUnavailable, "the worker was started but recording it failed")
	}
	return observed, nil
}

// coordinatorLinkThread links one existing thread for observation, delivering the
// assignment's instruction only when a person directed that thread.
//
// A project agent's identity belongs to the project that owns it, so a thread in
// another project is refused rather than linked: an observation that reached
// across projects would be a link the task's scope does not cover.
func (s *Server) coordinatorLinkThread(task contract.CoordinatorTask, rec coordinatorAssignmentRecord, root string) (coordinatorAssignmentRecord, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return rec, err
	}
	thread := rec.Assignment.Thread
	if thread == nil {
		return svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
			r.Assignment.State = contract.CoordinatorAssignmentFailed
			r.Assignment.Failure = contract.CoordinatorFailureUnknownThread
			r.Assignment.Reason = "the assignment names no thread to link"
			return true
		})
	}
	agent, held := s.fleet.ws.Agent(thread.ID)
	if !held || agent.Project != root {
		return svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
			r.Assignment.State = contract.CoordinatorAssignmentFailed
			r.Assignment.Failure = contract.CoordinatorFailureUnknownThread
			r.Assignment.Reason = "the target project does not hold that thread"
			return true
		})
	}
	if rec.Steer && !rec.Prompted {
		// A person's direction for this thread was verified before the dispatch
		// was recorded; the instruction is handed over once, and the flag is
		// written before it goes so a resume cannot repeat it.
		if _, err := svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
			if r.Prompted {
				return false
			}
			r.Prompted = true
			return true
		}); err != nil {
			return rec, err
		}
		rec.Prompted = true
		s.fleet.deliverInstruction(thread.ID, rec.Assignment.Instruction)
	}
	observed, err := s.coordinatorObserveAssignment(task, rec)
	if err != nil {
		return rec, err
	}
	return observed, nil
}

// coordinatorAssignmentFailureOf classifies one dispatch error for a tool
// caller, so a client can tell "ask the person" from "this target failed".
func coordinatorAssignmentFailureOf(err error) factoryToolFailure {
	var challenge *marshalUserDirectionChallenge
	if errors.As(err, &challenge) {
		return factoryFailureOf(err)
	}
	switch {
	case errors.Is(err, ErrCoordinatorNotFound):
		return factoryToolFailure{Code: factoryCodeNotFound, Message: "no such coordinated task"}
	case errors.Is(err, ErrCoordinatorConflict):
		return factoryToolFailure{Code: factoryCodeConflict, Message: "this request id already dispatched different content, or the task is stopped"}
	case errors.Is(err, ErrCoordinatorInput):
		return factoryToolFailure{Code: factoryCodeInvalidInput, Message: "the dispatch is not valid"}
	case errors.Is(err, ErrCoordinatorUnavailable):
		return factoryToolFailure{Code: factoryCodeUnavailable, Message: "coordinator unavailable"}
	case errors.Is(err, errFactoryDenied):
		return factoryToolFailure{Code: factoryCodeDenied, Message: "not authorized for that project"}
	case errors.Is(err, errMarshalUserDirectionRequired):
		return factoryFailureOf(err)
	}
	return factoryToolFailure{Code: factoryCodeInvalidInput, Message: err.Error()}
}

// coordinatorAssignmentRows renders assignments for a tool result. It is a view
// of the contract types so the coordinator's conversation and the Studio read
// the same fields.
func coordinatorAssignmentRows(assignments []contract.CoordinatorAssignment) []map[string]any {
	rows := make([]map[string]any, 0, len(assignments))
	for _, a := range assignments {
		row := map[string]any{
			"id":             a.ID,
			"taskId":         a.TaskID,
			"projectId":      a.ProjectID,
			"relationship":   string(a.Relationship),
			"state":          string(a.State),
			"resultRevision": a.ResultRevision,
		}
		if a.Instruction != "" {
			row["instruction"] = a.Instruction
		}
		if a.IntakeID != "" {
			row["intakeId"] = a.IntakeID
		}
		if a.Worker != nil {
			row["worker"] = map[string]any{"agentId": a.Worker.AgentID, "sessionId": a.Worker.SessionID}
		}
		if a.Thread != nil {
			row["thread"] = map[string]any{"projectId": a.Thread.ProjectID, "kind": a.Thread.Kind, "id": a.Thread.ID}
		}
		if a.Failure != "" {
			row["failure"] = a.Failure
		}
		if a.Reason != "" {
			row["reason"] = a.Reason
		}
		row["createdAt"] = a.CreatedAt.Format(time.RFC3339)
		row["updatedAt"] = a.UpdatedAt.Format(time.RFC3339)
		rows = append(rows, row)
	}
	return rows
}

// coordinatorAssignmentIDs is the sorted identity list of a set of assignments.
// It exists so a test can say "these are the same assignments" without caring
// about their order.
func coordinatorAssignmentIDs(assignments []contract.CoordinatorAssignment) []string {
	out := make([]string, 0, len(assignments))
	for _, a := range assignments {
		out = append(out, a.ID)
	}
	sort.Strings(out)
	return out
}
