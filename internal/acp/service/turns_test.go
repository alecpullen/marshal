package service

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// fakeRunner is the agent runner the turn tests drive. It records each goal it
// gets, signals when Run starts, and runs whatever body the test set: a body
// that blocks is how a test holds a turn open.
type fakeRunner struct {
	mu      sync.Mutex
	goals   []string
	started chan struct{}
	body    func(ctx context.Context) error
	err     error
}

// newFakeRunner builds a runner that returns nil and signals each start.
func newFakeRunner() *fakeRunner {
	return &fakeRunner{started: make(chan struct{}, 1)}
}

func (f *fakeRunner) Run(ctx context.Context, goal string) error {
	f.mu.Lock()
	f.goals = append(f.goals, goal)
	body := f.body
	err := f.err
	f.mu.Unlock()

	select {
	case f.started <- struct{}{}:
	default:
	}
	if body != nil {
		return body(ctx)
	}
	return err
}

// setBody replaces what Run does after it records the goal.
func (f *fakeRunner) setBody(body func(ctx context.Context) error) {
	f.mu.Lock()
	f.body = body
	f.mu.Unlock()
}

// goalsSeen returns the goals Run was given, in order.
func (f *fakeRunner) goalsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.goals...)
}

// waitStart blocks until Run has started, or fails the test.
func (f *fakeRunner) waitStart(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the fake runner never started")
	}
}

// TestPromptRegistersItsWorkAndStopGroupWhileItRuns pins that a turn is
// registered as work and owns a stop group for as long as it runs, and that
// both are gone once it returns: WaitForWork proves the registration drained.
func TestPromptRegistersItsWorkAndStopGroupWhileItRuns(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	var registered bool
	fake.setBody(func(ctx context.Context) error {
		registered = state.CurrentTurnStop() != nil
		return nil
	})
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	if _, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !registered {
		t.Error("no stop group was registered while the run was in flight")
	}
	if state.CurrentTurnStop() != nil {
		t.Error("the stop group outlived the run")
	}

	state.BeginQuiesce()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := state.WaitForWork(ctx); err != nil {
		t.Errorf("WaitForWork after the run = %v, want nil: the turn's work was never released", err)
	}
}

// TestARunEndsIdle pins that ordinary cleanup returns the session to idle,
// whatever the run left the activity as.
func TestARunEndsIdle(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	fake.setBody(func(ctx context.Context) error {
		state.SetActivity(session.Activity{Kind: session.ActivityThinking})
		return nil
	})
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	if _, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := state.Activity().Kind; got != session.ActivityIdle {
		t.Errorf("activity = %q after the run, want %q", got, session.ActivityIdle)
	}
}

// TestAgentReadyFollowsTheRunner pins that AgentReady reads the runner the
// runtime reports now, not the one it had when the session was built.
func TestAgentReadyFollowsTheRunner(t *testing.T) {
	fake := newFakeRunner()
	var current Runner
	_, sess := New(Deps{State: newState(t, "s1"), Runner: func() Runner { return current }})

	if sess.Status().AgentReady {
		t.Error("AgentReady = true while the runtime reports no runner")
	}
	current = fake
	if !sess.Status().AgentReady {
		t.Error("AgentReady = false once the runtime reports a runner")
	}
}

// TestAStopRequestedBeforeTheTurnSkipsIt pins that a stop that lands before the
// turn registers its group is not lost: the registration adopts it and the run
// never starts.
func TestAStopRequestedBeforeTheTurnSkipsIt(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	if _, err := state.RequestTurnStop("early"); err != nil {
		t.Fatalf("RequestTurnStop: %v", err)
	}

	res, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "never runs"})
	if err != nil {
		t.Fatalf("Prompt = %v, want no error", err)
	}
	if res.StopReason != acpwire.StopReasonCancelled {
		t.Errorf("StopReason = %q, want %q", res.StopReason, acpwire.StopReasonCancelled)
	}
	if got := fake.goalsSeen(); len(got) != 0 {
		t.Errorf("runner goals = %v, want none", got)
	}
}

// TestAQueuedPromptSkippedByAnEarlyStopIsPutBack pins that a stop landing
// before the run began does not consume a queued text: the text never reached
// the model, so it goes back on the queue instead of vanishing.
func TestAQueuedPromptSkippedByAnEarlyStopIsPutBack(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	if _, err := state.RequestTurnStop("early"); err != nil {
		t.Fatalf("RequestTurnStop: %v", err)
	}

	res, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "kept", Queued: true})
	if err != nil {
		t.Fatalf("Prompt = %v, want no error", err)
	}
	if res.StopReason != acpwire.StopReasonCancelled {
		t.Errorf("StopReason = %q, want %q", res.StopReason, acpwire.StopReasonCancelled)
	}
	if got := fake.goalsSeen(); len(got) != 0 {
		t.Errorf("runner goals = %v, want none", got)
	}
	if got := state.SteeringQueue(); !reflect.DeepEqual(got, []string{"kept"}) {
		t.Errorf("SteeringQueue() = %v, want the skipped text back", got)
	}
}

func TestStopWithNothingRunningLeavesTheNextTurnAlone(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRunner()
	_, sess := New(Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }})

	if err := sess.Stop(ctx, "nothing is running"); err != nil {
		t.Fatalf("Stop with no run = %v, want nil", err)
	}

	res, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
	if err != nil {
		t.Fatalf("Prompt after an idle Stop: %v", err)
	}
	if res.StopReason != acpwire.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want %q", res.StopReason, acpwire.StopReasonEndTurn)
	}
	if got := fake.goalsSeen(); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Errorf("runner goals = %v, want [hello]", got)
	}
}

// TestAPanickingRunnerFailsTheTurn pins that a panicking run becomes the turn's
// error and leaves nothing registered, so the next turn still runs.
func TestAPanickingRunnerFailsTheTurn(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	fake := newFakeRunner()
	fake.setBody(func(ctx context.Context) error { panic("boom") })
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	if _, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "hello"}); err == nil ||
		!strings.Contains(err.Error(), "panic in agent run") {
		t.Fatalf("Prompt with a panicking runner = %v, want a panic error", err)
	}
	if state.CurrentTurnStop() != nil {
		t.Error("a panicking run left a stop group registered")
	}

	fake.setBody(nil)
	res, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "again"})
	if err != nil {
		t.Fatalf("the next Prompt: %v", err)
	}
	if res.StopReason != acpwire.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want %q", res.StopReason, acpwire.StopReasonEndTurn)
	}
}

// TestAClosedSessionRunsNothing pins the closed-session refusal for all six
// turn calls: a session a swap replaced answers each with CodeSessionClosed and
// starts nothing.
func TestAClosedSessionRunsNothing(t *testing.T) {
	ctx := context.Background()
	fake := newFakeRunner()
	host, old := New(Deps{
		State:          newState(t, "s1"),
		Runner:         func() Runner { return fake },
		Review:         func(context.Context, acpwire.ReviewParams) error { return nil },
		RunAgent:       func(context.Context, acpwire.AgentRunParams) error { return nil },
		Suggest:        func(context.Context, string) (string, error) { return "an answer", nil },
		VerifyProposal: func(context.Context) (acpwire.VerifyCommands, error) { return acpwire.VerifyCommands{}, nil },
	})
	host.swap(newState(t, "s2"))

	refused := func(name string, err error) {
		t.Helper()
		if code := closedCode(err); code != acpwire.CodeSessionClosed {
			t.Errorf("%s on a closed session = %v, want code %q", name, err, acpwire.CodeSessionClosed)
		}
	}

	_, err := old.Prompt(ctx, acpwire.PromptParams{Text: "hello"})
	refused("Prompt", err)
	refused("Stop", old.Stop(ctx, "stop"))
	refused("Review", old.Review(ctx, acpwire.ReviewParams{}))
	refused("RunAgent", old.RunAgent(ctx, acpwire.AgentRunParams{Agent: "linter"}))
	_, err = old.Suggest(ctx, "hi")
	refused("Suggest", err)
	_, err = old.VerifyProposal(ctx)
	refused("VerifyProposal", err)

	if got := fake.goalsSeen(); len(got) != 0 {
		t.Errorf("runner goals = %v, want none on a closed session", got)
	}
}
