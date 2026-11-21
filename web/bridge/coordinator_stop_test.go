package bridge

// These tests are the bridge half of task 8: stopping at the scope the request
// names.
//
// The cases are the ones the plan names — a stop that lands while a dispatch is
// still resolving, a repeated stop after a reconnect, and a task holding both a
// worker it created and a thread it only linked — plus the claim that makes a
// stop trustworthy at all: the intent is durable before a child is cancelled, so
// the record can never say stopped while a worker it started is still running.
//
// Everything here is hermetic: a fake fleet over disposable projects, an
// in-memory access policy, and a coordinator store under a temporary directory.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// stopOn runs one stop against the given server, which is the server a test has
// just restarted when it wants a reconnect's answer.
func (fx *assignmentFixture) stopOn(s *Server, task contract.CoordinatorTask) (contract.CoordinatorStopResult, error) {
	fx.t.Helper()
	return s.coordinatorStopTask(context.Background(), task)
}

// stop runs one stop and fails the test when the bridge refuses it.
func (fx *assignmentFixture) stop(task contract.CoordinatorTask) contract.CoordinatorStopResult {
	fx.t.Helper()
	out, err := fx.stopOn(fx.srv, task)
	if err != nil {
		fx.t.Fatalf("stop: %v", err)
	}
	return out
}

// assignmentOf returns the named assignment of a stopped task's answer.
func assignmentOf(t *testing.T, out contract.CoordinatorStopResult, projectID string) contract.CoordinatorAssignment {
	t.Helper()
	for _, a := range out.Assignments {
		if a.ProjectID == projectID {
			return a
		}
	}
	t.Fatalf("the stop answered %d assignments, none for %s: %+v", len(out.Assignments), projectID, out.Assignments)
	return contract.CoordinatorAssignment{}
}

// sourceTask opens one task whose source is a thread in project A, which is what
// makes the stop post a summary back into that thread.
func (fx *assignmentFixture) sourceTask(requestID, sourceThread string, projects ...string) contract.CoordinatorTask {
	fx.t.Helper()
	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		fx.t.Fatal(err)
	}
	task, _, err := svc.store.Create(contract.CoordinatorCreateRequest{
		RequestID: requestID,
		Title:     "Coordinate " + requestID,
		Source:    &contract.CoordinatorThreadRef{ProjectID: fx.projectA, Kind: "agent", ID: sourceThread},
		Projects:  projects,
	})
	if err != nil {
		fx.t.Fatalf("create sourced task: %v", err)
	}
	return task
}

// extraProject registers one more disposable project and returns its root and
// canonical id.
func (fx *assignmentFixture) extraProject() (string, string) {
	fx.t.Helper()
	root := fx.t.TempDir()
	if err := fx.ws.AddProject(root); err != nil {
		fx.t.Fatalf("register project %s: %v", root, err)
	}
	id, ok := fx.ws.ProjectID(root)
	if !ok {
		fx.t.Fatalf("project %s has no id", root)
	}
	return root, id
}

// TestCoordinatorStopDuringDispatchStartsNothing pins the first half of the
// first requirement: a stop that lands while an assignment is still dispatching
// blocks the dispatch, so no worker starts under a stopped task.
//
// The dispatch intent is durable and unresolved — exactly what a dispatch in
// flight leaves — and the stop settles it as stopped rather than leaving it for
// a later dispatch to resume.
func TestCoordinatorStopDuringDispatchStartsNothing(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	targets := []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Fix the redirect bug"}}

	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	minted, created, err := svc.store.BeginDispatch(task.ID, "dispatch-racing", coordinatorDispatchDigest(targets), targets)
	if err != nil || !created || len(minted) != 1 {
		t.Fatalf("record the intent: created=%v err=%v records=%+v", created, err, minted)
	}

	out := fx.stop(task)
	if out.Task.Status != contract.CoordinatorTaskStopped {
		t.Fatalf("the task reads %s, want stopped", out.Task.Status)
	}
	got := assignmentOf(t, out, fx.projectA)
	if got.State != contract.CoordinatorAssignmentStopped {
		t.Fatalf("the dispatching assignment reads %s (%s), want stopped", got.State, got.Reason)
	}
	if got.Reason != coordinatorStopReasonUnstarted {
		t.Fatalf("the stopped assignment says %q, want the not-started reason", got.Reason)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("a stop during dispatch started %d workers", len(workers))
	}

	// The dispatch that was in flight when the stop landed is refused now: the
	// task dispatches nothing, and the assignment it recorded starts nothing.
	if _, err := fx.dispatchOn(fx.srv, task, "", contract.CoordinatorAssignRequest{RequestID: "dispatch-racing", Targets: targets}); err == nil {
		t.Fatal("a stopped task accepted a dispatch")
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("a dispatch to a stopped task started %d workers", len(workers))
	}
	if list := fx.assignments(task); len(list) != 1 || list[0].State != contract.CoordinatorAssignmentStopped {
		t.Fatalf("the task reads %+v, want the one stopped assignment", list)
	}
}

// TestCoordinatorStopReconcilesAcknowledgmentThatRacedIt pins the racing half:
// a worker whose record appeared while the stop was being recorded is cancelled
// by the stop, not left running under a stopped task.
func TestCoordinatorStopReconcilesAcknowledgmentThatRacedIt(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	targets := []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Harden the parser"}}

	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	minted, created, err := svc.store.BeginDispatch(task.ID, "dispatch-race", coordinatorDispatchDigest(targets), targets)
	if err != nil || !created || len(minted) != 1 {
		t.Fatalf("record the intent: created=%v err=%v records=%+v", created, err, minted)
	}
	// The worker the racing dispatch started: its own record carries the
	// assignment's identity, so the stop finds it by that and not by a guess.
	agentID, _, err := fx.f.StartAssignment(context.Background(), AssignmentDispatch{
		TaskID: task.ID, AssignmentID: minted[0].Assignment.ID, Project: fx.rootA,
		Title: "Harden the parser", Prompt: "Harden the parser",
	})
	if err != nil || agentID == "" {
		t.Fatalf("start the racing worker: %v", err)
	}
	worker := fx.agentFor(agentID)

	out := fx.stop(task)
	if out.Task.Status != contract.CoordinatorTaskStopped {
		t.Fatalf("the task reads %s, want stopped: %+v", out.Task.Status, out.Assignments)
	}
	got := assignmentOf(t, out, fx.projectA)
	if got.State != contract.CoordinatorAssignmentStopped {
		t.Fatalf("the raced assignment reads %s (%s), want stopped", got.State, got.Reason)
	}
	if got.Reason != coordinatorStopReasonWorker {
		t.Fatalf("the stopped assignment says %q, want the cancelled-worker reason", got.Reason)
	}
	// The one worker the task created is cancelled by the stop — the ACP
	// cancellation the fleet already sends — so the work is reached and not only
	// the record.
	waitForCancel(t, worker)
	if workers := fx.agentsIn(fx.rootA); len(workers) != 1 {
		t.Fatalf("the stop started %d extra workers", len(workers)-1)
	}
}

// waitForCancel waits for one ACP turn cancellation to reach the fake agent.
func waitForCancel(t *testing.T, worker *fakeAgent) {
	t.Helper()
	if worker == nil {
		t.Fatal("the racing worker left no fake agent to inspect")
	}
	waitFor(t, 3_000_000_000, "session/cancel to reach the worker", func() bool {
		return len(worker.calls("session/cancel")) == 1
	})
}

// TestCoordinatorStopCancelsOnlyWorkersTheTaskCreated pins the scope rule on a
// task holding both: the worker it started is cancelled, and the thread it only
// linked keeps running and stays usable under its own Stop.
func TestCoordinatorStopCancelsOnlyWorkersTheTaskCreated(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	fx.grant(fx.projectB, standing())
	task := fx.createTask("req-both", fx.projectA, fx.projectB)

	threadID, err := fx.f.Spawn(context.Background(), fx.rootB, SpawnOptions{Prompt: "existing work"})
	if err != nil {
		t.Fatalf("spawn the linked thread: %v", err)
	}
	linked := fx.agentFor(threadID)
	linkedPrompts := len(linked.calls("session/prompt"))

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-both",
		Targets: []contract.CoordinatorAssignTarget{
			{ProjectID: fx.projectA, Instruction: "Fix the parser"},
			{ProjectID: fx.projectB, ThreadID: threadID, Instruction: "Keep an eye on the client compatibility issue"},
		},
	})
	var workerID string
	for _, a := range out.Assignments {
		if a.ProjectID == fx.projectA && a.Worker != nil {
			workerID = a.Worker.AgentID
		}
	}
	if workerID == "" {
		t.Fatalf("the dispatch started no worker: %+v", out.Assignments)
	}
	worker := fx.agentFor(workerID)

	stopped := fx.stop(task)
	if stopped.Task.Status != contract.CoordinatorTaskStopped {
		t.Fatalf("the task reads %s, want stopped", stopped.Task.Status)
	}
	owned := assignmentOf(t, stopped, fx.projectA)
	if owned.State != contract.CoordinatorAssignmentStopped || owned.Reason != coordinatorStopReasonWorker {
		t.Fatalf("the worker's assignment reads %s (%s), want stopped by cancellation", owned.State, owned.Reason)
	}
	waitForCancel(t, worker)

	// The linked thread is not the task's to stop: its assignment still reads
	// linked, no cancellation reached it, and it still takes a prompt of its own.
	link := assignmentOf(t, stopped, fx.projectB)
	if link.State != contract.CoordinatorAssignmentLinked {
		t.Fatalf("the linked assignment reads %s (%s), want linked", link.State, link.Reason)
	}
	if link.Thread == nil || link.Thread.ID != threadID {
		t.Fatalf("the linked assignment names %+v, want the thread it linked", link.Thread)
	}
	if cancels := linked.calls("session/cancel"); len(cancels) != 0 {
		t.Fatalf("stopping the task cancelled the linked thread %d times", len(cancels))
	}
	rt, err := fx.f.RuntimeForSession(threadID)
	if err != nil {
		t.Fatalf("the linked thread has no runtime after the stop: %v", err)
	}
	if err := rt.reg.Prompt(context.Background(), fx.f.sessionIDFor(rt), "carry on"); err != nil {
		t.Fatalf("the linked thread is not usable after the stop: %v", err)
	}
	if after := len(linked.calls("session/prompt")); after != linkedPrompts+1 {
		t.Fatalf("the linked thread received %d prompts after the stop, want its own one", after-linkedPrompts)
	}
	// And the thread itself is still listed: nothing removed it.
	if _, held := fx.ws.Agent(threadID); !held {
		t.Fatal("the stop removed the linked thread's record")
	}
}

// TestCoordinatorStopWithdrawsOwnedSubmissionsAndNamesEveryOutcome pins the rest
// of the scope rule and the summary: an owned submission is withdrawn rather than
// left for a person to confirm into a stopped task, a failed assignment keeps its
// failure, and the delivered result names each outcome as the one it is —
// cancelled, withdrawn, failed and still linked.
func TestCoordinatorStopWithdrawsOwnedSubmissionsAndNamesEveryOutcome(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	fx.grant(fx.projectB, asksFirst())
	pendingRoot, pendingProject := fx.extraProject()
	fx.grant(pendingProject, asksFirst())
	failedRoot, failedProject := fx.extraProject()
	// The revoked project: read is denied outright, so its assignment fails at
	// dispatch and the stop leaves that failure alone.
	fx.grant(failedProject, MarshalFactoryAccess{Read: boolPtr(false), CreateDraft: boolPtr(true)})

	sourceID, err := fx.f.Spawn(context.Background(), fx.rootA, SpawnOptions{Prompt: "the thread that hands work off"})
	if err != nil {
		t.Fatalf("spawn the source thread: %v", err)
	}
	linkedID, err := fx.f.Spawn(context.Background(), fx.rootB, SpawnOptions{Prompt: "existing work"})
	if err != nil {
		t.Fatalf("spawn the linked thread: %v", err)
	}
	task := fx.sourceTask("req-handoff", sourceID, fx.projectA, fx.projectB, pendingProject, failedProject)

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-handoff",
		Targets: []contract.CoordinatorAssignTarget{
			{ProjectID: fx.projectA, Instruction: "Fix the parser"},
			{ProjectID: fx.projectB, ThreadID: linkedID, Instruction: "Watch the compatibility issue"},
			{ProjectID: pendingProject, Instruction: "Ship the CLI fix"},
			{ProjectID: failedProject, Instruction: "Touch the revoked project"},
		},
	})
	var workerID string
	for _, a := range out.Assignments {
		switch {
		case a.ProjectID == fx.projectA && a.Worker != nil:
			workerID = a.Worker.AgentID
		case a.ProjectID == pendingProject && a.State != contract.CoordinatorAssignmentPending:
			t.Fatalf("the unconfirmed target reads %s, want pending", a.State)
		case a.ProjectID == failedProject && a.State != contract.CoordinatorAssignmentFailed:
			t.Fatalf("the revoked target reads %s, want failed", a.State)
		}
	}
	if workerID == "" {
		t.Fatalf("the dispatch started no worker: %+v", out.Assignments)
	}
	if pending := fx.ws.Pending(); len(pending) != 1 {
		t.Fatalf("the dispatch queued %d submissions, want 1", len(pending))
	}

	stopped := fx.stop(task)
	if stopped.Task.Status != contract.CoordinatorTaskStopped {
		t.Fatalf("the task reads %s, want stopped", stopped.Task.Status)
	}
	waitForCancel(t, fx.agentFor(workerID))

	// The owned submission is withdrawn: nothing is left for a person to confirm
	// into a task that has stopped.
	if pending := fx.ws.Pending(); len(pending) != 0 {
		t.Fatalf("the stop left %d submissions in the queue", len(pending))
	}
	withdrawn := assignmentOf(t, stopped, pendingProject)
	if withdrawn.State != contract.CoordinatorAssignmentStopped || withdrawn.Reason != coordinatorStopReasonWithdrawn {
		t.Fatalf("the withdrawn assignment reads %s (%s)", withdrawn.State, withdrawn.Reason)
	}
	// The failed assignment keeps its failure: a stop does not rewrite an outcome
	// that already happened.
	failed := assignmentOf(t, stopped, failedProject)
	if failed.State != contract.CoordinatorAssignmentFailed || failed.Failure != contract.CoordinatorFailureUnauthorized {
		t.Fatalf("the failed assignment reads %s (%s/%s), want its own failure", failed.State, failed.Failure, failed.Reason)
	}
	// The linked thread keeps its link and its life.
	link := assignmentOf(t, stopped, fx.projectB)
	if link.State != contract.CoordinatorAssignmentLinked {
		t.Fatalf("the linked assignment reads %s (%s), want linked", link.State, link.Reason)
	}

	// The result delivered into the source thread names every one of them.
	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	delivery, held, err := svc.store.Delivery(task.ID)
	if err != nil || !held {
		t.Fatalf("the stopped task delivered no result: held=%v err=%v", held, err)
	}
	if delivery.EventID != coordinatorDeliveryID(task.ID) {
		t.Fatalf("the delivery's event id is %q, want the task's stable one", delivery.EventID)
	}
	if delivery.Source == nil || delivery.Source.ID != sourceID {
		t.Fatalf("the delivery names source %+v, want the handing-off thread", delivery.Source)
	}
	for _, want := range []string{
		"this task stopped",
		"stopped: " + coordinatorStopReasonWorker,
		"stopped: " + coordinatorStopReasonWithdrawn,
		"failed",
		"linked and observed",
	} {
		if !strings.Contains(delivery.Text, want) {
			t.Fatalf("the delivered result does not say %q:\n%s", want, delivery.Text)
		}
	}
	// Every project is named, so a person can tell which one each outcome was.
	for _, name := range []string{
		coordinatorProjectName(fx.rootA),
		coordinatorProjectName(fx.rootB),
		coordinatorProjectName(pendingRoot),
		coordinatorProjectName(failedRoot),
	} {
		if !strings.Contains(delivery.Text, name) {
			t.Fatalf("the delivered result does not name %q:\n%s", name, delivery.Text)
		}
	}
}

// TestCoordinatorRepeatedStopAfterReconnectIsIdempotent pins the plan's second
// stop case: the daemon restarted between the first stop and the second, and the
// repeat answers with the state the first left — one summary under the task's
// stable event id, and no second cancellation.
func TestCoordinatorRepeatedStopAfterReconnectIsIdempotent(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	sourceID, err := fx.f.Spawn(context.Background(), fx.rootA, SpawnOptions{Prompt: "the thread that hands work off"})
	if err != nil {
		t.Fatalf("spawn the source thread: %v", err)
	}
	task := fx.sourceTask("req-repeat", sourceID, fx.projectA)

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-repeat",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Fix the parser"}},
	})
	if out.Assignments[0].Worker == nil {
		t.Fatalf("the dispatch started no worker: %+v", out.Assignments)
	}
	worker := fx.agentFor(out.Assignments[0].Worker.AgentID)

	first := fx.stop(task)
	if first.Task.Status != contract.CoordinatorTaskStopped {
		t.Fatalf("the first stop left the task %s", first.Task.Status)
	}
	waitForCancel(t, worker)
	cancels := len(worker.calls("session/cancel"))
	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	delivered, held, err := svc.store.Delivery(task.ID)
	if err != nil || !held {
		t.Fatalf("the first stop delivered no result: held=%v err=%v", held, err)
	}

	// The daemon restarts: a fresh bridge over the same durable state, and the
	// reconnect that follows asks for the same stop again.
	restarted := fx.restart()
	second, err := fx.stopOn(restarted, first.Task)
	if err != nil {
		t.Fatalf("the repeated stop was refused: %v", err)
	}
	if second.Task.Status != contract.CoordinatorTaskStopped {
		t.Fatalf("the repeat left the task %s", second.Task.Status)
	}
	if second.Task.Revision != first.Task.Revision {
		t.Fatalf("the repeat moved the task's revision from %d to %d", first.Task.Revision, second.Task.Revision)
	}
	if got, want := coordinatorAssignmentIDs(second.Assignments), coordinatorAssignmentIDs(first.Assignments); len(got) != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the repeat answered assignments %v, want the first stop's %v", got, want)
	}
	for _, a := range second.Assignments {
		if a.State != contract.CoordinatorAssignmentStopped {
			t.Fatalf("the repeat moved an assignment to %s (%s)", a.State, a.Reason)
		}
	}
	// One cancellation, not two: the repeat cancelled nothing a second time.
	if after := len(worker.calls("session/cancel")); after != cancels {
		t.Fatalf("the repeated stop sent %d more cancellations", after-cancels)
	}
	// And one summary, unchanged, under the same stable event id.
	repeated, held, err := restarted.coordinator.store.Delivery(task.ID)
	if err != nil || !held {
		t.Fatalf("the repeat lost the delivered result: held=%v err=%v", held, err)
	}
	if repeated.EventID != delivered.EventID {
		t.Fatalf("the repeat posted event %q, want the stable %q", repeated.EventID, delivered.EventID)
	}
	if !repeated.At.Equal(delivered.At) || repeated.Text != delivered.Text {
		t.Fatalf("the repeat posted a second summary at %s:\n%s", repeated.At, repeated.Text)
	}

	// The task still dispatches nothing, which is what stopped means.
	if _, err := fx.dispatchOn(restarted, second.Task, "", contract.CoordinatorAssignRequest{
		RequestID: "dispatch-after-stop",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Do it again"}},
	}); err == nil {
		t.Fatal("a stopped task accepted a dispatch after the reconnect")
	}
}

// TestCoordinatorStopAnswersItsRoute pins the action's wire: the scoped stop is
// one request and one answer, and a second request answers the same state.
func TestCoordinatorStopAnswersItsRoute(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-http",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Fix the parser"}},
	})

	rec := doReq(t, fx.srv, "POST", "/api/coordinator/tasks/"+task.ID+"/stop", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("POST stop = %d %s", rec.Code, rec.Body)
	}
	var out contract.CoordinatorStopResult
	decodeBody(t, rec, &out)
	if out.Task.Status != contract.CoordinatorTaskStopped {
		t.Fatalf("the route answered task %s, want stopped", out.Task.Status)
	}
	if len(out.Assignments) != 1 || out.Assignments[0].State != contract.CoordinatorAssignmentStopped {
		t.Fatalf("the route answered %+v, want the one stopped assignment", out.Assignments)
	}

	// A repeated request is the same answer, not a conflict: a screen that
	// retried a stop it was unsure about sees the state the first left.
	again := doReq(t, fx.srv, "POST", "/api/coordinator/tasks/"+task.ID+"/stop", nil, nil)
	if again.Code != 200 {
		t.Fatalf("the repeated POST stop = %d %s", again.Code, again.Body)
	}
	var repeated contract.CoordinatorStopResult
	decodeBody(t, again, &repeated)
	if repeated.Task.Revision != out.Task.Revision {
		t.Fatalf("the repeated stop moved the revision from %d to %d", out.Task.Revision, repeated.Task.Revision)
	}

	// A task that does not exist is a 404 rather than a stop that happened
	// nowhere.
	if rec := doReq(t, fx.srv, "POST", "/api/coordinator/tasks/nope/stop", nil, nil); rec.Code != 404 {
		t.Fatalf("stopping an unknown task = %d %s, want 404", rec.Code, rec.Body)
	}
}

// TestCoordinatorApprovalWaitsForTheStopThatBeatIt pins the confirmation's
// serialization with the stop: an approval that arrives while the stop holds
// the assignment's dispatch lock cannot approve beside it, so the interleaving
// that would start a worker the stop had already withdrawn and settled starts
// nothing.
func TestCoordinatorApprovalWaitsForTheStopThatBeatIt(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, asksFirst())
	task := fx.createTask("req-race", fx.projectA)

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-race",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Ship the parser fix"}},
	})
	asg := out.Assignments[0]
	if asg.State != contract.CoordinatorAssignmentPending {
		t.Fatalf("assignment = %s (%s), want pending", asg.State, asg.Reason)
	}
	if _, held := fx.ws.PendingByID(asg.ID); !held {
		t.Fatal("the pending submission is not in the queue under the assignment's identity")
	}

	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	// The stop holds the assignment's dispatch lock while it withdraws the
	// submission and settles the task; the test holds the same lock to place
	// the HTTP approval inside that window.
	release := svc.lockAssignment(asg.ID)
	started := make(chan struct{})
	type posted struct{ code int }
	done := make(chan posted, 1)
	go func() {
		close(started)
		req := httptest.NewRequest(http.MethodPost, "/api/pending/"+asg.ID+"/approve", nil)
		// Addressed to localhost, the name the listener answers: the guard
		// refuses another site's name (webguard.go, D13) before the handler.
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		fx.srv.ServeHTTP(rec, req)
		done <- posted{code: rec.Code}
	}()
	<-started
	// Parked on the lock. (An approval without it is long since done, and the
	// wait below is what catches the one that ran beside the stop.)
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case r := <-done:
			t.Fatalf("the approval ran beside the stop instead of waiting on the assignment's lock: HTTP %d", r.code)
		case <-time.After(10 * time.Millisecond):
		}
	}
	// What the stop writes under that same lock: the submission is withdrawn
	// and the task and its assignment are settled stopped.
	if err := fx.ws.DeletePending(asg.ID); err != nil {
		t.Fatalf("withdraw the submission: %v", err)
	}
	if _, _, err := svc.store.SetTaskStatus(task.ID, contract.CoordinatorTaskStopped); err != nil {
		t.Fatalf("settle the task: %v", err)
	}
	if _, err := svc.store.UpdateAssignment(task.ID, asg.ID, func(r *coordinatorAssignmentRecord) bool {
		r.Assignment.State = contract.CoordinatorAssignmentStopped
		r.Assignment.Reason = coordinatorStopReasonWithdrawn
		r.Assignment.IntakeID = ""
		return true
	}); err != nil {
		t.Fatalf("settle the assignment: %v", err)
	}
	release()
	r := <-done
	if r.code == http.StatusOK {
		t.Fatal("an approval of a withdrawn submission answered 200")
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("the racing approval started %d workers", len(workers))
	}
	if current := fx.only(task); current.State != contract.CoordinatorAssignmentStopped {
		t.Fatalf("the settled assignment reads %s (%s), want stopped", current.State, current.Reason)
	}
}

// TestCoordinatorCrashedIntentUnderAStoppedTaskReadsStopped pins what a read
// answers when the daemon died after recording a dispatch intent and the task
// was then stopped: no stop pass ever saw the intent, so the read settles it as
// stopped before any work started rather than leaving "starting" under a
// stopped task.
func TestCoordinatorCrashedIntentUnderAStoppedTaskReadsStopped(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-crash", fx.projectA)
	targets := []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Fix the redirect bug"}}

	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := svc.store.BeginDispatch(task.ID, "dispatch-crash", coordinatorDispatchDigest(targets), targets); err != nil || !created {
		t.Fatalf("record the intent: created=%v err=%v", created, err)
	}
	// The task is stopped with no worker and no submission: exactly what a stop
	// that landed after the crash left.
	if _, _, err := svc.store.SetTaskStatus(task.ID, contract.CoordinatorTaskStopped); err != nil {
		t.Fatal(err)
	}

	got := fx.only(task)
	if got.State != contract.CoordinatorAssignmentStopped {
		t.Fatalf("the crashed intent reads %s (%s), want stopped", got.State, got.Reason)
	}
	if got.Reason != coordinatorStopReasonUnstarted {
		t.Fatalf("the crashed intent says %q, want the not-started reason", got.Reason)
	}
	if got.Worker != nil || got.IntakeID != "" {
		t.Fatalf("the crashed intent carries %+v", got)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("the crashed intent started %d workers", len(workers))
	}
}
