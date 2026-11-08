package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/postmortem"
)

// fakePlanRun is the plan run the pipeline cases drive. It gates its first
// gates runs, records each answer and its run count, and can block until its
// context is done after signalling that it started. It is the service package's
// fakePlanRun, copied here so the harness does not depend on its package.
type fakePlanRun struct {
	gates   int
	answers []string
	runs    int
	// started, when non-nil, is closed once when Run first begins.
	started   chan struct{}
	startOnce sync.Once
	// block makes Run wait for its context after it signals, and return the
	// context's error.
	block bool
}

func (f *fakePlanRun) Run(ctx context.Context) error {
	f.runs++
	if f.started != nil {
		f.startOnce.Do(func() { close(f.started) })
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.runs <= f.gates {
		return &acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: "pipeline: human gate required"}
	}
	return nil
}

func (f *fakePlanRun) Answer(answer string) { f.answers = append(f.answers, answer) }

// gatedPlanRun fails the test unless err carries the human gate's code.
func gatedPlanRun(t *testing.T, name string, err error) {
	t.Helper()
	if !acpclient.IsCode(err, acpwire.CodeHumanGateRequired) {
		t.Fatalf("%s = %v, want code %q", name, err, acpwire.CodeHumanGateRequired)
	}
}

// noPausedPlanSays is the service's refusal when no plan run is paused. An
// unclassified error's sentence crosses the wire (P3 plan 6b decision 1), so
// both arms report it, where the service's own tests compare the sentinel.
const noPausedPlanSays = "inproc: no plan run is waiting on an answer"

// refusedNoPausedPlan fails the test unless err is the service's no-paused-run
// refusal.
func refusedNoPausedPlan(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s = nil, want the no-paused-run refusal", name)
	}
	if got := err.Error(); got != noPausedPlanSays {
		t.Fatalf("%s = %q, want %q", name, got, noPausedPlanSays)
	}
}

// fakeEndService is the runtime's end surface as the cases drive it: it reports
// a set membership and decision, records what it was given, and returns set
// errors. It is the service package's fakeEndService, copied here so the
// harness does not depend on its package.
type fakeEndService struct {
	mu          sync.Mutex
	work        acpwire.EndWork
	decision    acpwire.EndDecision
	waitErr     error
	analysisErr error
	params      []acpwire.EndAcceptParams
	goals       []string
	waits       int
}

func (f *fakeEndService) ActiveWork() acpwire.EndWork {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.work
}

func (f *fakeEndService) AcceptEnd(params acpwire.EndAcceptParams) (acpwire.EndDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = append(f.params, params)
	return f.decision, nil
}

func (f *fakeEndService) WaitForEnd(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits++
	return f.waitErr
}

func (f *fakeEndService) RunExitAnalysis(ctx context.Context, goal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goals = append(f.goals, goal)
	return f.analysisErr
}

// accepts returns the acceptance parameters the service was given, in order.
func (f *fakeEndService) accepts() []acpwire.EndAcceptParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acpwire.EndAcceptParams(nil), f.params...)
}

// analysisGoals returns the goals RunExitAnalysis was given, in order.
func (f *fakeEndService) analysisGoals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.goals...)
}

// waitCount returns the number of WaitForEnd calls.
func (f *fakeEndService) waitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waits
}

// browserDB opens a migrated database in a temporary directory, as the service
// package's browser cases do.
func browserDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// serveEnds registers the end-flow, close and session-list handlers on the
// pipe arm's server over the served host and service, and returns the manager
// the barrier fences (P3 plan 6c).
func serveEnds(tb testing.TB, srv *Server, servedHost *service.Host, served *service.Session, sessionID string) *EndManager {
	tb.Helper()
	ends := NewEndManager(EndManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			return served, id == sessionID
		},
		HostLookup: func(id string) (*service.Host, bool) {
			return servedHost, id == sessionID
		},
		Notify:  srv.Notify,
		Marshal: srv.marshalClient,
	})
	// A nil others map is what the pipe arm wants: the two marshalOr methods
	// refuse a client without the capability as method-not-found, and this
	// arm's client always declares it.
	registerEnds(srv, ends, nil)
	tb.Cleanup(ends.Close)
	return ends
}

// TestEndingConformance runs every pipeline and end-flow case against both arms
// of the seam, and the host's session list against both arms of the host seam:
// the service itself, and the client's mirror of it over a pipe. The same
// answers from both is V1.
func TestEndingConformance(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, open func(t *testing.T, d service.Deps) seam)
	}{
		{name: "a swarm runs on the run path", run: startSwarmRunsOnTheRunPath},
		{name: "a stopped swarm is not an error", run: aStoppedSwarmIsNotAnError},
		{name: "a plan run is built when it starts", run: startSDDBuildsThePlanWhenItStarts},
		{name: "a gated plan waits for its answer", run: aGatedPlanWaitsForItsAnswer},
		{name: "a gate can pause the run again", run: aGateCanPauseTheRunAgain},
		{name: "stop drops the paused run", run: stopSDDDropsThePausedRun},
		{name: "stop clears a stale question", run: stopSDDClearsAStaleQuestion},
		{name: "a new plan run replaces a paused one", run: aNewPlanRunReplacesAPausedOne},
		{name: "an authored plan is the candidate", run: authorPlanReturnsTheCandidate},
		{name: "a stopped authoring reports cancelled", run: aStoppedAuthoringReportsCancelled},
		{name: "a preflight forwards without the slot", run: planPreflightForwardsWithoutTheSlot},

		{name: "the end calls forward to the end service", run: endCallsForwardToTheEndService},
		{name: "a session without an end service ends at once", run: aSessionWithoutAnEndServiceEndsAtOnce},
		{name: "the postmortem writes the report", run: postmortemWritesTheReport},
		{name: "close shuts the session down", run: closeShutsTheSessionDown},
		{name: "close cancels the run in flight", run: closeCancelsTheRunInFlight},
	}
	for _, arm := range seamArms() {
		for _, tc := range cases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}

	hostCases := []struct {
		name string
		run  func(t *testing.T, open func(t *testing.T, d service.Deps) (hostSeam, seam))
	}{
		{name: "list sessions reads the database", run: listSessionsReadsTheDatabase},
		{name: "list sessions without a database is coded", run: listSessionsWithoutADatabaseIsCoded},
	}
	for _, arm := range hostArms() {
		for _, tc := range hostCases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}
}

// startSwarmRunsOnTheRunPath pins that a swarm runs on the shared run path,
// gets its parameters unchanged, and that a goal-less run and a session with no
// swarm runner are both refused before anything starts.
func startSwarmRunsOnTheRunPath(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	var calls []acpwire.SwarmParams
	var registered bool
	sess := open(t, service.Deps{
		State: state,
		Swarm: func(ctx context.Context, params acpwire.SwarmParams) error {
			calls = append(calls, params)
			registered = state.CurrentTurnStop() != nil
			return nil
		},
	})

	want := acpwire.SwarmParams{Goal: "g", Overrides: map[string]string{"planner": "local/qwen"}}
	if err := sess.StartSwarm(ctx, want); err != nil {
		t.Fatalf("StartSwarm: %v", err)
	}
	if !reflect.DeepEqual(calls, []acpwire.SwarmParams{want}) {
		t.Errorf("swarm calls = %+v, want the params unchanged", calls)
	}
	if !registered {
		t.Error("no stop group was registered while the swarm ran")
	}

	if err := sess.StartSwarm(ctx, acpwire.SwarmParams{Goal: "  "}); err == nil {
		t.Error("StartSwarm with an empty goal = nil, want an error")
	}
	if len(calls) != 1 {
		t.Errorf("swarm calls = %+v, want the goal-less run to call nothing", calls)
	}

	bare := open(t, service.Deps{State: seamState(t, "s2")})
	if err := bare.StartSwarm(ctx, acpwire.SwarmParams{Goal: "g"}); !acpclient.IsCode(err, acpwire.CodeNoRunner) {
		t.Errorf("StartSwarm without a runtime swarm = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// aStoppedSwarmIsNotAnError pins that a swarm that returns its own context
// error once stopped still leaves StartSwarm with no error: the stop reported
// the cancellation.
func aStoppedSwarmIsNotAnError(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	started := make(chan struct{})
	sess := open(t, service.Deps{
		State: seamState(t, "s1"),
		Swarm: func(ctx context.Context, params acpwire.SwarmParams) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})

	done := make(chan error, 1)
	go func() { done <- sess.StartSwarm(context.Background(), acpwire.SwarmParams{Goal: "g"}) }()
	waitClosed(t, started, "the swarm")

	if err := sess.Stop(context.Background(), "stop the swarm"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a stopped StartSwarm = %v, want nil", err)
	}
}

// startSDDBuildsThePlanWhenItStarts pins that a plan run is built from the
// caller's parameters on the run path, and that a run with no path, an unknown
// strategy, no runner and a failing factory are all refused without leaving a
// turn behind.
func startSDDBuildsThePlanWhenItStarts(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	fake := &fakePlanRun{}
	var calls []acpwire.SDDParams
	sess := open(t, service.Deps{
		State: state,
		NewPlanRun: func(params acpwire.SDDParams) (service.PlanRun, error) {
			calls = append(calls, params)
			return fake, nil
		},
	})

	want := acpwire.SDDParams{
		PlanPath:  "docs/plans/p.md",
		Strategy:  acpwire.StrategyAdaptive,
		Overrides: map[string]string{"sdd_implementer": "kimi/k3"},
		Verify:    &acpwire.VerifyCommands{Build: "make build", Test: "make test"},
	}
	if err := sess.StartSDD(ctx, want); err != nil {
		t.Fatalf("StartSDD: %v", err)
	}
	if !reflect.DeepEqual(calls, []acpwire.SDDParams{want}) {
		t.Errorf("plan run params = %+v, want the params unchanged", calls)
	}
	if fake.runs != 1 {
		t.Errorf("the fake ran %d times, want once", fake.runs)
	}

	if err := sess.StartSDD(ctx, acpwire.SDDParams{}); err == nil {
		t.Error("StartSDD with no plan path = nil, want an error")
	}
	if err := sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md", Strategy: "fast"}); err == nil {
		t.Error("StartSDD with an unknown strategy = nil, want an error")
	}
	if len(calls) != 1 {
		t.Errorf("plan run params = %+v, want the refused runs to build nothing", calls)
	}

	bare := open(t, service.Deps{State: seamState(t, "s2")})
	if err := bare.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}); !acpclient.IsCode(err, acpwire.CodeNoRunner) {
		t.Errorf("StartSDD without a runtime plan runner = %v, want code %q", err, acpwire.CodeNoRunner)
	}

	failing := seamState(t, "s3")
	broke := open(t, service.Deps{
		State: failing,
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) {
			return nil, errors.New("boom")
		},
	})
	err := broke.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("StartSDD with a failing factory = %v, want the factory's error", err)
	}
	if stop := failing.CurrentTurnStop(); stop != nil {
		t.Errorf("a failed plan run left a stop group registered: %v", stop)
	}
}

// aGatedPlanWaitsForItsAnswer pins that a plan run its gate pauses returns the
// code, that the answer is recorded as the user's message before the run
// resumes, and that a second answer finds nothing paused.
func aGatedPlanWaitsForItsAnswer(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	fake := &fakePlanRun{gates: 1}
	sess := open(t, service.Deps{
		State:      state,
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) { return fake, nil },
	})

	gatedPlanRun(t, "a gated StartSDD", sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}))

	before := len(state.Messages())
	if err := sess.AnswerSDDGate(ctx, "use 30s"); err != nil {
		t.Fatalf("AnswerSDDGate: %v", err)
	}
	if !reflect.DeepEqual(fake.answers, []string{"use 30s"}) {
		t.Errorf("answers = %v, want the answer recorded", fake.answers)
	}
	if fake.runs != 2 {
		t.Errorf("the fake ran %d times, want twice", fake.runs)
	}
	msgs := state.Messages()
	if len(msgs) != before+1 {
		t.Fatalf("messages went from %d to %d, want one more", before, len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleUser || last.Content != "use 30s" {
		t.Errorf("last message = %v %q, want a user message with the answer", last.Role, last.Content)
	}

	refusedNoPausedPlan(t, "a second answer", sess.AnswerSDDGate(ctx, "again"))
	if got := len(state.Messages()); got != before+1 {
		t.Errorf("messages = %d, want no message from the refused answer", got)
	}
}

// aGateCanPauseTheRunAgain pins that a resumed run may pause a second time: the
// first answer returns the code, and the second resumes it to the end.
func aGateCanPauseTheRunAgain(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	fake := &fakePlanRun{gates: 2}
	sess := open(t, service.Deps{
		State:      seamState(t, "s1"),
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) { return fake, nil },
	})

	gatedPlanRun(t, "a gated StartSDD", sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}))
	gatedPlanRun(t, "the first answer", sess.AnswerSDDGate(ctx, "use 30s"))
	if err := sess.AnswerSDDGate(ctx, "and retries"); err != nil {
		t.Fatalf("the second answer = %v, want nil", err)
	}
	if fake.runs != 3 {
		t.Errorf("the fake ran %d times, want three", fake.runs)
	}
}

// stopSDDDropsThePausedRun pins that StopSDD clears the question and makes the
// paused run unanswerable.
func stopSDDDropsThePausedRun(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	fake := &fakePlanRun{gates: 1}
	sess := open(t, service.Deps{
		State:      state,
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) { return fake, nil },
	})

	gatedPlanRun(t, "a gated StartSDD", sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}))
	state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "which database?"})

	if err := sess.StopSDD(ctx); err != nil {
		t.Fatalf("StopSDD: %v", err)
	}
	if q := state.SDDGate().Question; q != "" {
		t.Errorf("the gate question = %q after StopSDD, want it cleared", q)
	}
	refusedNoPausedPlan(t, "an answer after StopSDD", sess.AnswerSDDGate(ctx, "postgres"))
}

// stopSDDClearsAStaleQuestion pins the branch that keeps a gate question from
// outliving its run: with nothing paused, StopSDD still clears it.
func stopSDDClearsAStaleQuestion(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "which database?"})
	if err := sess.StopSDD(context.Background()); err != nil {
		t.Fatalf("StopSDD with nothing paused: %v", err)
	}
	if q := state.SDDGate().Question; q != "" {
		t.Errorf("the gate question = %q after StopSDD, want it cleared", q)
	}
}

// aNewPlanRunReplacesAPausedOne pins that starting a new run drops the paused
// one, which can then no longer be answered.
func aNewPlanRunReplacesAPausedOne(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	a := &fakePlanRun{gates: 1}
	b := &fakePlanRun{}
	runs := []service.PlanRun{a, b}
	sess := open(t, service.Deps{
		State: seamState(t, "s1"),
		NewPlanRun: func(acpwire.SDDParams) (service.PlanRun, error) {
			run := runs[0]
			runs = runs[1:]
			return run, nil
		},
	})

	gatedPlanRun(t, "run A", sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/a.md"}))
	if err := sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/b.md"}); err != nil {
		t.Fatalf("run B = %v, want nil", err)
	}
	refusedNoPausedPlan(t, "an answer after run B", sess.AnswerSDDGate(ctx, "use 30s"))
	if a.runs != 1 {
		t.Errorf("run A ran %d times, want once", a.runs)
	}
}

// authorPlanReturnsTheCandidate pins that an authoring run forwards its
// parameters, reports the candidate it wrote as an ended turn, and is refused
// without a goal and without a runtime author.
func authorPlanReturnsTheCandidate(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	var calls []acpwire.AuthorPlanParams
	sess := open(t, service.Deps{
		State: seamState(t, "s1"),
		AuthorPlan: func(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
			calls = append(calls, params)
			return acpwire.AuthoredPlan{PlanPath: "/repo/p.md"}, nil
		},
	})

	want := acpwire.AuthorPlanParams{Goal: "add retries", Design: "a design"}
	got, err := sess.AuthorPlan(ctx, want)
	if err != nil {
		t.Fatalf("AuthorPlan: %v", err)
	}
	if got.PlanPath != "/repo/p.md" {
		t.Errorf("AuthorPlan plan path = %q, want the runtime's candidate", got.PlanPath)
	}
	if got.StopReason != acpwire.StopReasonEndTurn {
		t.Errorf("AuthorPlan stop reason = %q, want %q", got.StopReason, acpwire.StopReasonEndTurn)
	}
	if !reflect.DeepEqual(calls, []acpwire.AuthorPlanParams{want}) {
		t.Errorf("authoring calls = %+v, want the params unchanged", calls)
	}

	if _, err := sess.AuthorPlan(ctx, acpwire.AuthorPlanParams{}); err == nil {
		t.Error("AuthorPlan with an empty goal = nil error, want one")
	}
	if len(calls) != 1 {
		t.Errorf("authoring calls = %+v, want the goal-less run to call nothing", calls)
	}

	bare := open(t, service.Deps{State: seamState(t, "s2")})
	if _, err := bare.AuthorPlan(ctx, acpwire.AuthorPlanParams{Goal: "g"}); !acpclient.IsCode(err, acpwire.CodeNoRunner) {
		t.Errorf("AuthorPlan without a runtime author = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// aStoppedAuthoringReportsCancelled pins that a stopped authoring run reports
// StopReasonCancelled and no error.
func aStoppedAuthoringReportsCancelled(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	type result struct {
		plan acpwire.AuthoredPlan
		err  error
	}
	started := make(chan struct{})
	sess := open(t, service.Deps{
		State: seamState(t, "s1"),
		AuthorPlan: func(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
			close(started)
			<-ctx.Done()
			return acpwire.AuthoredPlan{}, ctx.Err()
		},
	})

	done := make(chan result, 1)
	go func() {
		plan, err := sess.AuthorPlan(context.Background(), acpwire.AuthorPlanParams{Goal: "g"})
		done <- result{plan: plan, err: err}
	}()
	waitClosed(t, started, "the authoring run")

	if err := sess.Stop(context.Background(), "stop the authoring"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("a stopped AuthorPlan = %v, want no error", got.err)
	}
	if got.plan.StopReason != acpwire.StopReasonCancelled {
		t.Errorf("a stopped AuthorPlan stop reason = %q, want %q", got.plan.StopReason, acpwire.StopReasonCancelled)
	}
}

// planPreflightForwardsWithoutTheSlot pins that a preflight forwards to the
// runtime even while a turn holds the session, and that an unknown strategy and
// a missing runtime preflight are refused.
func planPreflightForwardsWithoutTheSlot(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	runner := newFakeRunner()
	blocked := make(chan struct{})
	release := make(chan struct{})
	runner.setBody(func(ctx context.Context) error {
		close(blocked)
		<-release
		return nil
	})

	want := acpwire.PlanPreflight{
		Inspection: acpwire.PlanInspection{Strategy: acpwire.StrategyAdaptive},
		Verify:     acpwire.VerifyGate{Build: "go build ./...", Known: true},
	}
	var calls []acpwire.PlanPreflightParams
	sess := open(t, service.Deps{
		State:  seamState(t, "s1"),
		Runner: func() service.Runner { return runner },
		PlanPreflight: func(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
			calls = append(calls, params)
			return want, nil
		},
	})

	turn := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
		turn <- err
	}()
	waitClosed(t, blocked, "the turn holding the slot")

	params := acpwire.PlanPreflightParams{PlanPath: "docs/plans/p.md", Strategy: acpwire.StrategyStrict}
	got, err := sess.PlanPreflight(ctx, params)
	if err != nil {
		t.Fatalf("PlanPreflight while a turn runs: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlanPreflight = %+v, want what the runtime returned", got)
	}
	if !reflect.DeepEqual(calls, []acpwire.PlanPreflightParams{params}) {
		t.Errorf("preflight calls = %+v, want the params unchanged", calls)
	}

	close(release)
	if err := <-turn; err != nil {
		t.Fatalf("the turn holding the slot = %v, want nil", err)
	}

	if _, err := sess.PlanPreflight(ctx, acpwire.PlanPreflightParams{PlanPath: "docs/plans/p.md", Strategy: "fast"}); err == nil {
		t.Error("PlanPreflight with an unknown strategy = nil error, want one")
	}
	bare := open(t, service.Deps{State: seamState(t, "s2")})
	if _, err := bare.PlanPreflight(ctx, acpwire.PlanPreflightParams{PlanPath: "docs/plans/p.md"}); !acpclient.IsCode(err, acpwire.CodeNoRunner) {
		t.Errorf("PlanPreflight without a runtime preflight = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// endCallsForwardToTheEndService pins that the four forward calls reach the
// runtime unchanged: the work, the acceptance parameters, the wait and the exit
// analysis's goal.
func endCallsForwardToTheEndService(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	work := acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1", Label: "agent turn"}}}
	wantDecision := acpwire.EndDecision{Expanded: []string{"cmd-3"}, Work: work}
	fake := &fakeEndService{work: work, decision: wantDecision, waitErr: errors.New("join failed")}

	sess := open(t, service.Deps{State: seamState(t, "s1"), End: fake})

	if got := sess.ActiveWork(); !reflect.DeepEqual(got, work) {
		t.Errorf("ActiveWork() = %+v, want the service's %+v", got, work)
	}

	params := acpwire.EndAcceptParams{
		Intent:           acpwire.EndIntentExit,
		DisplayedIDs:     []string{"a"},
		RequireDisplayed: true,
	}
	decision, err := sess.AcceptEnd(ctx, params)
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !reflect.DeepEqual(decision, wantDecision) {
		t.Errorf("AcceptEnd = %+v, want the service's %+v", decision, wantDecision)
	}
	if got := fake.accepts(); !reflect.DeepEqual(got, []acpwire.EndAcceptParams{params}) {
		t.Errorf("AcceptEnd params = %+v, want them unchanged", got)
	}

	if err := sess.WaitForEnd(ctx); err == nil || err.Error() != "join failed" {
		t.Errorf("WaitForEnd = %v, want the service's join failed", err)
	}
	if got := fake.waitCount(); got != 1 {
		t.Errorf("WaitForEnd calls = %d, want 1", got)
	}

	if err := sess.RunExitAnalysis(ctx, "goal"); err != nil {
		t.Fatalf("RunExitAnalysis: %v", err)
	}
	if got := fake.analysisGoals(); !reflect.DeepEqual(got, []string{"goal"}) {
		t.Errorf("RunExitAnalysis goals = %v, want [goal]", got)
	}
}

// aSessionWithoutAnEndServiceEndsAtOnce pins the no-runtime fallback: no work to
// warn about, an acceptance, a settlement with nothing to join, and a refusal
// of the exit analysis.
func aSessionWithoutAnEndServiceEndsAtOnce(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	sess := open(t, service.Deps{State: seamState(t, "s1")})

	if got := sess.ActiveWork(); len(got.Items) != 0 {
		t.Errorf("ActiveWork() = %+v, want no items without an end service", got)
	}

	decision, err := sess.AcceptEnd(ctx, acpwire.EndAcceptParams{Intent: acpwire.EndIntentExit})
	if err != nil {
		t.Fatalf("AcceptEnd without an end service: %v", err)
	}
	if !decision.Accepted {
		t.Error("AcceptEnd = not accepted, want accepted without an end service")
	}
	if len(decision.Work.Items) != 0 {
		t.Errorf("AcceptEnd work = %+v, want no items", decision.Work)
	}

	if err := sess.WaitForEnd(ctx); err != nil {
		t.Errorf("WaitForEnd without an end service = %v, want nil", err)
	}
	if err := sess.RunExitAnalysis(ctx, "goal"); !acpclient.IsCode(err, acpwire.CodeNoRunner) {
		t.Errorf("RunExitAnalysis without an end service = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// postmortemWritesTheReport pins that the call writes the report where the
// TUI's exit path does, and that the file decodes as a report.
func postmortemWritesTheReport(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	path, err := sess.Postmortem(context.Background())
	if err != nil {
		t.Fatalf("Postmortem: %v", err)
	}
	wantDir := postmortem.Dir(home, postmortem.ProjectSlug(state.WorkingDir))
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("report dir = %q, want %q", got, wantDir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	var report postmortem.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode the report: %v", err)
	}
	if report.SchemaVersion != postmortem.SchemaVersion {
		t.Errorf("report schema version = %d, want %d", report.SchemaVersion, postmortem.SchemaVersion)
	}
	if report.Session.ID != "s1" {
		t.Errorf("report session ID = %q, want s1", report.Session.ID)
	}
}

// closeShutsTheSessionDown pins what an end does: it denies the open approval,
// shuts the state down, closes the change signal, keeps answering reads with
// their final values, and is idempotent.
func closeShutsTheSessionDown(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	responses := make(chan session.UserApprovalDecision, 1)
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "rm -rf build",
		ResponseChan: responses,
	})

	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case d := <-responses:
		if d.Approved {
			t.Error("the pending approval was approved, want a denial")
		}
	default:
		t.Error("the pending approval was not settled by Close")
	}

	select {
	case <-state.Done():
	default:
		t.Error("the state is not shut down after Close")
	}

	// Both arms close Changed at the close: the service's closes it, and the
	// client's retire, which Close runs once the agent has answered, closes its
	// mirror's. The seam does not carry the read, so it is asked of the value.
	changed, ok := sess.(interface{ Changed() <-chan struct{} })
	if !ok {
		t.Fatalf("%T has no Changed, want one on both arms", sess)
	}
	select {
	case <-changed.Changed():
	default:
		t.Error("Changed is not closed after Close")
	}

	if got := sess.ID(); got != "s1" {
		t.Errorf("ID() = %q after Close, want its final value s1", got)
	}
	if err := sess.Stop(context.Background(), "stop"); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Errorf("Stop after Close = %v, want code %q", err, acpwire.CodeSessionClosed)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Errorf("the second Close = %v, want nil", err)
	}
}

// closeCancelsTheRunInFlight pins that an end stops the turn: the run's context
// is cancelled, and the turn it was in reports cancelled, not failed.
func closeCancelsTheRunInFlight(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	fake := newFakeRunner()
	fake.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	sess := open(t, service.Deps{State: state, Runner: func() service.Runner { return fake }})

	type outcome struct {
		res acpwire.PromptResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "a long turn"})
		done <- outcome{res: res, err: err}
	}()
	fake.waitStart(t)

	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("Prompt after Close = %v, want no error", got.err)
	}
	if got.res.StopReason != acpwire.StopReasonCancelled {
		t.Errorf("StopReason = %q, want %q", got.res.StopReason, acpwire.StopReasonCancelled)
	}
}

// listSessionsReadsTheDatabase pins ListSessions' mapping: the sessions of one
// working directory, in order, with their IDs, titles and message counts, and
// none of another directory's.
func listSessionsReadsTheDatabase(t *testing.T, open func(t *testing.T, d service.Deps) (hostSeam, seam)) {
	database := browserDB(t)
	cwd := "/repo"
	projectID, err := database.GetOrCreateProject(cwd, "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	otherID, err := database.GetOrCreateProject("/other", "other")
	if err != nil {
		t.Fatalf("GetOrCreateProject other: %v", err)
	}

	if err := database.CreateSession("s1", projectID, "first session", time.Unix(1000, 0)); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if err := database.CreateSession("s2", projectID, "", time.Unix(2000, 0)); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}
	if err := database.CreateSession("s3", otherID, "elsewhere", time.Unix(3000, 0)); err != nil {
		t.Fatalf("CreateSession s3: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := database.SaveMessage("s1", "user", "hello", "text", time.Unix(1000+int64(i), 0), "", 0, true, 0); err != nil {
			t.Fatalf("SaveMessage s1: %v", err)
		}
	}
	if _, err := database.SaveMessage("s2", "user", "hi", "text", time.Unix(2000, 0), "", 0, true, 0); err != nil {
		t.Fatalf("SaveMessage s2: %v", err)
	}

	state := session.New(config.Default(), cwd, time.Unix(500, 0), session.Persistence{DB: database, SessionID: "s1"})
	host, _ := open(t, service.Deps{State: state, ProjectID: projectID})

	got, err := host.ListSessions(context.Background(), cwd, 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListSessions = %+v, want the two sessions of %q", got, cwd)
	}
	byID := map[string]acpwire.SessionEntry{}
	for _, entry := range got {
		byID[entry.SessionID] = entry
	}
	if _, ok := byID["s3"]; ok {
		t.Errorf("ListSessions listed s3, a session of another working directory")
	}
	first, ok := byID["s1"]
	if !ok {
		t.Fatalf("ListSessions = %+v, want s1", got)
	}
	if first.Title != "first session" {
		t.Errorf("s1 Title = %q, want %q", first.Title, "first session")
	}
	if first.MessageCount != 2 {
		t.Errorf("s1 MessageCount = %d, want 2", first.MessageCount)
	}
	second, ok := byID["s2"]
	if !ok {
		t.Fatalf("ListSessions = %+v, want s2", got)
	}
	if second.Title != "" {
		t.Errorf("s2 Title = %q, want empty", second.Title)
	}
	if second.MessageCount != 1 {
		t.Errorf("s2 MessageCount = %d, want 1", second.MessageCount)
	}
}

// listSessionsWithoutADatabaseIsCoded pins the code the host's session list
// fails with when the session has no database.
func listSessionsWithoutADatabaseIsCoded(t *testing.T, open func(t *testing.T, d service.Deps) (hostSeam, seam)) {
	host, _ := open(t, service.Deps{State: seamState(t, "s1")})
	if _, err := host.ListSessions(context.Background(), "/repo", 10); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("ListSessions error = %v, want CodeNoDatabase", err)
	}
}

// TestEndCallBudget holds R10's 1ms budget for an end call over the pipe on the
// 2,000-call session: the call includes its barrier, the end forwarder's fence
// included (P3 plan 6c).
func TestEndCallBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	// The end service accepts every choice and its membership never changes, so
	// nothing but the call and its barrier is timed.
	end := &fakeEndService{decision: acpwire.EndDecision{Accepted: true}}
	sess := openPipeSeamTB(t, service.Deps{State: st, End: end})

	// The first read is what subscribes: it installs the client's mirror and
	// starts the forwarder whose fence the call's barrier waits on.
	sess.ActiveWork()

	const runs = 30
	ctx := context.Background()
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if _, err := sess.AcceptEnd(ctx, acpwire.EndAcceptParams{Intent: acpwire.EndIntentExit}); err != nil {
			t.Fatalf("AcceptEnd: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	t.Logf("end call: median %v over %d runs (budget 1ms)", median, runs)
	if median >= time.Millisecond {
		t.Fatalf("end call median = %v, budget 1ms (R10): do not loosen it", median)
	}
}
