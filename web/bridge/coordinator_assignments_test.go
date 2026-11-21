package bridge

// These tests are the bridge half of task 6: a coordinated task's work dispatched
// to the projects it names, once, under each project's own permissions.
//
// They are written against the failure cases the plan names — concurrent
// identical dispatches, an unconfirmed submission that lapses, a restart on
// either side of the worker being created, and a grant revoked before the
// dispatch — plus the two boundaries that keep the path safe to expose at all:
// a linked thread is observed unless a person directed it, and the assignment
// operations belong to the coordinator session alone.
//
// Everything here is hermetic: a fake fleet over disposable projects, an
// in-memory access policy, and a coordinator store under a temporary directory.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// assignmentFixture is one daemon with two registered projects, a fake fleet (so
// a spawn reaches an in-process agent and a test can see the instruction it got)
// and the coordinator routes and store.
type assignmentFixture struct {
	t   *testing.T
	f   *Fleet
	ws  *Workspace
	srv *Server
	ctl *fakeAgent
	// agentFor resolves the fake agent serving one spawned agent's session.
	agentFor func(string) *fakeAgent

	rootA, rootB       string
	projectA, projectB string
	now                time.Time
}

// newAssignmentFixture builds the daemon with a pinned clock, so an unconfirmed
// submission's deadline is exact rather than timing-dependent.
func newAssignmentFixture(t *testing.T) *assignmentFixture {
	t.Helper()
	f, ctl, agentFor := agentFleet(t)
	fx := &assignmentFixture{
		t: t, f: f, ws: f.ws, ctl: ctl, agentFor: agentFor,
		now: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
	}
	fx.rootA = t.TempDir()
	fx.rootB = t.TempDir()
	for _, root := range []string{fx.rootA, fx.rootB} {
		if err := fx.ws.AddProject(root); err != nil {
			t.Fatalf("register project %s: %v", root, err)
		}
	}
	var ok bool
	if fx.projectA, ok = fx.ws.ProjectID(fx.rootA); !ok {
		t.Fatal("project A has no id")
	}
	if fx.projectB, ok = fx.ws.ProjectID(fx.rootB); !ok {
		t.Fatal("project B has no id")
	}
	f.clock = func() time.Time { return fx.now }
	fx.srv = NewServer(f)
	// A daemon identity is what the access policy is resolved against; without
	// one no task can name a session.
	fx.srv.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))
	return fx
}

// restart builds a second bridge over the same durable state, as a daemon
// restart would: a fresh coordinator store read from disk, and nothing else
// carried across.
func (fx *assignmentFixture) restart() *Server {
	fx.t.Helper()
	restarted := NewServer(fx.f)
	restarted.SetDaemonInfo(testMarshalInstance(testMarshalPID, testMarshalStarted))
	return restarted
}

// createTask opens one coordinated task over the named projects.
func (fx *assignmentFixture) createTask(requestID string, projects ...string) contract.CoordinatorTask {
	fx.t.Helper()
	rec := doReq(fx.t, fx.srv, http.MethodPost, "/api/coordinator/tasks", contract.CoordinatorCreateRequest{
		RequestID: requestID, Title: "Coordinate " + requestID, Projects: projects,
	}, nil)
	if rec.Code != http.StatusCreated {
		fx.t.Fatalf("create task: %d %s", rec.Code, rec.Body)
	}
	var out contract.CoordinatorCreateResult
	decodeBody(fx.t, rec, &out)
	return out.Task
}

// grant stores one project's default access the way the access editor writes it:
// read the version, write against it.
func (fx *assignmentFixture) grant(projectID string, access MarshalFactoryAccess) {
	fx.t.Helper()
	version := marshalAccessState(projectID, "", "", MarshalFactoryAccess{}).Version
	if current, ok := fx.ws.marshalAccessDefault(projectID); ok {
		version = current.Version
	}
	if _, _, err := fx.srv.factoryAuth.putMarshalAccessDefault(projectID, access, version); err != nil {
		fx.t.Fatalf("grant access on %s: %v", projectID, err)
	}
}

// dispatchOn runs one dispatch against the given server, which is the server a
// test has just restarted when it wants the restart's reconciliation.
func (fx *assignmentFixture) dispatchOn(s *Server, task contract.CoordinatorTask, receipt string, req contract.CoordinatorAssignRequest) (contract.CoordinatorAssignResult, error) {
	fx.t.Helper()
	return s.coordinatorDispatch(context.Background(), task, req, receipt)
}

// dispatch runs one dispatch and fails the test when it is refused.
func (fx *assignmentFixture) dispatch(task contract.CoordinatorTask, req contract.CoordinatorAssignRequest) contract.CoordinatorAssignResult {
	fx.t.Helper()
	out, err := fx.dispatchOn(fx.srv, task, "", req)
	if err != nil {
		fx.t.Fatalf("dispatch: %v", err)
	}
	return out
}

// assignments reads the task's assignments through the same projection the
// coordinator's conversation reads.
func (fx *assignmentFixture) assignments(task contract.CoordinatorTask) []contract.CoordinatorAssignment {
	fx.t.Helper()
	out, err := fx.srv.coordinatorTaskAssignments(context.Background(), task)
	if err != nil {
		fx.t.Fatalf("read assignments: %v", err)
	}
	return out
}

// agentsIn reports the agents one project holds, which is how a test says how
// many workers a dispatch started.
func (fx *assignmentFixture) agentsIn(root string) []Agent {
	var out []Agent
	for _, a := range fx.ws.Agents() {
		if a.Project == root {
			out = append(out, a)
		}
	}
	return out
}

// only returns the one assignment a task holds, failing when it holds more.
func (fx *assignmentFixture) only(task contract.CoordinatorTask) contract.CoordinatorAssignment {
	fx.t.Helper()
	list := fx.assignments(task)
	if len(list) != 1 {
		fx.t.Fatalf("task holds %d assignments, want 1: %+v", len(list), list)
	}
	return list[0]
}

// httptestDispatch serves one coordinator tool call as an MCP client would, and
// returns the recorder so a test can read the answer it got.
func httptestDispatch(s *Server, c MCPClient, name string, args map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(args)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	id, _ := json.Marshal("call-1")
	s.coordinatorToolCall(rec, req, c, id, name, raw)
	return rec
}

// standing is a project policy that already authorises this session to start
// work: the case that dispatches immediately.
func standing() MarshalFactoryAccess {
	authority := factoryAuthorityStanding
	return MarshalFactoryAccess{
		Read: boolPtr(true), CreateDraft: boolPtr(true),
		ApproveAndQueue: boolPtr(true), ApprovalAuthority: &authority,
	}
}

// asksFirst is a project policy that may create work but must ask a person
// before starting it: the case that becomes pending intake.
func asksFirst() MarshalFactoryAccess {
	return MarshalFactoryAccess{Read: boolPtr(true), CreateDraft: boolPtr(true)}
}

// TestCoordinatorDispatchStartsOneWorker pins the ordinary case: an assignment
// the project's own policy authorises starts immediately, one worker, and the
// instruction reaches it once.
func TestCoordinatorDispatchStartsOneWorker(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)

	result := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-1",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Fix the redirect bug"}},
	})
	if len(result.Assignments) != 1 {
		t.Fatalf("dispatch answered %d assignments, want 1", len(result.Assignments))
	}
	got := result.Assignments[0]
	if got.State != contract.CoordinatorAssignmentRunning {
		t.Fatalf("assignment = %s (%s), want running", got.State, got.Reason)
	}
	if got.Relationship != contract.CoordinatorAssignmentWorker {
		t.Fatalf("relationship = %s, want worker", got.Relationship)
	}
	if got.Worker == nil || got.Worker.AgentID == "" || got.Worker.SessionID == "" {
		t.Fatalf("assignment carries no acknowledged worker: %+v", got.Worker)
	}
	workers := fx.agentsIn(fx.rootA)
	if len(workers) != 1 {
		t.Fatalf("project A holds %d workers, want 1", len(workers))
	}
	if workers[0].ID != got.Worker.AgentID {
		t.Fatalf("the assignment names worker %s, the project holds %s", got.Worker.AgentID, workers[0].ID)
	}
	if workers[0].CoordinatorAssignmentID != got.ID || workers[0].CoordinatorTaskID != task.ID {
		t.Fatalf("the worker does not carry the assignment identity: %+v", workers[0])
	}
	if workers[0].Origin != OriginCoordinator {
		t.Fatalf("the worker's origin is %q, want the coordinator's own", workers[0].Origin)
	}
	prompts := waitForPrompt(t, fx.agentFor(got.Worker.AgentID), 1)
	if len(prompts) != 1 {
		t.Fatalf("the worker received %d instructions, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "Fix the redirect bug") {
		t.Fatalf("the instruction that reached the worker was %q", prompts[0])
	}
	if rev := fx.only(task).ResultRevision; rev == 0 {
		t.Fatal("a running assignment observed no result revision")
	}
}

// TestCoordinatorConcurrentIdenticalDispatchesReconcileToOneOutcome pins the
// first review focus: two identical dispatches of one request are one dispatch,
// however they interleave, so one worker starts and one instruction is sent.
func TestCoordinatorConcurrentIdenticalDispatchesReconcileToOneOutcome(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	req := contract.CoordinatorAssignRequest{
		RequestID: "dispatch-double",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Harden the parser"}},
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []contract.CoordinatorAssignResult
		errs    []error
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := fx.dispatchOn(fx.srv, task, "", req)
			mu.Lock()
			defer mu.Unlock()
			results = append(results, out)
			errs = append(errs, err)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("a concurrent dispatch was refused: %v", err)
		}
	}
	if len(results) != 2 {
		t.Fatalf("dispatches answered %d times, want 2", len(results))
	}
	ids := map[string]bool{}
	for _, out := range results {
		for _, id := range coordinatorAssignmentIDs(out.Assignments) {
			ids[id] = true
		}
	}
	if len(ids) != 1 {
		t.Fatalf("two identical dispatches minted %d assignments, want 1: %v", len(ids), ids)
	}
	workers := fx.agentsIn(fx.rootA)
	if len(workers) != 1 {
		t.Fatalf("two identical dispatches started %d workers, want 1", len(workers))
	}
	if prompts := waitForPrompt(t, fx.agentFor(workers[0].ID), 1); len(prompts) != 1 {
		t.Fatalf("the worker received %d instructions, want 1", len(prompts))
	}
	// And the task reads back as one assignment, not two.
	if list := fx.assignments(task); len(list) != 1 || list[0].ID != workers[0].CoordinatorAssignmentID {
		t.Fatalf("the task holds %+v, want the one assignment the worker carries", list)
	}
}

// TestCoordinatorUnconfirmedAssignmentStaysPending pins the second review focus:
// a project that has not authorised this session to start work gets the usual
// ask. The assignment is stored pending — never running — and only a person's
// confirmation starts the worker, which then gets the instruction once.
func TestCoordinatorUnconfirmedAssignmentStaysPending(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, asksFirst())
	task := fx.createTask("req-a", fx.projectA)

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-pending",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Ship the parser fix"}},
	})
	got := out.Assignments[0]
	if got.State != contract.CoordinatorAssignmentPending {
		t.Fatalf("assignment = %s, want pending: %s", got.State, got.Reason)
	}
	if got.Worker != nil {
		t.Fatalf("a pending assignment carries a worker: %+v", got.Worker)
	}
	if got.IntakeID != got.ID {
		t.Fatalf("the pending submission is %q, want the assignment's own identity %q", got.IntakeID, got.ID)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("a pending assignment started %d workers", len(workers))
	}
	pending, ok := fx.ws.PendingByID(got.IntakeID)
	if !ok {
		t.Fatal("the assignment says pending but the queue holds no submission")
	}
	if pending.TaskID != task.ID || pending.AssignmentID != got.ID || pending.Project != fx.rootA {
		t.Fatalf("the submission does not carry the assignment's identity: %+v", pending)
	}
	// Nothing but the person's confirmation starts it: the submission reaches
	// the worker through the same identity, once.
	agentID, err := fx.f.Approve(context.Background(), got.IntakeID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if len(fx.agentsIn(fx.rootA)) != 1 {
		t.Fatal("approval started more than one worker")
	}
	if worker, ok := fx.ws.Agent(agentID); !ok || worker.CoordinatorAssignmentID != got.ID {
		t.Fatalf("the approved worker does not carry the assignment identity: %+v", worker)
	}
	if prompts := waitForPrompt(t, fx.agentFor(agentID), 1); len(prompts) != 1 {
		t.Fatalf("the approved worker received %d instructions, want 1", len(prompts))
	}
	running := fx.only(task)
	if running.State != contract.CoordinatorAssignmentRunning || running.Worker == nil || running.Worker.AgentID != agentID {
		t.Fatalf("after approval the assignment reads %s %+v, want running on %s", running.State, running.Worker, agentID)
	}
}

// TestCoordinatorWithdrawnSubmissionReadsAsWithdrawn pins the machine-readable
// side of a denied submission: the assignment reads failed with the withdrawn
// failure — the ask was taken away, which is nothing wrong with the target
// project — and nothing started.
func TestCoordinatorWithdrawnSubmissionReadsAsWithdrawn(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, asksFirst())
	task := fx.createTask("req-deny", fx.projectA)

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-deny",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Ship the parser fix"}},
	})
	got := out.Assignments[0]
	if got.State != contract.CoordinatorAssignmentPending {
		t.Fatalf("assignment = %s (%s), want pending", got.State, got.Reason)
	}
	if err := fx.f.Deny(got.IntakeID); err != nil {
		t.Fatalf("deny the submission: %v", err)
	}

	denied := fx.only(task)
	if denied.State != contract.CoordinatorAssignmentFailed {
		t.Fatalf("a denied submission reads %s, want failed", denied.State)
	}
	if denied.Failure != contract.CoordinatorFailureWithdrawn {
		t.Fatalf("the failure code is %q, want %q", denied.Failure, contract.CoordinatorFailureWithdrawn)
	}
	if !strings.Contains(denied.Reason, "denied") {
		t.Fatalf("the denied assignment says %q", denied.Reason)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("a denied submission started %d workers", len(workers))
	}
}

// TestCoordinatorExpiredIntakeReconcilesToOneOutcome pins the second review
// focus on the other side: a submission nobody confirmed lapses, and the
// assignment reads expired with nothing started — not pending forever, and not a
// second submission when the request is retried.
func TestCoordinatorExpiredIntakeReconcilesToOneOutcome(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, asksFirst())
	task := fx.createTask("req-a", fx.projectA)
	req := contract.CoordinatorAssignRequest{
		RequestID: "dispatch-lapse",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Ship the parser fix"}},
	}
	first := fx.dispatch(task, req).Assignments[0]
	if first.State != contract.CoordinatorAssignmentPending {
		t.Fatalf("assignment = %s, want pending", first.State)
	}

	// The deadline passes with nobody confirming it. The queue sweeps lapsed
	// submissions on its own schedule; the assignment reads expired either way.
	fx.now = fx.now.Add(pendingTTL + time.Minute)
	fx.ws.SweepExpired(fx.now)
	expired := fx.only(task)
	if expired.State != contract.CoordinatorAssignmentExpired {
		t.Fatalf("assignment = %s (%s), want expired", expired.State, expired.Reason)
	}
	if expired.IntakeID != "" || expired.Worker != nil {
		t.Fatalf("an expired assignment still carries a reference: %+v", expired)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("an expired assignment started %d workers", len(workers))
	}

	// Retrying the same request does not queue a second submission or start
	// work: the dispatch identity has one outcome.
	replayed := fx.dispatch(task, req).Assignments[0]
	if replayed.ID != first.ID || replayed.State != contract.CoordinatorAssignmentExpired {
		t.Fatalf("the retry answered %+v, want the expired assignment %s", replayed, first.ID)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("retrying a lapsed dispatch started %d workers", len(workers))
	}
	if pending := fx.ws.Pending(); len(pending) != 0 {
		t.Fatalf("retrying a lapsed dispatch queued %d submissions", len(pending))
	}
}

// TestCoordinatorRestartBeforeWorkerCreationResumesOneWorker pins the plan's
// restart case on the near side: the intent is durable and nothing else happened
// when the daemon died, so the next dispatch resumes the same assignment and
// starts exactly one worker.
func TestCoordinatorRestartBeforeWorkerCreationResumesOneWorker(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	targets := []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Fix the redirect bug"}}

	// What a dispatch that died before its side effect leaves: the intent and the
	// assignment identity, written by the store before anything was attempted.
	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	minted, created, err := svc.store.BeginDispatch(task.ID, "dispatch-restart", coordinatorDispatchDigest(targets), targets)
	if err != nil || !created || len(minted) != 1 {
		t.Fatalf("record the intent: created=%v err=%v records=%+v", created, err, minted)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatal("recording an intent started a worker")
	}

	restarted := fx.restart()
	out, err := fx.dispatchOn(restarted, task, "", contract.CoordinatorAssignRequest{RequestID: "dispatch-restart", Targets: targets})
	if err != nil {
		t.Fatalf("resume after restart: %v", err)
	}
	if len(out.Assignments) != 1 || out.Assignments[0].ID != minted[0].Assignment.ID {
		t.Fatalf("the resumed dispatch answered %+v, want the recorded assignment", out.Assignments)
	}
	if out.Assignments[0].State != contract.CoordinatorAssignmentRunning {
		t.Fatalf("resumed assignment = %s (%s), want running", out.Assignments[0].State, out.Assignments[0].Reason)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 1 {
		t.Fatalf("the resumed dispatch started %d workers, want 1", len(workers))
	}
}

// TestCoordinatorRestartAfterWorkerCreationAdoptsTheWorker pins the plan's
// restart case on the far side: the worker exists with its assignment identity
// and the receipt was never written, so reconciling adopts that worker instead
// of starting a second one — and hands the instruction over once.
func TestCoordinatorRestartAfterWorkerCreationAdoptsTheWorker(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	targets := []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, Instruction: "Harden the parser"}}

	svc, err := fx.srv.coordinatorAvailable()
	if err != nil {
		t.Fatal(err)
	}
	minted, created, err := svc.store.BeginDispatch(task.ID, "dispatch-crash", coordinatorDispatchDigest(targets), targets)
	if err != nil || !created || len(minted) != 1 {
		t.Fatalf("record the intent: created=%v err=%v records=%+v", created, err, minted)
	}
	// The worker was created — with the assignment's identity — and the daemon
	// died before the receipt was written.
	agentID, _, err := fx.f.StartAssignment(context.Background(), AssignmentDispatch{
		TaskID: task.ID, AssignmentID: minted[0].Assignment.ID, Project: fx.rootA,
		Title: "Harden the parser", Prompt: "Harden the parser",
	})
	if err != nil || agentID == "" {
		t.Fatalf("start the worker the crash left: %v", err)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 1 {
		t.Fatalf("the crashed dispatch left %d workers, want 1", len(workers))
	}

	restarted := fx.restart()
	out, err := fx.dispatchOn(restarted, task, "", contract.CoordinatorAssignRequest{RequestID: "dispatch-crash", Targets: targets})
	if err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	got := out.Assignments[0]
	if got.Worker == nil || got.Worker.AgentID != agentID {
		t.Fatalf("the reconciled assignment names %+v, want the worker the crash left (%s)", got.Worker, agentID)
	}
	if got.State != contract.CoordinatorAssignmentRunning {
		t.Fatalf("reconciled assignment = %s (%s), want running", got.State, got.Reason)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 1 {
		t.Fatalf("reconciling after a restart started a second worker: %d", len(workers))
	}
	// The instruction is handed over once, and the claim is what makes it once: a
	// dispatch that died after the claim was written but before the prompt went
	// out leaves it unsent, exactly as the first-send claim does (task 3). What
	// this reconciliation must not do is send it a second time.
	time.Sleep(50 * time.Millisecond)
	if prompts := fx.agentFor(agentID).calls("session/prompt"); len(prompts) > 1 {
		t.Fatalf("the adopted worker received %d instructions, want at most 1", len(prompts))
	}
}

// TestCoordinatorRevokedGrantDeniesOneTargetAndSiblingsProgress pins the fourth
// review focus: a target whose grant was revoked is denied before anything
// starts, its own assignment carries the failure, and its eligible sibling still
// dispatches.
func TestCoordinatorRevokedGrantDeniesOneTargetAndSiblingsProgress(t *testing.T) {
	fx := newAssignmentFixture(t)
	// Project A's access is revoked: read is denied outright. Project B's own
	// policy authorises the dispatch.
	authority := factoryAuthorityStanding
	fx.grant(fx.projectA, MarshalFactoryAccess{
		Read: boolPtr(false), CreateDraft: boolPtr(true),
		ApproveAndQueue: boolPtr(true), ApprovalAuthority: &authority,
	})
	fx.grant(fx.projectB, standing())
	task := fx.createTask("req-both", fx.projectA, fx.projectB)

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-both",
		Targets: []contract.CoordinatorAssignTarget{
			{ProjectID: fx.projectA, Instruction: "Fix the parser"},
			{ProjectID: fx.projectB, Instruction: "Fix the CLI"},
		},
	})
	if len(out.Assignments) != 2 {
		t.Fatalf("dispatch answered %d assignments, want one per target", len(out.Assignments))
	}
	byProject := map[string]contract.CoordinatorAssignment{}
	for _, a := range out.Assignments {
		byProject[a.ProjectID] = a
	}
	denied := byProject[fx.projectA]
	if denied.State != contract.CoordinatorAssignmentFailed || denied.Failure != contract.CoordinatorFailureUnauthorized {
		t.Fatalf("the revoked target = %s (%s/%s), want failed unauthorized", denied.State, denied.Failure, denied.Reason)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("a revoked target started %d workers", len(workers))
	}
	if pending := fx.ws.Pending(); len(pending) != 0 {
		t.Fatalf("a revoked target queued %d submissions", len(pending))
	}
	allowed := byProject[fx.projectB]
	if allowed.State != contract.CoordinatorAssignmentRunning {
		t.Fatalf("the eligible sibling = %s (%s), want running", allowed.State, allowed.Reason)
	}
	if workers := fx.agentsIn(fx.rootB); len(workers) != 1 {
		t.Fatalf("the eligible sibling started %d workers, want 1", len(workers))
	}
	// The task reads back both outcomes, so a client can retry the one that
	// failed without losing the one that ran.
	if list := fx.assignments(task); len(list) != 2 {
		t.Fatalf("the task holds %d assignments, want 2", len(list))
	}
}

// TestCoordinatorLinkObservesWithoutStarting pins the third requirement: an
// existing thread is linked for observation. Nothing starts and nothing is sent,
// and the link is recorded against the thread it names.
func TestCoordinatorLinkObservesWithoutStarting(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	threadID, err := fx.f.Spawn(context.Background(), fx.rootA, SpawnOptions{Prompt: "existing work"})
	if err != nil {
		t.Fatalf("spawn the thread: %v", err)
	}
	thread := fx.agentFor(threadID)
	before := len(thread.calls("session/prompt"))

	out := fx.dispatch(task, contract.CoordinatorAssignRequest{
		RequestID: "dispatch-link",
		Targets:   []contract.CoordinatorAssignTarget{{ProjectID: fx.projectA, ThreadID: threadID, Instruction: "Keep an eye on the client compatibility issue"}},
	})
	got := out.Assignments[0]
	if got.State != contract.CoordinatorAssignmentLinked {
		t.Fatalf("assignment = %s (%s), want linked", got.State, got.Reason)
	}
	if got.Relationship != contract.CoordinatorAssignmentThread {
		t.Fatalf("relationship = %s, want thread", got.Relationship)
	}
	if got.Thread == nil || got.Thread.ID != threadID || got.Thread.ProjectID != fx.projectA {
		t.Fatalf("the link names %+v, want the thread in project A", got.Thread)
	}
	if got.Worker != nil {
		t.Fatalf("a link carries a worker: %+v", got.Worker)
	}
	if agents := fx.agentsIn(fx.rootA); len(agents) != 1 {
		t.Fatalf("linking started a worker: project A holds %d agents, want the one it already had", len(agents))
	}
	time.Sleep(20 * time.Millisecond)
	if after := len(thread.calls("session/prompt")); after != before {
		t.Fatalf("linking an existing thread sent it %d prompts", after-before)
	}
}

// TestCoordinatorSteeringNeedsTheUsersDirection pins the last sentence of that
// requirement: a linked thread is given the instruction only when a person
// directed it. Without that direction the dispatch is refused before anything is
// recorded, and the refusal carries the receipt to present; with it, the thread
// receives the instruction once.
func TestCoordinatorSteeringNeedsTheUsersDirection(t *testing.T) {
	fx := newAssignmentFixture(t)
	authority := factoryAuthorityUserDirected
	fx.grant(fx.projectA, MarshalFactoryAccess{
		Read: boolPtr(true), CreateDraft: boolPtr(true), AnswerQuestion: boolPtr(true),
		ApproveAndQueue: boolPtr(true), ApprovalAuthority: &authority,
	})
	task := fx.createTask("req-a", fx.projectA)
	threadID, err := fx.f.Spawn(context.Background(), fx.rootA, SpawnOptions{Prompt: "existing work"})
	if err != nil {
		t.Fatalf("spawn the thread: %v", err)
	}
	thread := fx.agentFor(threadID)
	req := contract.CoordinatorAssignRequest{
		RequestID: "dispatch-steer",
		Targets: []contract.CoordinatorAssignTarget{{
			ProjectID: fx.projectA, ThreadID: threadID,
			Instruction: "The client compatibility issue must be fixed first",
			Steer:       true,
		}},
	}
	_, err = fx.dispatchOn(fx.srv, task, "", req)
	var challenge *marshalUserDirectionChallenge
	if !errors.As(err, &challenge) {
		t.Fatalf("steering without a person's direction = %v, want a direction challenge", err)
	}
	if challenge.Receipt == "" {
		t.Fatal("the challenge carries no receipt to present")
	}
	if list := fx.assignments(task); len(list) != 0 {
		t.Fatalf("a refused steer recorded %d assignments", len(list))
	}

	// The person confirmed it and the trusted transport presents the receipt.
	if _, err := fx.dispatchOn(fx.srv, task, challenge.Receipt, req); err != nil {
		t.Fatalf("steering with the person's direction: %v", err)
	}
	got := fx.only(task)
	if got.State != contract.CoordinatorAssignmentLinked || got.Thread == nil || got.Thread.ID != threadID {
		t.Fatalf("the steered thread reads %s %+v, want the link to the thread", got.State, got.Thread)
	}
	if prompts := waitForPrompt(t, thread, 1); len(prompts) != 1 {
		t.Fatalf("the directed thread received %d instructions, want 1", len(prompts))
	} else if !strings.Contains(prompts[0], "compatibility") {
		t.Fatalf("the instruction that reached the thread was %q", prompts[0])
	}
}

// TestCoordinatorHandoffFromMarshalUsesTheSamePathWithoutASource pins the other
// half of the handoff: a request made in Marshal's own conversation is the same
// assignment path with no source at all — it opens no task, links no thread and
// reports back to nobody.
func TestCoordinatorHandoffFromMarshalUsesTheSamePathWithoutASource(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)
	instanceID, generation, ok := fx.srv.marshalInstance()
	if !ok {
		t.Fatal("the fixture daemon has no identity")
	}
	binding := MarshalSessionBinding{
		InstanceID: instanceID, ProjectID: fx.projectA, SessionID: task.SessionID, Generation: generation,
	}

	out, err := fx.srv.coordinatorHandoff(context.Background(), binding, contract.CoordinatorHandoffRequest{
		RequestID: "direct-1",
		Outcome:   "Harden the parser",
		Targets:   []contract.CoordinatorHandoffTarget{{ProjectID: fx.projectA, Instruction: "Harden the parser"}},
	}, "")
	if err != nil {
		t.Fatalf("a handoff from Marshal's own conversation: %v", err)
	}
	if out.Source != nil {
		t.Fatalf("a request from Marshal's own conversation linked a source: %+v", out.Source)
	}
	if out.TaskID != task.ID {
		t.Fatalf("the request ran under task %q, want the conversation's own %q", out.TaskID, task.ID)
	}
	if len(out.Assignments) != 1 || out.Assignments[0].State != contract.CoordinatorAssignmentRunning {
		t.Fatalf("the request answered %+v, want one running assignment", out.Assignments)
	}
	if out.Delivery != nil {
		t.Fatalf("a request with no source reported back to nobody: %+v", out.Delivery)
	}
	if _, held, derr := fx.srv.coordinator.store.Delivery(task.ID); derr != nil || held {
		t.Fatalf("a request with no source stored a delivery: held=%v err=%v", held, derr)
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 1 {
		t.Fatalf("the request started %d workers, want 1", len(workers))
	}
	// One task, not a second opened beside the conversation's own.
	tasks, err := fx.srv.coordinator.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("a source-less handoff opened %d tasks, want the one it ran under", len(tasks))
	}
}

// TestCoordinatorAssignmentsBelongToTheCoordinatorSession pins the boundary: the
// assignment operations are offered and served only to a verified built-in
// session whose session a coordinated task minted. A project session and an
// external client are both refused, and no argument can name a task instead.
func TestCoordinatorAssignmentsBelongToTheCoordinatorSession(t *testing.T) {
	fx := newAssignmentFixture(t)
	fx.grant(fx.projectA, standing())
	task := fx.createTask("req-a", fx.projectA)

	instanceID, generation, ok := fx.srv.marshalInstance()
	if !ok {
		t.Fatal("the fixture daemon has no identity")
	}
	coordinatorBinding := MarshalSessionBinding{
		InstanceID: instanceID, ProjectID: fx.projectA, SessionID: task.SessionID, Generation: generation,
	}
	definitions := fx.srv.coordinatorToolDefinitions(mcpPrincipal{Binding: coordinatorBinding, Builtin: true})
	if len(definitions) == 0 {
		t.Fatal("a coordinated task's session is offered no assignment operations")
	}
	names := map[string]bool{}
	for _, definition := range definitions {
		name, _ := definition["name"].(string)
		names[name] = true
	}
	for _, want := range []string{coordinatorToolAttention, coordinatorToolAssign, coordinatorToolAssignments} {
		if !names[want] {
			t.Fatalf("the coordinator session is not offered %s: %v", want, names)
		}
	}

	// A built-in session that no task minted is a project session: it is offered
	// the handoff (the one way it can ask Marshal to carry work elsewhere) and
	// none of the task-scoped tools, because it has no task to act on.
	projectBinding := coordinatorBinding
	projectBinding.SessionID = "project-session"
	projectTools := fx.srv.coordinatorToolDefinitions(mcpPrincipal{Binding: projectBinding, Builtin: true})
	projectNames := map[string]bool{}
	for _, definition := range projectTools {
		name, _ := definition["name"].(string)
		projectNames[name] = true
	}
	if !projectNames[coordinatorToolHandoff] {
		t.Fatalf("a project session is offered no handoff: %v", projectNames)
	}
	for _, forbidden := range []string{coordinatorToolAttention, coordinatorToolAssign, coordinatorToolAssignments} {
		if projectNames[forbidden] {
			t.Fatalf("a project session was offered the task-scoped %s: %v", forbidden, projectNames)
		}
	}
	// An external client holds no binding at all.
	if got := fx.srv.coordinatorToolDefinitions(mcpPrincipal{Builtin: false}); len(got) != 0 {
		t.Fatalf("an external client was offered coordinator tools: %v", got)
	}

	// And the call itself is refused for a client the transport did not bind,
	// whatever arguments it sends.
	rec := httptestDispatch(fx.srv, MCPClient{ID: "external-client"}, coordinatorToolAssign, map[string]any{
		"requestId": "dispatch-forged",
		"targets":   []map[string]any{{"projectId": fx.projectA, "instruction": "Fix it"}},
	})
	if !strings.Contains(rec.Body.String(), factoryCodeDenied) {
		t.Fatalf("an external client's dispatch = %s, want a denial", rec.Body.String())
	}
	if list := fx.assignments(task); len(list) != 0 {
		t.Fatalf("a refused caller recorded %d assignments", len(list))
	}
	if workers := fx.agentsIn(fx.rootA); len(workers) != 0 {
		t.Fatalf("a refused caller started %d workers", len(workers))
	}
}
