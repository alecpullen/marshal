package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
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
