package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
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
	// scope; refusing here is what keeps this phase from widening it, and the
	// status is re-read from the record so a stop recorded a moment ago — by
	// another request, or by a person pressing Stop during this dispatch — is
	// not missed because the caller's copy is older than it.
	if status, gerr := svc.store.Get(task.ID); gerr == nil {
		task = status
	}
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
	out.Assignments = s.coordinatorStampNames(out.Assignments)
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
	// The status an assignment is observed under comes from the record, not the
	// caller's copy: a read taken before a stop would otherwise report a stopped
	// assignment running again, and the stop's own summary would then be the only
	// place that said it stopped.
	if current, gerr := svc.store.Get(task.ID); gerr == nil {
		task.Status = current.Status
	}
	records, err := svc.store.Assignments(task.ID)
	if err != nil {
		return nil, err
	}
	before := make([]contract.CoordinatorAssignment, 0, len(records))
	for _, rec := range records {
		before = append(before, rec.Assignment)
	}
	out := make([]contract.CoordinatorAssignment, 0, len(records))
	for _, rec := range records {
		observed, oerr := s.coordinatorReconcileAssignment(ctx, task, rec, contract.CoordinatorAssignTarget{}, false)
		if oerr != nil {
			return nil, oerr
		}
		out = append(out, observed.Assignment)
	}
	// An observation that learned something is announced on the fleet stream, so
	// a client that is watching receives it without asking again, and the source
	// thread's summary is refreshed to what the worker now reports — a correction
	// made in the worker's own thread is what the eventual result says. A read
	// that changed nothing emits nothing and rewrites nothing.
	out = s.coordinatorStampNames(out)
	if coordinatorAssignmentsDigest(before) != coordinatorAssignmentsDigest(out) {
		delivery, derr := s.coordinatorDeliverSource(task, out)
		if derr != nil {
			slog.Default().Warn("webbridge: coordinator delivery not refreshed", "task", task.ID, "err", derr)
		}
		if delivery == nil {
			if rec, held, rerr := svc.store.Delivery(task.ID); rerr == nil && held {
				delivery = &rec
			}
		}
		s.coordinatorEmitUpdate(task, out, delivery)
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
	// A settled stop is not something an observation may undo. A worker this task
	// cancelled still has its agent record, so observing it would otherwise report
	// the assignment running again the moment the stop was answered — and the
	// record would then disagree with the summary the stop posted.
	if rec.Assignment.State == contract.CoordinatorAssignmentStopped &&
		task.Status != contract.CoordinatorTaskOpen {
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
	// A task that stopped while this dispatch was being attempted starts nothing.
	// The caller took this assignment's dispatch lock, so the stop either finished
	// before it (and the assignment already reads stopped) or waits for it — and
	// the status is read from the record rather than the caller's copy, which may
	// predate the stop.
	if latest, gerr := svc.store.Get(task.ID); gerr == nil {
		task = latest
	}
	if task.Status != contract.CoordinatorTaskOpen {
		return svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
			if r.Assignment.State == contract.CoordinatorAssignmentStopped &&
				r.Assignment.Reason == coordinatorStopReasonUnstarted {
				return false
			}
			r.Assignment.State = contract.CoordinatorAssignmentStopped
			r.Assignment.Failure = ""
			r.Assignment.Reason = coordinatorStopReasonUnstarted
			r.Assignment.IntakeID = ""
			return true
		})
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

// The stop path: one coordinated task asked to stop, at the scope that request
// names.
//
// It has the same three jobs a dispatch has, in the order that makes a stop
// trustworthy.
//
// First, durability. The stop is written to the task before anything is
// cancelled (coordinatorStore.SetTaskStatus): while the record says stopping no
// new assignment may be dispatched for the task, and a bridge that dies here
// resumes from the record rather than from a cancellation that may never have
// been sent. Only then are children touched.
//
// Second, scope. A stop cancels the workers *this task created* — the agents
// whose own records carry this task's and assignment's identity — and withdraws
// the pending intake whose identity is this task's. A linked thread was not
// started by the task, and a person who directed it owns its Stop: stopping a
// coordinated task leaves that thread running, and its assignment keeps reading
// linked. A failed assignment is likewise a settled outcome and keeps its
// failure, so the summary can name it.
//
// Third, reconciliation. A dispatch racing the stop is an acknowledgment racing
// it: the resolve path re-reads the task under the assignment's lock before it
// starts a worker and again before it delivers the instruction, and the stop
// takes the same per-assignment lock, so whatever order they interleave in, one
// outcome is written — a worker that appeared during the stop is cancelled
// rather than left running under a stopped task.

// coordinatorStopTask stops one coordinated task: durable intent first, then the
// task's own children, then the settled state and the result it reports back.
//
// It is idempotent. A task already stopped answers with the state the first stop
// left, so a repeat after a reconnect — or a second press — cancels nothing a
// second time and posts no second summary.
func (s *Server) coordinatorStopTask(ctx context.Context, task contract.CoordinatorTask) (contract.CoordinatorStopResult, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return contract.CoordinatorStopResult{}, err
	}
	if s.fleet == nil || s.fleet.ws == nil {
		return contract.CoordinatorStopResult{}, ErrCoordinatorUnavailable
	}
	// One stop per task at a time: a repeat waits for the one in flight and then
	// answers with what it settled, rather than cancelling the same workers
	// beside it.
	release := svc.lockTask(task.ID)
	defer release()
	current, err := svc.store.Get(task.ID)
	if err != nil {
		return contract.CoordinatorStopResult{}, err
	}
	current.Host = s.coordinatorHost()
	if current.Status != contract.CoordinatorTaskOpen && current.Status != contract.CoordinatorTaskStopping {
		// Already stopped: the answer is the state the stop left, not a second
		// one. This is what makes a repeat after a reconnect idempotent.
		return s.coordinatorStopSnapshot(ctx, current)
	}
	// The intent is durable before a child is touched.
	stopping, _, err := svc.store.SetTaskStatus(task.ID, contract.CoordinatorTaskStopping)
	if err != nil {
		return contract.CoordinatorStopResult{}, err
	}
	stopping.Host = current.Host
	unconfirmed := s.coordinatorStopOwnedWork(ctx, stopping)
	settled := stopping
	if !unconfirmed {
		settled, _, err = svc.store.SetTaskStatus(task.ID, contract.CoordinatorTaskStopped)
		if err != nil {
			return contract.CoordinatorStopResult{}, err
		}
	}
	settled.Host = current.Host
	// The assignments as the stop left them, and the result the source thread
	// reads.
	//
	// The summary is posted here rather than inferred from the read that follows:
	// the stop wrote the assignment states before this read, so the read sees no
	// change and would post nothing. PutDelivery is idempotent on the task's
	// stable event id, so the first stop posts one summary and a repeat — even one
	// resumed after a restart — updates that record rather than adding another.
	assignments, err := s.coordinatorTaskAssignments(ctx, settled)
	if err != nil {
		return contract.CoordinatorStopResult{}, err
	}
	delivery, derr := s.coordinatorDeliverSource(settled, assignments)
	if derr != nil {
		slog.Default().Warn("webbridge: coordinator stop summary not stored", "task", settled.ID, "err", derr)
	}
	if delivery == nil {
		if rec, held, rerr := svc.store.Delivery(settled.ID); rerr == nil && held {
			delivery = &rec
		}
	}
	s.coordinatorEmitUpdate(settled, assignments, delivery)
	out := contract.CoordinatorStopResult{Task: settled, Assignments: assignments, Delivery: delivery}
	return out, nil
}

// coordinatorStopSnapshot is the answer a repeat stop gets: the settled task, its
// assignments as they stand, and the result it already reported.
//
// It announces nothing new — the summary is the one the first stop wrote — so a
// repeat after a reconnect costs a read and no event.
func (s *Server) coordinatorStopSnapshot(ctx context.Context, task contract.CoordinatorTask) (contract.CoordinatorStopResult, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return contract.CoordinatorStopResult{}, err
	}
	assignments, err := s.coordinatorTaskAssignments(ctx, task)
	if err != nil {
		return contract.CoordinatorStopResult{}, err
	}
	out := contract.CoordinatorStopResult{Task: task, Assignments: assignments}
	if delivery, held, derr := svc.store.Delivery(task.ID); derr == nil && held {
		out.Delivery = &delivery
	}
	return out, nil
}

// coordinatorStopOwnedWork cancels the workers this task created and withdraws
// the pending intake it owns, reporting whether every cancellation could be
// confirmed.
//
// It takes each assignment's dispatch lock, so a dispatch racing the stop either
// finished before this pass (and its worker is cancelled here) or runs after it
// and reads the stopped task (and starts nothing). A linked thread is not
// touched at all: the task did not start it, and its own Stop is its own.
func (s *Server) coordinatorStopOwnedWork(ctx context.Context, task contract.CoordinatorTask) (unconfirmed bool) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return true
	}
	records, err := svc.store.Assignments(task.ID)
	if err != nil {
		return true
	}
	for _, rec := range records {
		if s.coordinatorStopAssignment(ctx, task, rec.Assignment.ID) {
			unconfirmed = true
		}
	}
	return unconfirmed
}

// coordinatorStopAssignment settles one assignment of a stopping task and
// reports whether its cancellation could not be confirmed.
//
// A settled outcome is left alone: a failed or expired assignment keeps the
// failure it already has, and a linked thread keeps its link, because neither is
// work the task may cancel.
func (s *Server) coordinatorStopAssignment(ctx context.Context, task contract.CoordinatorTask, assignmentID string) (unconfirmed bool) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return true
	}
	release := svc.lockAssignment(assignmentID)
	defer release()
	rec, held, err := svc.store.Assignment(task.ID, assignmentID)
	if err != nil || !held {
		return err != nil
	}
	if rec.Assignment.Relationship == contract.CoordinatorAssignmentThread {
		// A linked thread was not started by this task, and — directed or only
		// observed — it keeps its own Stop. Nothing here touches it.
		return false
	}
	if rec.Assignment.State == contract.CoordinatorAssignmentFailed ||
		rec.Assignment.State == contract.CoordinatorAssignmentExpired {
		return false
	}
	// The submission this task stored for the assignment is withdrawn, not left
	// for a person to confirm into a stopped task. Its identity is the
	// assignment's own, so a submission that belongs to another task is never
	// touched.
	if withdrawn := s.coordinatorWithdrawSubmission(task, rec); withdrawn {
		_, uerr := svc.store.UpdateAssignment(task.ID, assignmentID, func(r *coordinatorAssignmentRecord) bool {
			if r.Assignment.State == contract.CoordinatorAssignmentStopped {
				return false
			}
			r.Assignment.State = contract.CoordinatorAssignmentStopped
			r.Assignment.Failure = ""
			r.Assignment.Reason = coordinatorStopReasonWithdrawn
			r.Assignment.IntakeID = ""
			return true
		})
		if uerr != nil {
			return true
		}
		return false
	}
	worker, hasWorker := s.coordinatorWorkerFor(task.ID, assignmentID)
	if hasWorker {
		// A worker whose work already left the workspace has finished: its turn is
		// not cancelled, because cancelling it would name delivered work as
		// cancelled. The assignment settles as stopped and says what it delivered,
		// so the summary names completed work as completed.
		if coordinatorWorkerDelivered(worker) {
			_, uerr := svc.store.UpdateAssignment(task.ID, assignmentID, func(r *coordinatorAssignmentRecord) bool {
				if r.Assignment.State == contract.CoordinatorAssignmentStopped &&
					r.Assignment.Reason == coordinatorStopReasonDelivered {
					return false
				}
				r.Assignment.State = contract.CoordinatorAssignmentStopped
				r.Assignment.Failure = ""
				r.Assignment.Reason = coordinatorStopReasonDelivered
				r.Assignment.IntakeID = ""
				return true
			})
			return uerr != nil
		}
		if cerr := s.coordinatorCancelWorker(ctx, worker); cerr != nil {
			// The cancellation could not be confirmed, so the task stays
			// stopping and the assignment keeps what it said: a stopped task
			// whose worker may still be running would be a claim nothing
			// supports.
			return true
		}
		_, uerr := svc.store.UpdateAssignment(task.ID, assignmentID, func(r *coordinatorAssignmentRecord) bool {
			if r.Assignment.State == contract.CoordinatorAssignmentStopped &&
				r.Assignment.Reason == coordinatorStopReasonWorker {
				return false
			}
			r.Assignment.State = contract.CoordinatorAssignmentStopped
			r.Assignment.Failure = ""
			r.Assignment.Reason = coordinatorStopReasonWorker
			r.Assignment.IntakeID = ""
			return true
		})
		if uerr != nil {
			return true
		}
		return false
	}
	// Neither a submission nor a worker: an assignment the task had not started.
	// Stopping it spends its dispatch identity, which is why it is written rather
	// than left dispatching for a later dispatch to resume.
	_, err = svc.store.UpdateAssignment(task.ID, assignmentID, func(r *coordinatorAssignmentRecord) bool {
		if r.Assignment.State == contract.CoordinatorAssignmentStopped &&
			r.Assignment.Reason == coordinatorStopReasonUnstarted {
			return false
		}
		r.Assignment.State = contract.CoordinatorAssignmentStopped
		r.Assignment.Failure = ""
		r.Assignment.Reason = coordinatorStopReasonUnstarted
		r.Assignment.IntakeID = ""
		r.IntakeExpiresAt = time.Time{}
		return true
	})
	return err != nil
}

// The reasons a stopped assignment carries. They are display text a person reads
// in the row and in the summary, and they are compared when a repeat stop decides
// whether it changed anything.
const (
	coordinatorStopReasonWorker    = "its worker was cancelled when the task stopped"
	coordinatorStopReasonWithdrawn = "its submission was withdrawn when the task stopped"
	coordinatorStopReasonUnstarted = "it was stopped before any work started"
	coordinatorStopReasonDelivered = "its work was already delivered when the task stopped"
)

// coordinatorWorkerDelivered reports whether one worker's work already left the
// workspace: it pushed its branch or opened a pull request.
//
// A stop settles such an assignment as stopped — the task is over — but names what
// it delivered rather than counting it as cancelled, because cancelling a finished
// turn would report completed work as work that was stopped.
func coordinatorWorkerDelivered(worker Agent) bool {
	return !worker.PushedAt.IsZero() || strings.TrimSpace(worker.PRUrl) != ""
}

// coordinatorWithdrawSubmission removes the pending submission one assignment's
// stop withdraws, reporting whether there was one.
//
// Ownership is checked against the submission's own identity: a submission that
// names another task or another assignment is not this task's to withdraw, and is
// left where it is.
func (s *Server) coordinatorWithdrawSubmission(task contract.CoordinatorTask, rec coordinatorAssignmentRecord) bool {
	if s.fleet == nil || s.fleet.ws == nil {
		return false
	}
	id := strings.TrimSpace(rec.Assignment.IntakeID)
	if id == "" {
		id = strings.TrimSpace(rec.Assignment.ID)
	}
	pending, held := s.fleet.ws.PendingByID(id)
	if !held {
		return false
	}
	if pending.TaskID != task.ID || (pending.AssignmentID != "" && pending.AssignmentID != rec.Assignment.ID) {
		return false
	}
	if err := s.fleet.ws.DeletePending(pending.ID); err != nil {
		return false
	}
	s.fleet.auditf(AuditEvent{Event: AuditPendingDenied, OwnerID: DefaultOwnerID,
		AgentID: pending.AssignmentID, ClientID: pending.ClientID, RepoID: pending.RepoID,
		Origin: pending.Origin})
	return true
}

// coordinatorCancelWorker cancels the in-flight turn of one worker this task
// created, so a stop reaches the work rather than only the record.
//
// The worker's own record is the authority for what may be cancelled: an agent
// that carries another task's or another assignment's identity is not this
// stop's, and cancel is never sent to it. A worker with no running runtime has
// nothing in flight, which is a cancellation that is already true.
func (s *Server) coordinatorCancelWorker(ctx context.Context, worker Agent) error {
	agent, held := s.fleet.ws.Agent(worker.ID)
	if !held {
		// The record is gone, so there is no turn left to cancel.
		return nil
	}
	if agent.CoordinatorTaskID != worker.CoordinatorTaskID || agent.CoordinatorAssignmentID != worker.CoordinatorAssignmentID ||
		strings.TrimSpace(agent.CoordinatorAssignmentID) == "" {
		return fmt.Errorf("%w: worker ownership unconfirmed", ErrCoordinatorConflict)
	}
	if strings.TrimSpace(agent.SessionID) == "" {
		// It acknowledged no session, so it is running nothing.
		return nil
	}
	rt, err := s.fleet.runtimeForAgent(agent.ID)
	if err != nil {
		if errors.Is(err, ErrUnknownAgent) {
			// No runtime: the worker is paused or ended, and there is no turn in
			// flight to cancel.
			return nil
		}
		return err
	}
	if err := rt.reg.Cancel(ctx, agent.SessionID); err != nil {
		if errors.Is(err, ErrUnknownSession) {
			return nil
		}
		return err
	}
	return nil
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

// coordinatorStampNames fills each assignment's display project name from this
// host's catalog.
//
// The name is derived rather than stored, so an assignment reads under whatever
// the project is called now, and the daemon — which owns the catalog — is the one
// that answers it. A project this host no longer holds keeps its id and no name.
func (s *Server) coordinatorStampNames(assignments []contract.CoordinatorAssignment) []contract.CoordinatorAssignment {
	if len(assignments) == 0 {
		return assignments
	}
	catalog := s.coordinatorProjectCatalog()
	for i := range assignments {
		assignments[i].ProjectName = catalog[assignments[i].ProjectID]
	}
	return assignments
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

// The handoff path: a project thread's work handed to Marshal, reported back to
// the thread it came from.
//
// A handoff is the assignment path with a source. The invoking session supplies
// the source — never an argument — and the task the handoff opens (or joins, when
// that thread already hands work off) is the identity every assignment, every
// delivery and every read resolves against. A request made in Marshal's own
// conversation is the same path with no source: it dispatches onto the task that
// session already owns and reports back to nobody.
//
// Targets are names a person used ("the CLI") or canonical project ids. A name is
// resolved here against the projects this host registered; a name that matches
// more than one — or none — asks one question and dispatches nothing, so an
// ambiguous target never starts work in the wrong project.

// coordinatorProjectCatalog is every project this host registered, by canonical
// id, with the display name a person reads (the last segment of its root).
//
// It is what a handoff target's name is resolved against. An empty catalog is an
// empty host, which is not the same as a project with no name.
func (s *Server) coordinatorProjectCatalog() map[string]string {
	out := map[string]string{}
	if s.fleet == nil || s.fleet.ws == nil {
		return out
	}
	for _, root := range s.fleet.ws.Projects() {
		id, ok := s.fleet.ws.ProjectID(root)
		if !ok {
			continue
		}
		out[id] = coordinatorProjectName(root)
	}
	return out
}

// coordinatorProjectName is the display name of one project root: its last
// segment, which is what a person says and what a handoff target may quote.
func coordinatorProjectName(root string) string {
	cleaned := filepath.Clean(root)
	// A root that is a filesystem root has no last segment to name; the whole
	// path is then the name, which is still no path disclosure because it came
	// from the person.
	if base := filepath.Base(cleaned); base != "" && base != string(filepath.Separator) {
		return base
	}
	return cleaned
}

// coordinatorNameKey is what a target name is compared by: lowercased, trimmed,
// with the leading article a person puts in front of a project dropped, so "the
// CLI", "CLI" and "cli" are one name.
func coordinatorNameKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.TrimPrefix(key, "the ")
	key = strings.TrimSuffix(key, "'s")
	return strings.TrimSpace(key)
}

// coordinatorResolveHandoffTargets resolves the targets a handoff names to
// canonical dispatch targets.
//
// A target that already names a canonical id is used as it is, provided this host
// registered it. A target that names a project is matched by display name: an
// exact match wins, then the single project whose name contains what was said. A
// name that matches more than one project, or none, is the one clarifying
// question this handoff asks, and its candidates are the projects it could mean —
// so the person's answer can name one by id.
//
// Nothing here dispatches, and nothing here reads a path: a target resolved to a
// project id is the only thing the dispatch path is given.
func (s *Server) coordinatorResolveHandoffTargets(targets []contract.CoordinatorHandoffTarget, outcome, findings string) ([]contract.CoordinatorAssignTarget, *contract.CoordinatorClarification, error) {
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("%w: a handoff names at least one target", ErrCoordinatorInput)
	}
	catalog := s.coordinatorProjectCatalog()
	names := make([]string, 0, len(catalog))
	for id, name := range catalog {
		names = append(names, id+"\x00"+name)
	}
	sort.Strings(names)
	candidates := make([]contract.CoordinatorTargetCandidate, 0, len(names))
	for _, entry := range names {
		parts := strings.SplitN(entry, "\x00", 2)
		candidates = append(candidates, contract.CoordinatorTargetCandidate{ProjectID: parts[0], Name: parts[1]})
	}

	resolved := make([]contract.CoordinatorAssignTarget, 0, len(targets))
	seen := map[string]bool{}
	for _, target := range targets {
		projectID := strings.TrimSpace(target.ProjectID)
		name := strings.TrimSpace(target.Project)
		if projectID == "" {
			matched, askedName := coordinatorMatchProject(catalog, name)
			if matched == "" || askedName {
				// An ambiguous or unknown name is answered, not guessed at and
				// not widened: the question names the projects it could mean.
				return nil, coordinatorClarificationFor(name, matched, candidates), nil
			}
			projectID = matched
		} else if _, ok := catalog[projectID]; !ok {
			return nil, coordinatorClarificationFor(projectID, "", candidates), nil
		}
		if seen[projectID] {
			// One project is one assignment, however many times it was named.
			continue
		}
		seen[projectID] = true
		resolved = append(resolved, contract.CoordinatorAssignTarget{
			ProjectID:   projectID,
			Instruction: coordinatorHandoffInstruction(outcome, findings, target),
			ThreadID:    strings.TrimSpace(target.ThreadID),
			Steer:       target.Steer && strings.TrimSpace(target.ThreadID) != "",
		})
	}
	if len(resolved) == 0 {
		return nil, nil, fmt.Errorf("%w: a handoff names at least one target this host holds", ErrCoordinatorInput)
	}
	return resolved, nil, nil
}

// coordinatorMatchProject resolves one display name to exactly one project.
//
// It returns the match and whether the match was ambiguous (which the caller
// turns into a question). An exact name wins over a partial one, and a partial
// name that matches two projects is ambiguous rather than the first of them.
func coordinatorMatchProject(catalog map[string]string, name string) (string, bool) {
	key := coordinatorNameKey(name)
	if key == "" {
		return "", false
	}
	exact := ""
	for id, display := range catalog {
		if coordinatorNameKey(display) != key {
			continue
		}
		if exact != "" && exact != id {
			return "", true
		}
		exact = id
	}
	if exact != "" {
		return exact, false
	}
	partial := make([]string, 0, 2)
	for id, display := range catalog {
		if strings.Contains(coordinatorNameKey(display), key) {
			partial = append(partial, id)
		}
	}
	switch len(partial) {
	case 0:
		return "", false
	case 1:
		return partial[0], false
	default:
		return "", true
	}
}

// coordinatorClarificationFor is the one question a handoff asks about a target
// it could not resolve, with the projects it could have meant.
func coordinatorClarificationFor(name, matched string, candidates []contract.CoordinatorTargetCandidate) *contract.CoordinatorClarification {
	listed := candidates
	if len(listed) > 12 {
		listed = listed[:12]
	}
	labels := make([]string, 0, len(listed))
	for _, candidate := range listed {
		labels = append(labels, fmt.Sprintf("%s (%s)", candidate.Name, candidate.ProjectID))
	}
	question := fmt.Sprintf("Which project did you mean by %q? This host holds: %s", name, strings.Join(labels, ", "))
	if matched == "" && len(labels) == 0 {
		question = fmt.Sprintf("Which project did you mean by %q? This host holds no project yet.", name)
	}
	return &contract.CoordinatorClarification{Name: name, Question: question, Candidates: listed}
}

// coordinatorHandoffInstruction is what one target is asked to do: its own
// instruction when it stated one, the handoff's outcome when it did not, and the
// findings the source gathered beneath it.
//
// The findings travel with every target because they are the evidence the source
// already collected: a target that has to rediscover it would duplicate work the
// handoff exists to avoid. They are text the source wrote, never markup.
func coordinatorHandoffInstruction(outcome, findings string, target contract.CoordinatorHandoffTarget) string {
	body := strings.TrimSpace(target.Instruction)
	if body == "" {
		body = strings.TrimSpace(outcome)
	}
	if evidence := strings.TrimSpace(findings); evidence != "" {
		body += "\n\nWhat the source found:\n" + evidence
	}
	return body
}

// coordinatorSourceFor is the source thread one built-in session hands work off
// from: the fleet agent whose session this is, in the project its binding names.
//
// The binding is the identity the transport verified, so a caller cannot hand
// work off from a thread it does not hold. A session no agent owns has no thread
// to link, and asks the caller to use the task it already has instead.
func (s *Server) coordinatorSourceFor(binding MarshalSessionBinding) (*contract.CoordinatorThreadRef, error) {
	if s.fleet == nil || s.fleet.ws == nil {
		return nil, ErrCoordinatorUnavailable
	}
	root, ok := s.fleet.ws.ProjectRootByID(binding.ProjectID)
	if !ok {
		return nil, errFactoryDenied
	}
	for _, agent := range s.fleet.ws.Agents() {
		if agent.SessionID != binding.SessionID || agent.Project != root {
			continue
		}
		return &contract.CoordinatorThreadRef{ProjectID: binding.ProjectID, Kind: "agent", ID: agent.ID}, nil
	}
	return nil, fmt.Errorf("%w: this session is not a thread in its project", ErrCoordinatorInput)
}

// coordinatorHandoff is one structured handoff: a source thread's request for
// work in the projects it names.
//
// It is the same assignment path the coordinator's own dispatch takes. The only
// differences are where the identity comes from and what happens after: the
// source is the invoking session (or, for a request made in Marshal's own
// conversation, no source at all), and a task with a source posts one
// informational update back to it.
func (s *Server) coordinatorHandoff(ctx context.Context, binding MarshalSessionBinding, req contract.CoordinatorHandoffRequest, receiptID string) (contract.CoordinatorHandoffResult, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return contract.CoordinatorHandoffResult{}, err
	}
	if s.fleet == nil || s.fleet.ws == nil {
		return contract.CoordinatorHandoffResult{}, ErrCoordinatorUnavailable
	}
	outcome := strings.TrimSpace(req.Outcome)
	if outcome == "" {
		return contract.CoordinatorHandoffResult{}, fmt.Errorf("%w: a handoff says what it asks for", ErrCoordinatorInput)
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return contract.CoordinatorHandoffResult{}, fmt.Errorf("%w: requestId is required", ErrCoordinatorInput)
	}

	// A request made in Marshal's own conversation already has its task: it is
	// the same assignment path with no source, so nothing is linked and nothing
	// is reported back.
	if task, ok := svc.store.BySession(binding.SessionID); ok {
		task.Host = s.coordinatorHost()
		resolved, clarification, rerr := s.coordinatorResolveHandoffTargets(req.Targets, outcome, req.Findings)
		if rerr != nil {
			return contract.CoordinatorHandoffResult{}, rerr
		}
		if clarification != nil {
			return contract.CoordinatorHandoffResult{TaskID: task.ID, Host: task.Host, Clarification: clarification, Assignments: []contract.CoordinatorAssignment{}}, nil
		}
		result, derr := s.coordinatorDispatch(ctx, task, contract.CoordinatorAssignRequest{RequestID: requestID, Targets: resolved}, receiptID)
		if derr != nil {
			return contract.CoordinatorHandoffResult{TaskID: task.ID, Assignments: result.Assignments}, derr
		}
		s.coordinatorEmitUpdate(task, result.Assignments, nil)
		return contract.CoordinatorHandoffResult{TaskID: task.ID, Host: task.Host, Assignments: result.Assignments}, nil
	}

	source, err := s.coordinatorSourceFor(binding)
	if err != nil {
		return contract.CoordinatorHandoffResult{}, err
	}
	resolved, clarification, err := s.coordinatorResolveHandoffTargets(req.Targets, outcome, req.Findings)
	if err != nil {
		return contract.CoordinatorHandoffResult{}, err
	}
	if clarification != nil {
		return contract.CoordinatorHandoffResult{Host: s.coordinatorHost(), Source: source, Clarification: clarification, Assignments: []contract.CoordinatorAssignment{}}, nil
	}
	projectIDs := make([]string, 0, len(resolved))
	for _, target := range resolved {
		projectIDs = append(projectIDs, target.ProjectID)
	}
	task, err := s.coordinatorHandoffTask(svc, *source, requestID, outcome, projectIDs)
	if err != nil {
		return contract.CoordinatorHandoffResult{Host: s.coordinatorHost(), Source: source}, err
	}
	task.Host = s.coordinatorHost()
	result, err := s.coordinatorDispatch(ctx, task, contract.CoordinatorAssignRequest{RequestID: requestID, Targets: resolved}, receiptID)
	if err != nil {
		return contract.CoordinatorHandoffResult{TaskID: task.ID, Host: task.Host, Source: source, Assignments: result.Assignments}, err
	}
	// Reporting back is part of the handoff, but it is not allowed to undo one:
	// the work is already dispatched, and a delivery the store could not write
	// is logged rather than reported as a dispatch failure.
	delivery, derr := s.coordinatorDeliverSource(task, result.Assignments)
	if derr != nil {
		slog.Default().Warn("webbridge: coordinator delivery not stored", "task", task.ID, "err", derr)
	}
	s.coordinatorEmitUpdate(task, result.Assignments, delivery)
	out := contract.CoordinatorHandoffResult{TaskID: task.ID, Host: task.Host, Source: source, Assignments: result.Assignments}
	if delivery != nil {
		out.Delivery = delivery
	}
	return out, nil
}

// coordinatorHandoffTask is the task one source thread's handoff runs under: the
// open task that thread already handed work to, widened to cover the targets it
// names now, or a new one.
//
// The source is linked once. A second handoff from the same thread joins the
// existing task, so the assignments, the delivery and the Studio's view of the
// thread all keep resolving to one identity; the task's scope is widened rather
// than a second task opened beside it.
func (s *Server) coordinatorHandoffTask(svc *coordinatorService, source contract.CoordinatorThreadRef, requestID, outcome string, projectIDs []string) (contract.CoordinatorTask, error) {
	if existing, ok := svc.store.SourceTask(source); ok {
		widened, err := svc.store.WidenScope(existing.ID, projectIDs)
		if err != nil {
			return contract.CoordinatorTask{}, err
		}
		return widened, nil
	}
	created, _, err := svc.store.Create(contract.CoordinatorCreateRequest{
		// The handoff's own request id is the idempotency key, prefixed so it
		// cannot collide with a create a Studio's first send made.
		RequestID: "handoff:" + requestID,
		Title:     outcome,
		Source:    &source,
		Projects:  projectIDs,
	})
	return created, err
}

// coordinatorDeliveryID is the stable event identity of one task's source
// delivery.
//
// It is derived from the task alone, so every attempt at reporting the same task
// posts one event: a retry, a reconnect and a second observation all resolve to
// this id, and the store replaces the record instead of appending a summary.
func coordinatorDeliveryID(taskID string) string {
	sum := sha256.Sum256([]byte(taskID))
	return "coord-delivery-" + hex.EncodeToString(sum[:12])
}

// coordinatorDeliverSource records (and announces) the informational update one
// task posts back to its source thread.
//
// It is not an agent turn: nothing is prompted, no worker is started and no
// session is created, so the delivery reaches a thread with no viewer attached
// and an idle one alike. Visibility is rechecked here, immediately before the
// record is written: a source project whose access was revoked, or whose thread
// the project no longer holds, gets no summary rather than a stale one.
func (s *Server) coordinatorDeliverSource(task contract.CoordinatorTask, assignments []contract.CoordinatorAssignment) (*contract.CoordinatorDelivery, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return nil, err
	}
	source := task.Source
	if source == nil {
		return nil, nil
	}
	if !s.coordinatorSourceVisible(task) {
		return nil, nil
	}
	delivery, changed, err := svc.store.PutDelivery(contract.CoordinatorDelivery{
		TaskID:  task.ID,
		Source:  source,
		EventID: coordinatorDeliveryID(task.ID),
		Text:    s.coordinatorDeliveryText(task, assignments),
	})
	if err != nil {
		return nil, err
	}
	if !changed {
		// The record already said this: no second summary is posted.
		return &delivery, nil
	}
	return &delivery, nil
}

// coordinatorSourceVisible rechecks that one task's source is still the thing a
// summary may be written about: the project is one this host holds, the task may
// still read it, and — for a thread — the project still holds that agent.
func (s *Server) coordinatorSourceVisible(task contract.CoordinatorTask) bool {
	source := task.Source
	if source == nil || s.fleet == nil || s.fleet.ws == nil {
		return false
	}
	root, ok := s.fleet.ws.ProjectRootByID(source.ProjectID)
	if !ok {
		return false
	}
	if !s.coordinatorReadAllowed(task, source.ProjectID) {
		return false
	}
	if source.Kind != "agent" {
		return true
	}
	agent, held := s.fleet.ws.Agent(source.ID)
	return held && agent.Project == root
}

// coordinatorDeliveryText is one task's result as the source thread reads it:
// what each assignment's project is called, where it stands, and what its
// newest observed result says.
//
// It is written for a person and names no host path. A worker's latest facts come
// from the record it wrote itself — its branch, its pull request, whether it has
// pushed, what it is doing — so a correction the person made in the worker's own
// thread is what the summary reports, not what the assignment originally asked
// for.
func (s *Server) coordinatorDeliveryText(task contract.CoordinatorTask, assignments []contract.CoordinatorAssignment) string {
	catalog := s.coordinatorProjectCatalog()
	projectName := func(id string) string {
		if name := catalog[id]; name != "" {
			return name
		}
		if len(id) <= 8 {
			return id
		}
		return id[:8]
	}
	if len(assignments) == 0 {
		return "Marshal is linked to this thread; no assignment has been dispatched yet."
	}
	parts := make([]string, 0, len(assignments))
	for _, a := range assignments {
		row := projectName(a.ProjectID) + ": " + coordinatorAssignmentStateWords(a)
		if facts := s.coordinatorAssignmentFacts(a); len(facts) > 0 {
			row += " — " + strings.Join(facts, ", ")
		}
		parts = append(parts, row)
	}
	// A stopped task says so first, because the stop is what the person did and
	// every row below it stands in that light: a cancelled worker, a withdrawn
	// submission, a failure that stays a failure and a linked thread that keeps
	// running under its own Stop are different outcomes, and the sentence names
	// each as the one it is.
	count := fmt.Sprintf("%d %s", len(assignments), plural(len(assignments), "assignment", "assignments"))
	body := strings.Join(parts, "; ") + "."
	switch task.Status {
	case contract.CoordinatorTaskStopping:
		return fmt.Sprintf("Marshal: stopping this task, with %s. %s", count, body)
	case contract.CoordinatorTaskStopped:
		return fmt.Sprintf("Marshal: this task stopped, with %s. %s", count, body)
	}
	return fmt.Sprintf("Marshal: %s. %s", count, body)
}

// coordinatorAssignmentStateWords is one assignment's state in a sentence.
func coordinatorAssignmentStateWords(a contract.CoordinatorAssignment) string {
	switch a.State {
	case contract.CoordinatorAssignmentDispatching:
		return "starting"
	case contract.CoordinatorAssignmentPending:
		return "waiting for your confirmation"
	case contract.CoordinatorAssignmentRunning:
		return "running"
	case contract.CoordinatorAssignmentLinked:
		return "linked and observed"
	case contract.CoordinatorAssignmentFailed:
		if strings.TrimSpace(a.Reason) != "" {
			return "failed: " + a.Reason
		}
		return "failed"
	case contract.CoordinatorAssignmentExpired:
		return "expired before anyone confirmed it"
	case contract.CoordinatorAssignmentStopped:
		// A stopped assignment carries why it stopped: a cancelled worker, a
		// withdrawn submission, work that had already been delivered and an
		// assignment that never started are different outcomes, and the summary
		// names each as the one it is.
		if strings.TrimSpace(a.Reason) != "" {
			return "stopped: " + a.Reason
		}
		return "stopped"
	}
	return string(a.State)
}

// coordinatorAssignmentFacts is what one assignment's newest result says, in the
// few words a summary can carry: where its worker is, where it pushed, and what
// it is doing now.
func (s *Server) coordinatorAssignmentFacts(a contract.CoordinatorAssignment) []string {
	agentID := ""
	if a.Worker != nil {
		agentID = a.Worker.AgentID
	} else if a.Thread != nil {
		agentID = a.Thread.ID
	}
	if agentID == "" || s.fleet == nil || s.fleet.ws == nil {
		return nil
	}
	agent, ok := s.fleet.ws.Agent(agentID)
	if !ok {
		return nil
	}
	facts := make([]string, 0, 4)
	if branch := strings.TrimSpace(agent.Branch); branch != "" {
		facts = append(facts, "on "+branch)
	}
	if pr := strings.TrimSpace(agent.PRUrl); pr != "" {
		facts = append(facts, "pull request "+pr)
	}
	if !agent.PushedAt.IsZero() {
		facts = append(facts, "pushed")
	}
	if live := s.fleet.live.get(agentID); strings.TrimSpace(live.activity) != "" {
		fact := "last " + live.activity
		if live.changedFiles > 0 {
			fact += fmt.Sprintf(" (%d %s changed)", live.changedFiles, plural(live.changedFiles, "file", "files"))
		}
		facts = append(facts, fact)
	}
	if a.ResultRevision > 0 {
		facts = append(facts, fmt.Sprintf("result %d", a.ResultRevision))
	}
	return facts
}

// plural picks the singular or plural word for a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// coordinatorRetryAssignment attempts one failed or expired assignment again,
// under its existing identity.
//
// It is the scoped retry a person's failure row offers: only this assignment is
// reset, its siblings are untouched, and the dispatch identity is the one it
// already has — so the retry can start one worker, never a second. A steering
// target owes the person's direction again, verified before anything is reset.
func (s *Server) coordinatorRetryAssignment(ctx context.Context, task contract.CoordinatorTask, assignmentID, receiptID string) (contract.CoordinatorAssignment, error) {
	svc, err := s.coordinatorAvailable()
	if err != nil {
		return contract.CoordinatorAssignment{}, err
	}
	if task.Status != contract.CoordinatorTaskOpen {
		return contract.CoordinatorAssignment{}, ErrCoordinatorConflict
	}
	rec, held, err := svc.store.Assignment(task.ID, assignmentID)
	if err != nil {
		return contract.CoordinatorAssignment{}, err
	}
	if !held {
		return contract.CoordinatorAssignment{}, ErrCoordinatorNotFound
	}
	if rec.Assignment.State != contract.CoordinatorAssignmentFailed &&
		rec.Assignment.State != contract.CoordinatorAssignmentExpired {
		return contract.CoordinatorAssignment{}, ErrCoordinatorConflict
	}
	target := contract.CoordinatorAssignTarget{ProjectID: rec.Assignment.ProjectID, Instruction: rec.Assignment.Instruction}
	if rec.Assignment.Thread != nil {
		target.ThreadID = rec.Assignment.Thread.ID
		target.Steer = rec.Steer
	}
	if err := s.coordinatorAuthorizeSteering(task, []contract.CoordinatorAssignTarget{target}, assignmentID+"-retry", receiptID); err != nil {
		return contract.CoordinatorAssignment{}, err
	}
	reset, err := svc.store.UpdateAssignment(task.ID, rec.Assignment.ID, func(r *coordinatorAssignmentRecord) bool {
		if r.Assignment.State != contract.CoordinatorAssignmentFailed &&
			r.Assignment.State != contract.CoordinatorAssignmentExpired {
			return false
		}
		r.Assignment.State = contract.CoordinatorAssignmentDispatching
		r.Assignment.Failure, r.Assignment.Reason, r.Assignment.IntakeID = "", "", ""
		r.IntakeExpiresAt = time.Time{}
		// The attempt that failed delivered nothing, so the resumed dispatch
		// may hand the instruction over once.
		r.Prompted = false
		return true
	})
	if err != nil {
		return contract.CoordinatorAssignment{}, err
	}
	if reset.Assignment.State != contract.CoordinatorAssignmentDispatching {
		return contract.CoordinatorAssignment{}, ErrCoordinatorConflict
	}
	observed, err := s.coordinatorReconcileAssignment(ctx, task, reset, target, true)
	if err != nil {
		return contract.CoordinatorAssignment{}, err
	}
	if assignments, lerr := svc.store.Assignments(task.ID); lerr == nil {
		rows := make([]contract.CoordinatorAssignment, 0, len(assignments))
		for _, row := range assignments {
			rows = append(rows, row.Assignment)
		}
		s.coordinatorEmitUpdate(task, rows, nil)
	}
	return observed.Assignment, nil
}
