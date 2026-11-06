package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/watch"
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

// fakeTurnStopService records the turn stops a session issues, so a test can
// prove the command service heard the stop BEFORE the run's context was
// cancelled.
type fakeTurnStopService struct {
	mu         sync.Mutex
	turnIDs    []string
	causes     []string
	targets    []string
	onStopFunc func()
}

func (f *fakeTurnStopService) StopAttached(turnID, cause string) ([]string, error) {
	f.mu.Lock()
	f.turnIDs = append(f.turnIDs, turnID)
	f.causes = append(f.causes, cause)
	fn := f.onStopFunc
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	return []string{"job-1"}, nil
}

func (f *fakeTurnStopService) RequestStop(executionID, cause string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, executionID)
	return nil
}

// snapshot returns the turn ids and causes the service saw.
func (f *fakeTurnStopService) snapshot() (turnIDs, causes []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.turnIDs...), append([]string(nil), f.causes...)
}

// TestPromptRunsTheRunner pins the happy path: one prompt runs one runner turn
// with the user's text and ends the turn.
func TestPromptRunsTheRunner(t *testing.T) {
	fake := newFakeRunner()
	_, sess := New(Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }})

	res, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.StopReason != acpwire.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want %q", res.StopReason, acpwire.StopReasonEndTurn)
	}
	if got := fake.goalsSeen(); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Errorf("runner goals = %v, want [hello]", got)
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

// TestPromptWithoutARunnerIsRefused pins the no-runner refusal: no runner func
// and a runner func that returns nil both mean the provider is not built, so
// no turn starts, AgentReady is false, and a queued text goes back on the
// queue while an unqueued one does not.
func TestPromptWithoutARunnerIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		runner func() Runner
	}{
		{name: "no runner func", runner: nil},
		{name: "runner func returns nil", runner: func() Runner { return nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state := newState(t, "s1")
			_, sess := New(Deps{State: state, Runner: tc.runner})

			res, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "queued text", Queued: true})
			if code := closedCode(err); code != acpwire.CodeNoRunner {
				t.Fatalf("Prompt = %v, want code %q", err, acpwire.CodeNoRunner)
			}
			if res != (acpwire.PromptResult{}) {
				t.Errorf("Prompt result = %+v, want the zero result", res)
			}
			if sess.Status().AgentReady {
				t.Error("AgentReady = true without a runner")
			}
			if got := state.SteeringQueue(); !reflect.DeepEqual(got, []string{"queued text"}) {
				t.Errorf("SteeringQueue() = %v, want the refused text back", got)
			}

			if _, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "typed now"}); closedCode(err) != acpwire.CodeNoRunner {
				t.Fatalf("Prompt with unqueued text = %v, want code %q", err, acpwire.CodeNoRunner)
			}
			if got := state.SteeringQueue(); !reflect.DeepEqual(got, []string{"queued text"}) {
				t.Errorf("SteeringQueue() = %v after an unqueued refusal, want only the queued text", got)
			}
		})
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

// TestASecondRunIsBusy pins one run at a time: while a prompt is in flight a
// second prompt and a review are refused with CodeBusy, and the refused
// prompt's text goes back on the queue.
func TestASecondRunIsBusy(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	fake := newFakeRunner()
	release := make(chan struct{})
	fake.setBody(func(ctx context.Context) error {
		<-release
		return nil
	})
	_, sess := New(Deps{
		State:  state,
		Runner: func() Runner { return fake },
		Review: func(context.Context, acpwire.ReviewParams) error { return nil },
	})

	done := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "first"})
		done <- err
	}()
	fake.waitStart(t)

	res, err := sess.Prompt(ctx, acpwire.PromptParams{Text: "second", Queued: true})
	if code := closedCode(err); code != acpwire.CodeBusy {
		t.Fatalf("the second Prompt = %v, want code %q", err, acpwire.CodeBusy)
	}
	if res != (acpwire.PromptResult{}) {
		t.Errorf("the refused result = %+v, want the zero result", res)
	}
	if got := state.SteeringQueue(); !reflect.DeepEqual(got, []string{"second"}) {
		t.Errorf("SteeringQueue() = %v, want the refused text back", got)
	}
	if err := sess.Review(ctx, acpwire.ReviewParams{}); closedCode(err) != acpwire.CodeBusy {
		t.Errorf("Review while a run is in flight = %v, want code %q", err, acpwire.CodeBusy)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the first Prompt: %v", err)
	}
}

// TestAnEndingSessionRefusesThePrompt pins the quiescing refusal: the gate is
// asked before anything starts, so the runner is never called, no stop group
// is left behind, and the text goes back on the queue.
func TestAnEndingSessionRefusesThePrompt(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	state.BeginQuiesce()

	res, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "too late", Queued: true})
	if code := closedCode(err); code != acpwire.CodeQuiescing {
		t.Fatalf("Prompt on a quiescing session = %v, want code %q", err, acpwire.CodeQuiescing)
	}
	if err == nil || err.Error() != "session is quiescing" {
		t.Errorf("Prompt error = %v, want the state's quiescing message", err)
	}
	if res != (acpwire.PromptResult{}) {
		t.Errorf("Prompt result = %+v, want the zero result", res)
	}
	if got := fake.goalsSeen(); len(got) != 0 {
		t.Errorf("runner goals = %v, want none", got)
	}
	if state.CurrentTurnStop() != nil {
		t.Error("a refused prompt left a stop group registered")
	}
	if got := state.SteeringQueue(); !reflect.DeepEqual(got, []string{"too late"}) {
		t.Errorf("SteeringQueue() = %v, want the refused text back", got)
	}
}

// TestStopStopsTheGroupBeforeTheRun pins the ordering Stop owes the command
// service: the attached commands are stopped first, and only then is the run's
// context cancelled. The run reports the cancellation itself, so the stop came
// from the turn, not from the runner.
func TestStopStopsTheGroupBeforeTheRun(t *testing.T) {
	state := newState(t, "s1")
	svc := &fakeTurnStopService{}
	state.SetTurnStopService(svc)

	var mu sync.Mutex
	var order []string
	svc.mu.Lock()
	svc.onStopFunc = func() {
		mu.Lock()
		order = append(order, "service-stop")
		mu.Unlock()
	}
	svc.mu.Unlock()

	fake := newFakeRunner()
	fake.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		mu.Lock()
		order = append(order, "agent-cancel")
		mu.Unlock()
		return ctx.Err()
	})
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

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

	if err := sess.Stop(context.Background(), "turn stopped"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("Prompt after Stop = %v, want no error", got.err)
	}
	if got.res.StopReason != acpwire.StopReasonCancelled {
		t.Errorf("StopReason = %q, want %q", got.res.StopReason, acpwire.StopReasonCancelled)
	}

	mu.Lock()
	ordered := append([]string(nil), order...)
	mu.Unlock()
	if !reflect.DeepEqual(ordered, []string{"service-stop", "agent-cancel"}) {
		t.Errorf("stop order = %v, want the service stop before the agent cancel", ordered)
	}
	if _, causes := svc.snapshot(); !reflect.DeepEqual(causes, []string{"turn stopped"}) {
		t.Errorf("service causes = %v, want [turn stopped]", causes)
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

// TestAStoppedRunThatStartedKeepsItsText pins the other half: once the run has
// begun, a stop consumes the text, so a queued text is not re-run.
func TestAStoppedRunThatStartedKeepsItsText(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	fake.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	done := make(chan acpwire.PromptResult, 1)
	go func() {
		res, _ := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "consumed", Queued: true})
		done <- res
	}()
	fake.waitStart(t)
	if err := sess.Stop(context.Background(), "user stop"); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if res := <-done; res.StopReason != acpwire.StopReasonCancelled {
		t.Errorf("StopReason = %q, want %q", res.StopReason, acpwire.StopReasonCancelled)
	}
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Errorf("SteeringQueue() = %v, want empty: the run consumed the text", got)
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

// TestReviewRunsOnTheRunPath pins that a review takes the same path a prompt
// does — the stop group is registered while it runs and the params reach the
// runtime unchanged — and that a review naming both a range and a base is
// refused before anything starts.
func TestReviewRunsOnTheRunPath(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	var calls []acpwire.ReviewParams
	var registered bool
	_, sess := New(Deps{
		State: state,
		Review: func(ctx context.Context, params acpwire.ReviewParams) error {
			calls = append(calls, params)
			registered = state.CurrentTurnStop() != nil
			return nil
		},
	})

	want := acpwire.ReviewParams{Focus: "main.go", Base: "main"}
	if err := sess.Review(ctx, want); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if !reflect.DeepEqual(calls, []acpwire.ReviewParams{want}) {
		t.Errorf("review calls = %+v, want the params unchanged", calls)
	}
	if !registered {
		t.Error("no stop group was registered while the review ran")
	}

	if err := sess.Review(ctx, acpwire.ReviewParams{Range: "a..b", Base: "main"}); !errors.Is(err, errRangeAndBase) {
		t.Errorf("Review with a range and a base = %v, want errRangeAndBase", err)
	}
	if len(calls) != 1 {
		t.Errorf("review calls = %+v, want the refused review to call nothing", calls)
	}

	_, bare := New(Deps{State: newState(t, "s2")})
	if err := bare.Review(ctx, acpwire.ReviewParams{}); closedCode(err) != acpwire.CodeNoRunner {
		t.Errorf("Review without a runtime review = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// TestRunAgentRunsOnTheRunPath pins the same path for a custom agent, and that
// an agent run with no name is refused before anything starts.
func TestRunAgentRunsOnTheRunPath(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	var calls []acpwire.AgentRunParams
	var registered bool
	_, sess := New(Deps{
		State: state,
		RunAgent: func(ctx context.Context, params acpwire.AgentRunParams) error {
			calls = append(calls, params)
			registered = state.CurrentTurnStop() != nil
			return nil
		},
	})

	want := acpwire.AgentRunParams{Agent: "linter", Goal: "check style"}
	if err := sess.RunAgent(ctx, want); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !reflect.DeepEqual(calls, []acpwire.AgentRunParams{want}) {
		t.Errorf("agent run calls = %+v, want the params unchanged", calls)
	}
	if !registered {
		t.Error("no stop group was registered while the agent run ran")
	}

	if err := sess.RunAgent(ctx, acpwire.AgentRunParams{}); err == nil {
		t.Error("RunAgent with no agent name = nil, want an error")
	}
	if len(calls) != 1 {
		t.Errorf("agent run calls = %+v, want the nameless run to call nothing", calls)
	}
}

// TestAStoppedReviewIsNotAnError pins that a reviewer that returns its own
// context error once stopped still leaves Review with no error: the stop
// reported the cancellation.
func TestAStoppedReviewIsNotAnError(t *testing.T) {
	started := make(chan struct{})
	_, sess := New(Deps{
		State: newState(t, "s1"),
		Review: func(ctx context.Context, params acpwire.ReviewParams) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})

	done := make(chan error, 1)
	go func() { done <- sess.Review(context.Background(), acpwire.ReviewParams{}) }()
	<-started

	if err := sess.Stop(context.Background(), "stop the review"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a stopped Review = %v, want nil", err)
	}
}

// TestSuggestAndVerifyProposalForward pins that the two one-shot calls forward
// to the runtime and fail when the runtime supplied neither.
func TestSuggestAndVerifyProposalForward(t *testing.T) {
	ctx := context.Background()
	want := acpwire.VerifyCommands{Build: "go build ./...", Test: "go test ./..."}
	_, sess := New(Deps{
		State: newState(t, "s1"),
		Suggest: func(ctx context.Context, lastMessage string) (string, error) {
			if lastMessage != "thanks" {
				t.Errorf("Suggest got %q, want the caller's message", lastMessage)
			}
			return "you're welcome", nil
		},
		VerifyProposal: func(ctx context.Context) (acpwire.VerifyCommands, error) {
			return want, nil
		},
	})

	got, err := sess.Suggest(ctx, "thanks")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if got != "you're welcome" {
		t.Errorf("Suggest = %q, want the runtime's answer", got)
	}
	cmds, err := sess.VerifyProposal(ctx)
	if err != nil {
		t.Fatalf("VerifyProposal: %v", err)
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("VerifyProposal = %+v, want %+v", cmds, want)
	}

	_, bare := New(Deps{State: newState(t, "s2")})
	if _, err := bare.Suggest(ctx, "hi"); err == nil {
		t.Error("Suggest without a runtime suggestion = nil error, want one")
	}
	if _, err := bare.VerifyProposal(ctx); err == nil {
		t.Error("VerifyProposal without a runtime proposal = nil error, want one")
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

// TestPromptWritesTheWatchResumeGoal pins that a resume turn's text comes from
// the watch and the session's own last assistant reply, not from params.Text.
func TestPromptWritesTheWatchResumeGoal(t *testing.T) {
	fake := newFakeRunner()
	state := newState(t, "s1")
	state.AddMessageFinal(session.RoleAssistant, "I fixed the build.", session.ContentTypePlain)
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	params := acpwire.PromptParams{
		Text:        "ignored",
		WatchResume: &acpwire.WatchResume{Name: "ci", Repeat: true},
	}
	if _, err := sess.Prompt(context.Background(), params); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	want := watch.ResumeGoal("ci", "I fixed the build.", true)
	if got := fake.goalsSeen(); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("runner goals = %q, want [%q]", got, want)
	}
}

// TestPromptCodesAProviderError pins decision 4: a provider failure leaves the
// run coded, with the banner its caller shows carried in the error's data.
func TestPromptCodesAProviderError(t *testing.T) {
	fake := newFakeRunner()
	fake.err = &provider.ProviderError{Provider: "openai", StatusCode: 500, Body: "boom"}
	_, sess := New(Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }})

	_, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
	if err == nil {
		t.Fatal("Prompt error = nil, want the coded provider error")
	}
	var coded *acpwire.Error
	if !errors.As(err, &coded) {
		t.Fatalf("err = %T, want *acpwire.Error", err)
	}
	if coded.Code != acpwire.CodeProviderError {
		t.Errorf("code = %q, want %q", coded.Code, acpwire.CodeProviderError)
	}
	n, ok := acpclient.NoticeOf(err)
	if !ok {
		t.Fatal("the coded error must carry the notice")
	}
	if n.Category != acpwire.NoticeProvider || n.Hint == "" {
		t.Errorf("notice = %+v, want a provider notice with a hint", n)
	}
}

// TestPromptPassesOtherErrorsThrough pins the other half of decision 4: an
// error the agent did not classify reaches the caller as the same value.
func TestPromptPassesOtherErrorsThrough(t *testing.T) {
	fake := newFakeRunner()
	plain := errors.New("boom")
	fake.err = plain
	_, sess := New(Deps{State: newState(t, "s1"), Runner: func() Runner { return fake }})

	_, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
	if err != plain {
		t.Fatalf("err = %v, want the same error value back", err)
	}
}
