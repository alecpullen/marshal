package acp

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/llm/provider"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// fakeRunner is the agent runner the turn cases drive. It records each goal it
// gets, signals when Run starts, and runs whatever body the case set: a body
// that blocks is how a case holds a turn open. It is the service package's
// fakeRunner, copied here so the harness does not depend on its package.
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

// waitStart blocks until Run has started, or fails the case.
func (f *fakeRunner) waitStart(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the fake runner never started")
	}
}

// fakeTurnStopService records the turn stops a session issues, so a case can
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

// fakeWork is a viewmodel.WorkService for the work cases. It records each
// call, and publish sends to every open subscription. It is the service
// package's fakeWork, copied here so the harness does not depend on its
// package.
type fakeWork struct {
	mu      sync.Mutex
	snap    viewmodel.WorkSnapshot
	subs    int
	open    []chan pubsub.Event[viewmodel.WorkSnapshot]
	queries []viewmodel.WorkQuery
	page    viewmodel.WorkPage
	pageErr error
	inspect []fakeInspect
	detail  viewmodel.WorkDetail
	stops   []fakeStop
	stop    viewmodel.WorkStopResult
	stopErr error
}

type fakeInspect struct {
	target viewmodel.WorkTarget
	raw    bool
}

type fakeStop struct {
	target viewmodel.WorkTarget
	reason string
}

func (f *fakeWork) WorkSnapshot() viewmodel.WorkSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

// setSnapshot replaces the snapshot a later seed reads.
func (f *fakeWork) setSnapshot(snap viewmodel.WorkSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = snap
}

// SubscribeWork closes the channel when the context ends.
func (f *fakeWork) SubscribeWork(ctx context.Context) <-chan pubsub.Event[viewmodel.WorkSnapshot] {
	f.mu.Lock()
	f.subs++
	ch := make(chan pubsub.Event[viewmodel.WorkSnapshot], 16)
	f.open = append(f.open, ch)
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		for i, c := range f.open {
			if c == ch {
				f.open = append(f.open[:i], f.open[i+1:]...)
				break
			}
		}
		close(ch)
		f.mu.Unlock()
	}()
	return ch
}

// publish delivers a snapshot on the newest open subscription without blocking.
// It holds the mutex across the send so it can never race a cancel's close.
func (f *fakeWork) publish(snap viewmodel.WorkSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.open) == 0 {
		return
	}
	ch := f.open[len(f.open)-1]
	select {
	case ch <- pubsub.Event[viewmodel.WorkSnapshot]{Type: "work", Payload: snap}:
	default:
	}
}

func (f *fakeWork) ListWork(_ context.Context, q viewmodel.WorkQuery) (viewmodel.WorkPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return f.page, f.pageErr
}

func (f *fakeWork) InspectWork(_ context.Context, target viewmodel.WorkTarget, raw bool) (viewmodel.WorkDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspect = append(f.inspect, fakeInspect{target: target, raw: raw})
	return f.detail, nil
}

func (f *fakeWork) StopWork(_ context.Context, target viewmodel.WorkTarget, reason string) (viewmodel.WorkStopResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, fakeStop{target: target, reason: reason})
	return f.stop, f.stopErr
}

func (f *fakeWork) AcknowledgeWork(context.Context, viewmodel.WorkAck) error { return nil }

// setPageErr replaces the error the next ListWork returns.
func (f *fakeWork) setPageErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageErr = err
}

// subscriptionCount is how many times the adapter subscribed.
func (f *fakeWork) subscriptionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs
}

// lastQuery is the WorkQuery the adapter last passed to ListWork.
func (f *fakeWork) lastQuery(t *testing.T) viewmodel.WorkQuery {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		t.Fatalf("ListWork was not called")
	}
	return f.queries[len(f.queries)-1]
}

// lastInspect is the target and raw flag the adapter last passed to
// InspectWork.
func (f *fakeWork) lastInspect(t *testing.T) fakeInspect {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inspect) == 0 {
		t.Fatalf("InspectWork was not called")
	}
	return f.inspect[len(f.inspect)-1]
}

// lastStop is the target and reason the adapter last passed to StopWork.
func (f *fakeWork) lastStop(t *testing.T) fakeStop {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stops) == 0 {
		t.Fatalf("StopWork was not called")
	}
	return f.stops[len(f.stops)-1]
}

// closedCode returns the acpwire error code err carries, or "" when it is not
// an *acpwire.Error.
func closedCode(err error) acpwire.ErrorCode {
	var werr *acpwire.Error
	if !errors.As(err, &werr) {
		return ""
	}
	return werr.Code
}

// steerTurn opens an eligible turn on state with a no-op interrupt handler and
// returns its turn ID.
func steerTurn(t *testing.T, state *session.State) string {
	t.Helper()
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{}, nil
	})
	turn, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	return turn
}

// waitStatus waits until a status read satisfies ok, and fails at the 5s
// deadline: over the wire the status also arrives as an update, so a read may
// need a forwarder's pass.
func waitStatus(t *testing.T, sess seam, what string, ok func(acpwire.SessionStatus) bool) acpwire.SessionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := sess.Status()
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the status never showed %s: it reads %+v", what, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitWork waits until a work read reports revision, and fails at the 5s
// deadline: over the wire a snapshot also arrives as an update.
func waitWork(t *testing.T, sess seam, revision uint64) acpwire.WorkSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, ok := sess.Work()
		if ok && got.Revision == revision {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("Work() = revision %d/%v, want revision %d", got.Revision, ok, revision)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestControlConformance runs every control case against both arms of the
// seam: the service itself, and the client's mirror of it over a pipe. The
// same answers from both is V1.
func TestControlConformance(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, open func(t *testing.T, d service.Deps) seam)
	}{
		{name: "a prompt runs the runner", run: promptRunsTheRunner},
		{name: "a prompt without a runner is refused", run: promptWithoutARunnerIsRefused},
		{name: "a second run is busy", run: aSecondRunIsBusy},
		{name: "an ending session refuses the prompt", run: anEndingSessionRefusesThePrompt},
		{name: "stop stops the group before the run", run: stopStopsTheGroupBeforeTheRun},
		{name: "a stopped run that started keeps its text", run: aStoppedRunThatStartedKeepsItsText},
		{name: "a review runs on the run path", run: reviewRunsOnTheRunPath},
		{name: "an agent run runs on the run path", run: runAgentRunsOnTheRunPath},
		{name: "a stopped review is not an error", run: aStoppedReviewIsNotAnError},
		{name: "suggest and verify proposal forward", run: suggestAndVerifyProposalForward},
		{name: "a prompt writes the watch resume goal", run: promptWritesTheWatchResumeGoal},
		{name: "a prompt codes a provider error", run: promptCodesAProviderError},
		{name: "a prompt passes other errors through", run: promptPassesOtherErrorsThrough},

		{name: "steer queues and pop takes the oldest", run: steerQueuesAndPopTakesTheOldest},
		{name: "clear steering empties the queue", run: clearSteeringEmptiesTheQueue},
		{name: "set interaction passes the patch", run: setInteractionPassesThePatch},
		{name: "set interaction returns the setters error", run: setInteractionReturnsTheSettersError},
		{name: "set effort reports when it applies", run: setEffortReportsWhenItApplies},
		{name: "set sudo unlock reaches the status", run: setSudoUnlockReachesStatus},
		{name: "cancel subagent stops only a running child", run: cancelSubagentStopsOnlyARunningChild},
		{name: "clear finished run clears progress and events", run: clearFinishedRunClearsProgressAndEvents},
		{name: "take watch resume takes the latch once", run: takeWatchResumeTakesTheLatchOnce},

		{name: "send now records the receipt before returning", run: sendNowRecordsTheReceiptBeforeReturning},
		{name: "send now returns the states refusal", run: sendNowReturnsTheStatesRefusal},

		{name: "work reads the seed", run: workReadsTheSeed},
		{name: "work without a service reports none", run: workWithoutAServiceReportsNone},
		{name: "work keeps a newer snapshot and signals", run: workKeepsANewerSnapshotAndSignals},
		{name: "list work names the sessions conversation", run: listWorkNamesTheSessionsConversation},
		{name: "a stale cursor is coded", run: aStaleCursorIsCoded},
		{name: "inspect and stop name their target", run: inspectAndStopNameTheirTarget},
	}
	for _, arm := range seamArms() {
		for _, tc := range cases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}
}

// promptRunsTheRunner pins the happy path: one prompt runs one runner turn
// with the user's text and ends the turn.
func promptRunsTheRunner(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fake := newFakeRunner()
	sess := open(t, service.Deps{State: seamState(t, "s1"), Runner: func() service.Runner { return fake }})

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

// promptWithoutARunnerIsRefused pins the no-runner refusal: no runner func and
// a runner func that returns nil both mean the provider is not built, so no
// turn starts, AgentReady is false, and a queued text goes back on the queue
// while an unqueued one does not.
func promptWithoutARunnerIsRefused(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	cases := []struct {
		name   string
		runner func() service.Runner
	}{
		{name: "no runner func", runner: nil},
		{name: "runner func returns nil", runner: func() service.Runner { return nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state := seamState(t, "s1")
			sess := open(t, service.Deps{State: state, Runner: tc.runner})

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

// aSecondRunIsBusy pins one run at a time: while a prompt is in flight a
// second prompt and a review are refused with CodeBusy, and the refused
// prompt's text goes back on the queue.
func aSecondRunIsBusy(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	fake := newFakeRunner()
	release := make(chan struct{})
	fake.setBody(func(ctx context.Context) error {
		<-release
		return nil
	})
	sess := open(t, service.Deps{
		State:  state,
		Runner: func() service.Runner { return fake },
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

// anEndingSessionRefusesThePrompt pins the quiescing refusal: the gate is
// asked before anything starts, so the runner is never called, no stop group
// is left behind, and the text goes back on the queue.
func anEndingSessionRefusesThePrompt(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	fake := newFakeRunner()
	sess := open(t, service.Deps{State: state, Runner: func() service.Runner { return fake }})

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

// stopStopsTheGroupBeforeTheRun pins the ordering Stop owes the command
// service: the attached commands are stopped first, and only then is the run's
// context cancelled. The run reports the cancellation itself, so the stop came
// from the turn, not from the runner.
func stopStopsTheGroupBeforeTheRun(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
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

// aStoppedRunThatStartedKeepsItsText pins that once the run has begun, a stop
// consumes the text, so a queued text is not re-run.
func aStoppedRunThatStartedKeepsItsText(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	fake := newFakeRunner()
	fake.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	sess := open(t, service.Deps{State: state, Runner: func() service.Runner { return fake }})

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

// reviewRunsOnTheRunPath pins that a review takes the same path a prompt does
// — the stop group is registered while it runs and the params reach the
// runtime unchanged — and that a review naming both a range and a base is
// refused before anything starts. The refusal is asserted by its text: the
// service's sentinel is its own, and an uncoded error crosses the seam with
// its message.
func reviewRunsOnTheRunPath(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	var calls []acpwire.ReviewParams
	var registered bool
	sess := open(t, service.Deps{
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

	// The refusal is unclassified, and the wire answers an unclassified error
	// with its own opaque text, so the two arms agree on the refusal itself:
	// the call fails, and it is not one of the agent's coded refusals.
	if err := sess.Review(ctx, acpwire.ReviewParams{Range: "a..b", Base: "main"}); err == nil || closedCode(err) != "" {
		t.Errorf("Review with a range and a base = %v, want an unclassified refusal", err)
	}
	if len(calls) != 1 {
		t.Errorf("review calls = %+v, want the refused review to call nothing", calls)
	}

	bare := open(t, service.Deps{State: seamState(t, "s2")})
	if err := bare.Review(ctx, acpwire.ReviewParams{}); closedCode(err) != acpwire.CodeNoRunner {
		t.Errorf("Review without a runtime review = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// runAgentRunsOnTheRunPath pins the same path for a custom agent, and that an
// agent run with no name is refused before anything starts.
func runAgentRunsOnTheRunPath(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	var calls []acpwire.AgentRunParams
	var registered bool
	sess := open(t, service.Deps{
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

// aStoppedReviewIsNotAnError pins that a reviewer that returns its own context
// error once stopped still leaves Review with no error: the stop reported the
// cancellation.
func aStoppedReviewIsNotAnError(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	started := make(chan struct{})
	sess := open(t, service.Deps{
		State: seamState(t, "s1"),
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

// suggestAndVerifyProposalForward pins that the two one-shot calls forward to
// the runtime and fail when the runtime supplied neither.
func suggestAndVerifyProposalForward(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	want := acpwire.VerifyCommands{Build: "go build ./...", Test: "go test ./..."}
	sess := open(t, service.Deps{
		State: seamState(t, "s1"),
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

	bare := open(t, service.Deps{State: seamState(t, "s2")})
	if _, err := bare.Suggest(ctx, "hi"); err == nil {
		t.Error("Suggest without a runtime suggestion = nil error, want one")
	}
	if _, err := bare.VerifyProposal(ctx); err == nil {
		t.Error("VerifyProposal without a runtime proposal = nil error, want one")
	}
}

// promptWritesTheWatchResumeGoal pins that a resume turn's text comes from the
// watch and the session's own last assistant reply, not from params.Text.
func promptWritesTheWatchResumeGoal(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fake := newFakeRunner()
	state := seamState(t, "s1")
	state.AddMessageFinal(session.RoleAssistant, "I fixed the build.", session.ContentTypePlain)
	sess := open(t, service.Deps{State: state, Runner: func() service.Runner { return fake }})

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

// promptCodesAProviderError pins decision 4: a provider failure leaves the run
// coded, with the banner its caller shows carried in the error's data.
func promptCodesAProviderError(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fake := newFakeRunner()
	fake.err = &provider.ProviderError{Provider: "openai", StatusCode: 500, Body: "boom"}
	sess := open(t, service.Deps{State: seamState(t, "s1"), Runner: func() service.Runner { return fake }})

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

// promptPassesOtherErrorsThrough pins the other half of decision 4: an error
// the agent did not classify reaches the caller as itself, not reframed as one
// of its coded refusals. The wire answers an unclassified error with its own
// opaque text (F-SEC-37), so what both arms agree on is that the run's failure
// is neither swallowed nor coded.
func promptPassesOtherErrorsThrough(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	fake := newFakeRunner()
	fake.err = errors.New("boom")
	sess := open(t, service.Deps{State: seamState(t, "s1"), Runner: func() service.Runner { return fake }})

	_, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
	if err == nil {
		t.Fatal("Prompt error = nil, want the run's failure")
	}
	if code := closedCode(err); code != "" {
		t.Errorf("err = %v carries code %q, want an unclassified error", err, code)
	}
}

// steerQueuesAndPopTakesTheOldest pins the queue's order: text queued first
// comes back first, and an empty queue reports false.
func steerQueuesAndPopTakesTheOldest(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	if err := sess.Steer(ctx, "a"); err != nil {
		t.Fatalf("Steer(a): %v", err)
	}
	if err := sess.Steer(ctx, "b"); err != nil {
		t.Fatalf("Steer(b): %v", err)
	}
	queue := state.SteeringQueue()
	if len(queue) != 2 || queue[0] != "a" || queue[1] != "b" {
		t.Fatalf("SteeringQueue() = %v, want [a b]", queue)
	}

	for _, want := range []string{"a", "b"} {
		got, ok := sess.PopSteering(ctx)
		if !ok {
			t.Fatalf("PopSteering() = false, want %q", want)
		}
		if got != want {
			t.Fatalf("PopSteering() = %q, want %q", got, want)
		}
	}
	if got, ok := sess.PopSteering(ctx); ok {
		t.Fatalf("PopSteering() on an empty queue = %q, true, want false", got)
	}
}

// clearSteeringEmptiesTheQueue pins that clearing drops every queued text.
func clearSteeringEmptiesTheQueue(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	if err := sess.Steer(ctx, "a"); err != nil {
		t.Fatalf("Steer(a): %v", err)
	}
	if err := sess.Steer(ctx, "b"); err != nil {
		t.Fatalf("Steer(b): %v", err)
	}
	if err := sess.ClearSteering(ctx); err != nil {
		t.Fatalf("ClearSteering: %v", err)
	}
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("SteeringQueue() = %v, want empty", got)
	}
}

// setInteractionPassesThePatch pins that the patch's present fields reach the
// setter and an omitted one stays unknown, and that the committed state comes
// back.
func setInteractionPassesThePatch(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	var recorded interaction.Patch
	sess := open(t, service.Deps{
		State: state,
		SetInteraction: func(p interaction.Patch) (interaction.State, error) {
			recorded = p
			return interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 5}, nil
		},
	})

	mode := "act"
	system := true
	got, err := sess.SetInteraction(ctx, acpwire.InteractionPatch{Mode: &mode, System: &system})
	if err != nil {
		t.Fatalf("SetInteraction: %v", err)
	}

	if recorded.Mode == nil || *recorded.Mode != interaction.ModeAct {
		t.Errorf("recorded mode = %v, want act", recorded.Mode)
	}
	if recorded.System == nil || !*recorded.System {
		t.Errorf("recorded system = %v, want true", recorded.System)
	}
	if recorded.Auto != nil {
		t.Errorf("recorded auto = %v, want nil", *recorded.Auto)
	}

	want := acpwire.Interaction{Mode: "act", Auto: true, System: true, Revision: 5}
	if got != want {
		t.Errorf("SetInteraction() = %+v, want %+v", got, want)
	}
}

// setInteractionReturnsTheSettersError pins that a setter's refusal reaches
// the caller as an error, with no agent code attached, since the service does
// not classify it. The wire answers an unclassified error with its own opaque
// text (F-SEC-37), so the text stays the setter's only in process.
func setInteractionReturnsTheSettersError(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	refused := errors.New("interaction refused")
	sess := open(t, service.Deps{
		State: state,
		SetInteraction: func(interaction.Patch) (interaction.State, error) {
			return interaction.State{}, refused
		},
	})

	mode := "act"
	_, err := sess.SetInteraction(ctx, acpwire.InteractionPatch{Mode: &mode})
	if err == nil {
		t.Fatalf("SetInteraction error = nil, want %v", refused)
	}
	if code := closedCode(err); code != "" {
		t.Errorf("err = %v carries code %q, want the unclassified setter error", err, code)
	}
}

// setEffortReportsWhenItApplies pins that the level reaches the setter and the
// setter's answer, which says when the change applies, comes back.
func setEffortReportsWhenItApplies(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	var recorded string
	sess := open(t, service.Deps{
		State: state,
		SetEffort: func(level string) (string, error) {
			recorded = level
			return session.EffortAppliesNextStep, nil
		},
	})

	applies, err := sess.SetEffort(ctx, "low")
	if err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	if recorded != "low" {
		t.Errorf("recorded level = %q, want low", recorded)
	}
	if applies != acpwire.EffortAppliesNextStep {
		t.Errorf("SetEffort() = %q, want %q", applies, acpwire.EffortAppliesNextStep)
	}
}

// setSudoUnlockReachesTheStatus pins the switch through the status the TUI
// reads.
func setSudoUnlockReachesStatus(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	if err := sess.SetSudoUnlock(ctx, true); err != nil {
		t.Fatalf("SetSudoUnlock(true): %v", err)
	}
	waitStatus(t, sess, "sudo unlock on", func(s acpwire.SessionStatus) bool { return s.SudoUnlock })
	if err := sess.SetSudoUnlock(ctx, false); err != nil {
		t.Fatalf("SetSudoUnlock(false): %v", err)
	}
	waitStatus(t, sess, "sudo unlock off", func(s acpwire.SessionStatus) bool { return !s.SudoUnlock })
}

// cancelSubagentStopsOnlyARunningChild pins the cancel by ID: a running child
// with a cancel function is stopped, and an unknown ID reports false.
func cancelSubagentStopsOnlyARunningChild(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	child := seamState(t, "child")
	view := state.RegisterSubagent("task 1", child)
	ran := false
	state.SetSubagentCancel(view.ID, func() { ran = true })

	sess := open(t, service.Deps{State: state})
	if !sess.CancelSubagent(ctx, view.ID) {
		t.Fatal("CancelSubagent reported false for a running child with a cancel function")
	}
	if !ran {
		t.Error("cancel function did not run")
	}
	if sess.CancelSubagent(ctx, 999) {
		t.Error("CancelSubagent reported true for an unknown ID")
	}
}

// clearFinishedRunClearsProgressAndEvents pins that a finished run's progress
// and event log are both cleared.
func clearFinishedRunClearsProgressAndEvents(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	state.SetSDDProgress(session.SDDProgress{PlanName: "p"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventTaskDone})

	sess := open(t, service.Deps{State: state})
	if err := sess.ClearFinishedRun(ctx); err != nil {
		t.Fatalf("ClearFinishedRun: %v", err)
	}
	if got := state.SDDProgress().PlanName; got != "" {
		t.Errorf("SDDProgress().PlanName = %q, want empty", got)
	}
	if got := state.RunEvents(); len(got) != 0 {
		t.Errorf("RunEvents() = %v, want empty", got)
	}
}

// takeWatchResumeTakesTheLatchOnce pins that the latch is read once: a second
// take reports false.
func takeWatchResumeTakesTheLatchOnce(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()
	state := seamState(t, "s1")
	state.SetWatchResume("ci", true)

	sess := open(t, service.Deps{State: state})
	name, repeat, ok := sess.TakeWatchResume(ctx)
	if !ok || name != "ci" || !repeat {
		t.Fatalf("TakeWatchResume() = %q, %v, %v, want ci, true, true", name, repeat, ok)
	}
	if _, _, ok := sess.TakeWatchResume(ctx); ok {
		t.Error("TakeWatchResume() took the latch twice")
	}
}

// sendNowRecordsTheReceiptBeforeReturning pins that an accepted Send now
// request is in Status by the time SendNow returns, queue and signal included.
func sendNowRecordsTheReceiptBeforeReturning(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})
	turn := steerTurn(t, state)

	got, err := sess.SendNow(context.Background(), acpwire.SendNowRequest{
		SessionID: "s1",
		TurnID:    turn,
		RequestID: "r1",
		Text:      "steer",
	})
	if err != nil {
		t.Fatalf("SendNow = %v, want nil", err)
	}
	if got.Status != acpwire.SteeringAccepted {
		t.Errorf("receipt status = %q, want %q", got.Status, acpwire.SteeringAccepted)
	}
	if got.RequestID != "r1" {
		t.Errorf("receipt request ID = %q, want r1", got.RequestID)
	}

	status := sess.Status()
	if status.Steering.Receipt == nil {
		t.Fatalf("Steering.Receipt = nil, want the accepted receipt")
	}
	if got := status.Steering.Receipt.RequestID; got != "r1" {
		t.Errorf("Steering.Receipt.RequestID = %q, want r1", got)
	}
	if want := []string{"steer"}; !reflect.DeepEqual(status.Steering.Queue, want) {
		t.Errorf("Steering.Queue = %v, want %v", status.Steering.Queue, want)
	}
}

// sendNowReturnsTheStatesRefusal pins that a refusal comes back as the state's
// own error, with nothing recorded.
func sendNowReturnsTheStatesRefusal(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	if _, err := sess.SendNow(context.Background(), acpwire.SendNowRequest{
		SessionID: "s1",
		TurnID:    "t1",
		RequestID: "r1",
		Text:      "steer",
	}); err == nil {
		t.Fatalf("SendNow = nil, want the state's refusal with no steering turn open")
	}
	if got := sess.Status().Steering.Receipt; got != nil {
		t.Fatalf("Steering.Receipt = %+v, want nil after a refusal", got)
	}
}

// workReadsTheSeed pins the snapshot the session keeps when it subscribes:
// the service's current one, already on the wire.
func workReadsTheSeed(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	f := &fakeWork{snap: viewmodel.WorkSnapshot{
		ConversationID: "s1",
		Revision:       1,
		Items: []viewmodel.WorkItem{{
			TaskKey: "work:1",
			State:   "running",
			Label:   "go test ./...",
		}},
	}}
	sess := open(t, service.Deps{State: seamState(t, "s1"), Work: f})

	got, ok := sess.Work()
	if !ok {
		t.Fatalf("Work reports no work, want the seed")
	}
	if want := viewmodel.WorkSnapshotOf(f.WorkSnapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Work =\n%+v\nwant\n%+v", got, want)
	}
	if n := f.subscriptionCount(); n != 1 {
		t.Errorf("subscriptions = %d, want 1", n)
	}
}

// workWithoutAServiceReportsNone pins that a session whose runtime supplied no
// work service reports no work, and fails every work call.
func workWithoutAServiceReportsNone(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	sess := open(t, service.Deps{State: seamState(t, "s1")})

	got, ok := sess.Work()
	if ok {
		t.Errorf("Work reports work %+v, want none", got)
	}
	if !reflect.DeepEqual(got, acpwire.WorkSnapshot{}) {
		t.Errorf("Work snapshot = %+v, want the zero snapshot", got)
	}

	ctx := context.Background()
	if _, err := sess.ListWork(ctx, acpwire.WorkQuery{View: "history"}); err == nil {
		t.Errorf("ListWork err = nil, want a failure")
	}
	if _, err := sess.InspectWork(ctx, "work:1", false); err == nil {
		t.Errorf("InspectWork err = nil, want a failure")
	}
	if _, err := sess.StopWork(ctx, "work:1", "requested"); err == nil {
		t.Errorf("StopWork err = nil, want a failure")
	}
}

// workKeepsANewerSnapshotAndSignals pins that a published snapshot replaces
// the kept one and reaches the reader.
func workKeepsANewerSnapshotAndSignals(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	f := &fakeWork{snap: viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 1}}
	sess := open(t, service.Deps{State: seamState(t, "s1"), Work: f})

	// The first read is what creates the client's mirror, which the update
	// that follows refreshes; on the service arm it reads the kept snapshot.
	if _, ok := sess.Work(); !ok {
		t.Fatalf("Work reports no work, want the seed")
	}

	f.publish(viewmodel.WorkSnapshot{ConversationID: "s1", Revision: 2})
	if got := waitWork(t, sess, 2); got.Revision != 2 {
		t.Fatalf("Work revision = %d, want 2", got.Revision)
	}
}

// listWorkNamesTheSessionsConversation pins that a page query carries the
// session's own conversation and the caller's view, query, cursor and limit.
func listWorkNamesTheSessionsConversation(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	f := &fakeWork{page: viewmodel.WorkPage{
		Revision: 2,
		Items:    []viewmodel.WorkItem{{TaskKey: "work:1", State: "completed"}},
	}}
	sess := open(t, service.Deps{State: seamState(t, "s1"), Work: f})

	page, err := sess.ListWork(context.Background(), acpwire.WorkQuery{
		View:   "history",
		Query:  "go",
		Cursor: "c",
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("ListWork = %v, want nil", err)
	}
	wantQuery := viewmodel.WorkQuery{
		ConversationID: "s1",
		View:           "history",
		Query:          "go",
		Cursor:         "c",
		Limit:          50,
	}
	if got := f.lastQuery(t); got != wantQuery {
		t.Errorf("ListWork query = %+v, want %+v", got, wantQuery)
	}
	if want := viewmodel.WorkPageOf(f.page); !reflect.DeepEqual(page, want) {
		t.Errorf("ListWork page =\n%+v\nwant\n%+v", page, want)
	}
}

// aStaleCursorIsCoded pins that a stale work cursor crosses the seam as
// CodeStaleWorkCursor, and that any other failure reaches the caller as
// itself: not swallowed, and not reframed as the stale-cursor code. The wire
// answers an unclassified error with its own opaque text (F-SEC-37), so the
// service's message stays its own only in process.
func aStaleCursorIsCoded(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	f := &fakeWork{pageErr: viewmodel.ErrWorkStaleCursor}
	sess := open(t, service.Deps{State: seamState(t, "s1"), Work: f})

	ctx := context.Background()
	if _, err := sess.ListWork(ctx, acpwire.WorkQuery{View: "history"}); !acpclient.IsCode(err, acpwire.CodeStaleWorkCursor) {
		t.Fatalf("stale cursor err = %v, want CodeStaleWorkCursor", err)
	}

	boom := errors.New("boom")
	f.setPageErr(boom)
	_, err := sess.ListWork(ctx, acpwire.WorkQuery{View: "history"})
	if err == nil {
		t.Fatalf("other err = nil, want the service's failure")
	}
	if got, ok := acpclient.CodeOf(err); ok {
		t.Fatalf("other err = %v carries code %q, want none", err, got)
	}
}

// inspectAndStopNameTheirTarget pins that both target calls name the session's
// conversation and the caller's task key.
func inspectAndStopNameTheirTarget(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	f := &fakeWork{
		detail: viewmodel.WorkDetail{Output: "the output", Raw: true, Report: "the report"},
	}
	sess := open(t, service.Deps{State: seamState(t, "s1"), Work: f})
	ctx := context.Background()

	detail, err := sess.InspectWork(ctx, "work:1", true)
	if err != nil {
		t.Fatalf("InspectWork = %v, want nil", err)
	}
	wantInspect := fakeInspect{target: viewmodel.WorkTarget{ConversationID: "s1", TaskKey: "work:1"}, raw: true}
	if got := f.lastInspect(t); got != wantInspect {
		t.Errorf("InspectWork call = %+v, want %+v", got, wantInspect)
	}
	if want := viewmodel.WorkDetailOf(f.detail); !reflect.DeepEqual(detail, want) {
		t.Errorf("InspectWork detail =\n%+v\nwant\n%+v", detail, want)
	}

	if _, err := sess.StopWork(ctx, "work:1", "requested"); err != nil {
		t.Fatalf("StopWork = %v, want nil", err)
	}
	wantStop := fakeStop{target: viewmodel.WorkTarget{ConversationID: "s1", TaskKey: "work:1"}, reason: "requested"}
	if got := f.lastStop(t); got != wantStop {
		t.Errorf("StopWork call = %+v, want %+v", got, wantStop)
	}
}

// TestControlCallBudget holds R10's 1ms budget for a control call over the
// pipe on the 2,000-call session: the call includes its barrier and the
// session_status update that carries the change (P3 plan 6b).
func TestControlCallBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	sess := openPipeSeamTB(t, service.Deps{State: st})

	const runs = 30
	ctx := context.Background()
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if err := sess.SetSudoUnlock(ctx, i%2 == 0); err != nil {
			t.Fatalf("SetSudoUnlock: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	t.Logf("control call: median %v over %d runs (budget 1ms)", median, runs)
	if median >= time.Millisecond {
		t.Fatalf("control call median = %v, budget 1ms (R10): do not loosen it", median)
	}
}
