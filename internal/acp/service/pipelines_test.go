package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// fakePlanRun is the plan run the pipeline tests drive. It gates its first
// gates runs, records each answer and its run count, and can block until its
// context is done after signalling that it started.
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

// gateAfterStopRun is a plan run whose first Run waits for release before it
// reports the human gate, so a test can land a stop while the gate is being
// reached.
type gateAfterStopRun struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gateAfterStopRun) Run(context.Context) error {
	g.once.Do(func() { close(g.started) })
	if g.release != nil {
		<-g.release
	}
	return &acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: "pipeline: human gate required"}
}

func (g *gateAfterStopRun) Answer(string) {}

// TestStartSwarmRunsOnTheRunPath pins that a swarm runs on the shared run
// path, gets its parameters unchanged, and that a goal-less run and a session
// with no swarm runner are both refused before anything starts.
func TestStartSwarmRunsOnTheRunPath(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	var calls []acpwire.SwarmParams
	var registered bool
	_, sess := New(Deps{
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

	_, bare := New(Deps{State: newState(t, "s2")})
	if err := bare.StartSwarm(ctx, acpwire.SwarmParams{Goal: "g"}); closedCode(err) != acpwire.CodeNoRunner {
		t.Errorf("StartSwarm without a runtime swarm = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// TestAStoppedSwarmIsNotAnError pins that a swarm that returns its own context
// error once stopped still leaves StartSwarm with no error: the stop reported
// the cancellation.
func TestAStoppedSwarmIsNotAnError(t *testing.T) {
	started := make(chan struct{})
	_, sess := New(Deps{
		State: newState(t, "s1"),
		Swarm: func(ctx context.Context, params acpwire.SwarmParams) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})

	done := make(chan error, 1)
	go func() { done <- sess.StartSwarm(context.Background(), acpwire.SwarmParams{Goal: "g"}) }()
	<-started

	if err := sess.Stop(context.Background(), "stop the swarm"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a stopped StartSwarm = %v, want nil", err)
	}
}

// TestStartSDDBuildsThePlanWhenItStarts pins that a plan run is built from the
// caller's parameters on the run path, and that a run with no path, an unknown
// strategy, no runner and a failing factory are all refused without leaving a
// turn behind.
func TestStartSDDBuildsThePlanWhenItStarts(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	fake := &fakePlanRun{}
	var calls []acpwire.SDDParams
	_, sess := New(Deps{
		State: state,
		NewPlanRun: func(params acpwire.SDDParams) (PlanRun, error) {
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

	_, bare := New(Deps{State: newState(t, "s2")})
	if err := bare.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}); closedCode(err) != acpwire.CodeNoRunner {
		t.Errorf("StartSDD without a runtime plan runner = %v, want code %q", err, acpwire.CodeNoRunner)
	}

	failing := newState(t, "s3")
	_, broke := New(Deps{
		State: failing,
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) {
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

// TestAGatedPlanWaitsForItsAnswer pins that a plan run its gate pauses returns
// the code, that the answer is recorded as the user's message before the run
// resumes, and that a second answer finds nothing paused.
func TestAGatedPlanWaitsForItsAnswer(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	fake := &fakePlanRun{gates: 1}
	_, sess := New(Deps{
		State:      state,
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) { return fake, nil },
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

	if err := sess.AnswerSDDGate(ctx, "again"); !errors.Is(err, errNoPausedPlan) {
		t.Errorf("a second answer = %v, want errNoPausedPlan", err)
	}
	if got := len(state.Messages()); got != before+1 {
		t.Errorf("messages = %d, want no message from the refused answer", got)
	}
}

// TestAGateCanPauseTheRunAgain pins that a resumed run may pause a second time:
// the first answer returns the code, and the second resumes it to the end.
func TestAGateCanPauseTheRunAgain(t *testing.T) {
	ctx := context.Background()
	fake := &fakePlanRun{gates: 2}
	_, sess := New(Deps{
		State:      newState(t, "s1"),
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) { return fake, nil },
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

// TestAnAnswerNeedsAPausedRunAndText pins that an answer is refused when no run
// is paused and when it carries no text, and that neither refusal records
// anything.
func TestAnAnswerNeedsAPausedRunAndText(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	if err := sess.AnswerSDDGate(ctx, "x"); !errors.Is(err, errNoPausedPlan) {
		t.Errorf("an answer with nothing paused = %v, want errNoPausedPlan", err)
	}

	fake := &fakePlanRun{}
	sess.mu.Lock()
	sess.paused = fake
	sess.mu.Unlock()

	before := len(state.Messages())
	if err := sess.AnswerSDDGate(ctx, "  "); !errors.Is(err, errNoAnswer) {
		t.Errorf("a blank answer = %v, want errNoAnswer", err)
	}
	if got := len(state.Messages()); got != before {
		t.Errorf("messages = %d, want none added by the refusals", got)
	}
	if len(fake.answers) != 0 || fake.runs != 0 {
		t.Errorf("the paused run saw %d answers and %d runs, want none", len(fake.answers), fake.runs)
	}
}

// TestStopSDDDropsThePausedRun pins that StopSDD clears the question and makes
// the paused run unanswerable.
func TestStopSDDDropsThePausedRun(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	fake := &fakePlanRun{gates: 1}
	_, sess := New(Deps{
		State:      state,
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) { return fake, nil },
	})

	gatedPlanRun(t, "a gated StartSDD", sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}))
	state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "which database?"})

	if err := sess.StopSDD(ctx); err != nil {
		t.Fatalf("StopSDD: %v", err)
	}
	if q := state.SDDGate().Question; q != "" {
		t.Errorf("the gate question = %q after StopSDD, want it cleared", q)
	}
	if err := sess.AnswerSDDGate(ctx, "postgres"); !errors.Is(err, errNoPausedPlan) {
		t.Errorf("an answer after StopSDD = %v, want errNoPausedPlan", err)
	}
}

// TestStopSDDClearsAStaleQuestion pins the branch that keeps a gate question
// from outliving its run: with nothing paused, StopSDD still clears it.
func TestStopSDDClearsAStaleQuestion(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "which database?"})
	if err := sess.StopSDD(context.Background()); err != nil {
		t.Fatalf("StopSDD with nothing paused: %v", err)
	}
	if q := state.SDDGate().Question; q != "" {
		t.Errorf("the gate question = %q after StopSDD, want it cleared", q)
	}
}

// TestAStopThatReachesAGatedRunStillReportsTheGate pins that a stop landing on
// a run that reached its human gate does not swallow the gate code: the run is
// paused and awaiting an answer the stop did not cancel, so the TUI has to
// hear about it or the paused run waits for nothing.
func TestAStopThatReachesAGatedRunStillReportsTheGate(t *testing.T) {
	run := &gateAfterStopRun{started: make(chan struct{}), release: make(chan struct{})}
	_, sess := New(Deps{
		State:      newState(t, "s1"),
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) { return run, nil },
	})

	done := make(chan error, 1)
	go func() {
		done <- sess.StartSDD(context.Background(), acpwire.SDDParams{PlanPath: "docs/plans/p.md"})
	}()
	<-run.started
	if err := sess.Stop(context.Background(), "stop the run"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	close(run.release)

	gatedPlanRun(t, "a stopped StartSDD that reached its gate", <-done)

	sess.mu.Lock()
	paused := sess.paused
	sess.mu.Unlock()
	if paused != PlanRun(run) {
		t.Errorf("the paused run = %v, want the run the gate left paused", paused)
	}
}

// TestANewPlanRunReplacesAPausedOne pins that starting a new run drops the
// paused one, which can then no longer be answered.
func TestANewPlanRunReplacesAPausedOne(t *testing.T) {
	ctx := context.Background()
	a := &fakePlanRun{gates: 1}
	b := &fakePlanRun{}
	runs := []PlanRun{a, b}
	_, sess := New(Deps{
		State: newState(t, "s1"),
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) {
			run := runs[0]
			runs = runs[1:]
			return run, nil
		},
	})

	gatedPlanRun(t, "run A", sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/a.md"}))
	if err := sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/b.md"}); err != nil {
		t.Fatalf("run B = %v, want nil", err)
	}
	if err := sess.AnswerSDDGate(ctx, "use 30s"); !errors.Is(err, errNoPausedPlan) {
		t.Errorf("an answer after run B = %v, want errNoPausedPlan", err)
	}
	if a.runs != 1 {
		t.Errorf("run A ran %d times, want once", a.runs)
	}
}

// TestARefusedAnswerKeepsTheRunPaused pins decision 5: an answer the session
// refuses at admission never reaches the run, so the run stays paused and can
// be answered once the session admits work again.
func TestARefusedAnswerKeepsTheRunPaused(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	fake := &fakePlanRun{gates: 1}
	_, sess := New(Deps{
		State:      state,
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) { return fake, nil },
	})

	gatedPlanRun(t, "a gated StartSDD", sess.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}))
	state.BeginQuiesce()

	before := len(state.Messages())
	err := sess.AnswerSDDGate(ctx, "use 30s")
	if !acpclient.IsCode(err, acpwire.CodeQuiescing) {
		t.Fatalf("an answer while quiescing = %v, want code %q", err, acpwire.CodeQuiescing)
	}
	if got := len(state.Messages()); got != before {
		t.Errorf("messages = %d, want none added by the refused answer", got)
	}
	if len(fake.answers) != 0 {
		t.Errorf("the run recorded answers %v, want none", fake.answers)
	}

	sess.mu.Lock()
	paused := sess.paused
	sess.mu.Unlock()
	if paused != PlanRun(fake) {
		t.Errorf("the paused run = %v, want the run the refusal left paused", paused)
	}
}

// TestAuthorPlanReturnsTheCandidate pins that an authoring run forwards its
// parameters, reports the candidate it wrote as an ended turn, and is refused
// without a goal and without a runtime author.
func TestAuthorPlanReturnsTheCandidate(t *testing.T) {
	ctx := context.Background()
	var calls []acpwire.AuthorPlanParams
	_, sess := New(Deps{
		State: newState(t, "s1"),
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

	_, bare := New(Deps{State: newState(t, "s2")})
	if _, err := bare.AuthorPlan(ctx, acpwire.AuthorPlanParams{Goal: "g"}); closedCode(err) != acpwire.CodeNoRunner {
		t.Errorf("AuthorPlan without a runtime author = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// TestAStoppedAuthoringReportsCancelled pins that a stopped authoring run
// reports StopReasonCancelled and no error.
func TestAStoppedAuthoringReportsCancelled(t *testing.T) {
	type result struct {
		plan acpwire.AuthoredPlan
		err  error
	}
	started := make(chan struct{})
	_, sess := New(Deps{
		State: newState(t, "s1"),
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
	<-started

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

// TestPlanPreflightForwardsWithoutTheSlot pins that a preflight forwards to the
// runtime even while a turn holds the session, and that an unknown strategy and
// a missing runtime preflight are refused.
func TestPlanPreflightForwardsWithoutTheSlot(t *testing.T) {
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
	_, sess := New(Deps{
		State:  newState(t, "s1"),
		Runner: func() Runner { return runner },
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
	<-blocked

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
	_, bare := New(Deps{State: newState(t, "s2")})
	if _, err := bare.PlanPreflight(ctx, acpwire.PlanPreflightParams{PlanPath: "docs/plans/p.md"}); closedCode(err) != acpwire.CodeNoRunner {
		t.Errorf("PlanPreflight without a runtime preflight = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// TestAClosedSessionStartsNoPipeline pins the closed-session refusal for all six
// pipeline calls: a session a swap replaced answers each with CodeSessionClosed
// and starts nothing.
func TestAClosedSessionStartsNoPipeline(t *testing.T) {
	ctx := context.Background()
	fake := &fakePlanRun{}
	var called bool
	host, old := New(Deps{
		State: newState(t, "s1"),
		Swarm: func(context.Context, acpwire.SwarmParams) error {
			called = true
			return nil
		},
		NewPlanRun: func(acpwire.SDDParams) (PlanRun, error) {
			called = true
			return fake, nil
		},
		PlanPreflight: func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
			called = true
			return acpwire.PlanPreflight{}, nil
		},
		AuthorPlan: func(context.Context, acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
			called = true
			return acpwire.AuthoredPlan{}, nil
		},
	})
	host.swap(newState(t, "s2"))

	refused := func(name string, err error) {
		t.Helper()
		if code := closedCode(err); code != acpwire.CodeSessionClosed {
			t.Errorf("%s on a closed session = %v, want code %q", name, err, acpwire.CodeSessionClosed)
		}
	}

	refused("StartSwarm", old.StartSwarm(ctx, acpwire.SwarmParams{Goal: "g"}))
	_, err := old.PlanPreflight(ctx, acpwire.PlanPreflightParams{PlanPath: "docs/plans/p.md"})
	refused("PlanPreflight", err)
	refused("StartSDD", old.StartSDD(ctx, acpwire.SDDParams{PlanPath: "docs/plans/p.md"}))
	refused("AnswerSDDGate", old.AnswerSDDGate(ctx, "use 30s"))
	refused("StopSDD", old.StopSDD(ctx))
	_, err = old.AuthorPlan(ctx, acpwire.AuthorPlanParams{Goal: "g"})
	refused("AuthorPlan", err)

	if called {
		t.Error("a closed session called the runtime")
	}
	if fake.runs != 0 {
		t.Errorf("the plan run ran %d times, want none on a closed session", fake.runs)
	}
}
