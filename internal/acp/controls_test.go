package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/pubsub"
	"marshal/internal/watch"
)

// controlSession is the session every control test serves.
const controlSession = "s1"

// controlRunner is the service package's fakeRunner, copied here so these
// handler tests drive a turn without that package's tests. It records each
// goal it gets, signals each start, and runs whatever body the test set: a body
// that blocks is how a test holds a turn open.
type controlRunner struct {
	mu      sync.Mutex
	goals   []string
	started chan struct{}
	body    func(ctx context.Context) error
}

// newControlRunner builds a runner that returns nil and signals each start.
func newControlRunner() *controlRunner {
	return &controlRunner{started: make(chan struct{}, 8)}
}

func (r *controlRunner) Run(ctx context.Context, goal string) error {
	r.mu.Lock()
	r.goals = append(r.goals, goal)
	body := r.body
	r.mu.Unlock()

	select {
	case r.started <- struct{}{}:
	default:
	}
	if body != nil {
		return body(ctx)
	}
	return nil
}

// setBody replaces what Run does after it records the goal.
func (r *controlRunner) setBody(body func(ctx context.Context) error) {
	r.mu.Lock()
	r.body = body
	r.mu.Unlock()
}

// goalsSeen returns the goals Run was given, in order.
func (r *controlRunner) goalsSeen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.goals...)
}

// waitStart blocks until Run has started, or fails the test.
func (r *controlRunner) waitStart(t *testing.T) {
	t.Helper()
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the control runner never started")
	}
}

// controlFrame is one session/update notification the client received.
type controlFrame struct {
	SessionID string
	Update    json.RawMessage
}

// controlAgent is the agent end of a control test: the service over one state,
// the connection's control and status managers, and the session/update frames
// the client recorded.
type controlAgent struct {
	controls *ControlManager
	statuses *StatusManager
	state    *session.State

	// lookup is what the managers' Lookup reports for the session.
	lookup atomic.Bool

	mu     sync.Mutex
	frames []controlFrame
}

// record keeps one received session/update frame.
func (a *controlAgent) record(sessionID string, update json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.frames = append(a.frames, controlFrame{SessionID: sessionID, Update: update})
}

// recorded returns the frames received so far, in arrival order.
func (a *controlAgent) recorded() []controlFrame {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]controlFrame, len(a.frames))
	copy(out, a.frames)
	return out
}

// hasKind reports whether a frame of that session/update kind was recorded.
func (a *controlAgent) hasKind(kind string) bool {
	for _, f := range a.recorded() {
		var peek struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(f.Update, &peek); err != nil {
			continue
		}
		if peek.Kind == kind {
			return true
		}
	}
	return false
}

// notifyClient decodes a notification the agent sent, keeping each
// session/update frame.
func (a *controlAgent) notifyClient(method string, params json.RawMessage) {
	if method != acpwire.MethodSessionUpdate {
		return
	}
	var env struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &env); err != nil {
		return
	}
	if len(env.Update) == 0 {
		return
	}
	a.record(env.SessionID, env.Update)
}

// controlSetup is what one control test serves.
type controlSetup struct {
	// marshal says whether the client declares the marshal capability.
	marshal bool
	deps    service.Deps
	// others is what registerControls routes a client without the capability to.
	others map[string]Handler
	// broker, when non-nil, also serves the transcript handlers over it, so a
	// test can publish the events the idle stack loop drains (P3 plan 6b
	// decision 4).
	broker *pubsub.Broker[session.Event]
}

// startControlAgent serves the control handlers and the status handlers on the
// agent end of a pipe and returns a client connection that has completed
// initialize.
func startControlAgent(t *testing.T, marshal bool, deps service.Deps, others map[string]Handler) (*rpc.Conn, *controlAgent) {
	t.Helper()
	return startControlAgentWith(t, controlSetup{marshal: marshal, deps: deps, others: others})
}

// startControlAgentWith is startControlAgent over a full setup.
func startControlAgentWith(t *testing.T, setup controlSetup) (*rpc.Conn, *controlAgent) {
	t.Helper()

	_, served := service.New(setup.deps)
	a := &controlAgent{state: setup.deps.State}
	a.lookup.Store(true)

	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes
	registerInitialize(srv)

	notify := srv.Notify
	lookup := func(id string) (*service.Session, bool) {
		if id != controlSession || !a.lookup.Load() {
			return nil, false
		}
		return served, true
	}

	// A setup that serves the transcript gets a runtime over its own broker, so
	// a test's runner can publish the events the idle stack loop drains.
	var turns *TurnManager
	if setup.broker != nil {
		runtime := func(id string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: controlSession,
				State:     setup.deps.State,
				Events:    setup.broker,
			}, id == controlSession
		}
		turns = NewTurnManager(TurnManagerConfig{
			Lookup:  runtime,
			Reads:   runtime,
			Notify:  notify,
			Marshal: srv.marshalClient,
		})
		sheets := NewSheetManager(SheetManagerConfig{
			Lookup:  lookup,
			Notify:  notify,
			Marshal: srv.marshalClient,
		})
		t.Cleanup(sheets.Close)
		registerTranscript(srv, turns, sheets, lookup)
	}

	a.statuses = NewStatusManager(StatusManagerConfig{
		Lookup:  lookup,
		Notify:  notify,
		Marshal: srv.marshalClient,
	})
	registerStatus(srv, a.statuses)
	a.controls = NewControlManager(ControlManagerConfig{
		Lookup:  lookup,
		Notify:  notify,
		Marshal: srv.marshalClient,
	})
	registerControls(srv, a.controls, setup.others)
	srv.SetResponseBarrier(responseBarrier(turns, a.statuses, a.controls))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()

	conn := rpc.NewConn(client, rpc.Handlers{Notify: a.notifyClient})

	t.Cleanup(func() {
		_ = conn.Close()
		a.controls.Close()
		a.statuses.Close()
		cancel()
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	p := acpwire.InitializeParams{ProtocolVersion: acpwire.MarshalProtocolVersion}
	if setup.marshal {
		p.ClientCapabilities = &acpwire.ClientCapabilities{
			Marshal: &acpwire.MarshalCapability{ProtocolVersion: acpwire.MarshalProtocolVersion},
		}
	}
	var res acpwire.InitializeResult
	if err := conn.Call(ctx, "initialize", p, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return conn, a
}

// controlReply is one call's answer, made from a goroutine.
type controlReply struct {
	res acpwire.PromptResult
	err error
}

// promptInBackground starts a session/prompt on its own goroutine, so the call
// stays in flight while the test acts.
func promptInBackground(conn *rpc.Conn, text string) <-chan controlReply {
	done := make(chan controlReply, 1)
	go func() {
		var res acpwire.PromptResult
		err := conn.Call(context.Background(), acpwire.MethodPrompt, acpwire.PromptCallParams{
			SessionID: controlSession,
			Prompt:    []acpwire.TextBlock{{Type: "text", Text: text}},
		}, &res)
		done <- controlReply{res: res, err: err}
	}()
	return done
}

// waitForFrame polls cond until it reports true or timeout expires.
func waitForFrame(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// TestAMarshalPromptRunsOnTheService is the plan's proof of decision 1: a
// marshal client's prompt runs on the service, not on ACP's turn slot.
func TestAMarshalPromptRunsOnTheService(t *testing.T) {
	state := seamState(t, controlSession)
	runner := newControlRunner()
	release := make(chan struct{})
	runner.setBody(func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	// The other arm is a real TurnManager over its own runner: if the marshal
	// path reached it, it would take its own slot and run its own runner.
	otherRunner := newControlRunner()
	turns := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: id,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, goal string) error {
					return otherRunner.Run(ctx, goal)
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, id == controlSession
		},
		Notify: func(string, any) error { return nil },
	})
	conn, _ := startControlAgent(t, true,
		service.Deps{State: state, Runner: func() service.Runner { return runner }},
		map[string]Handler{acpwire.MethodPrompt: turns.PromptTurn})

	done := promptInBackground(conn, "hello")
	runner.waitStart(t)

	if got, want := runner.goalsSeen(), []string{"hello"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the service's runner saw %v, want %v", got, want)
	}
	if turns.HasActiveTurn(controlSession) {
		t.Fatal("the turn handler reserved a slot for a marshal client")
	}
	if got := otherRunner.goalsSeen(); len(got) != 0 {
		t.Fatalf("the other handler's runner ran %v, want it never to run", got)
	}

	close(release)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("session/prompt: %v", r.err)
		}
		if r.res.StopReason != acpwire.StopReasonEndTurn {
			t.Fatalf("stop reason = %q, want %q", r.res.StopReason, acpwire.StopReasonEndTurn)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt did not return")
	}
}

// TestAPromptWithoutTheCapabilityReachesItsOtherHandler pins R9's
// compatibility arm: the same call from a client without the capability reaches
// today's handler, and the service's runner never starts.
func TestAPromptWithoutTheCapabilityReachesItsOtherHandler(t *testing.T) {
	runner := newControlRunner()
	var calls atomic.Int64
	others := map[string]Handler{
		acpwire.MethodPrompt: func(ctx context.Context, params json.RawMessage) (any, error) {
			calls.Add(1)
			return acpwire.PromptResult{StopReason: acpwire.StopReasonEndTurn}, nil
		},
	}
	conn, _ := startControlAgent(t, false,
		service.Deps{State: seamState(t, controlSession), Runner: func() service.Runner { return runner }},
		others)

	var res acpwire.PromptResult
	if err := conn.Call(context.Background(), acpwire.MethodPrompt, acpwire.PromptCallParams{
		SessionID: controlSession,
		Prompt:    []acpwire.TextBlock{{Type: "text", Text: "hello"}},
	}, &res); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("the other handler ran %d times, want 1", calls.Load())
	}
	if res.StopReason != acpwire.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", res.StopReason, acpwire.StopReasonEndTurn)
	}
	if got := runner.goalsSeen(); len(got) != 0 {
		t.Fatalf("the service's runner ran %v for a client without the capability", got)
	}
}

// TestAQueuedPromptThatCannotStartIsPutBack pins the service's requeue: a
// queued text whose turn cannot start goes back on the steering queue, and the
// code travels to the client.
func TestAQueuedPromptThatCannotStartIsPutBack(t *testing.T) {
	state := seamState(t, controlSession)
	conn, _ := startControlAgent(t, true, service.Deps{State: state}, nil)

	var res acpwire.PromptResult
	err := conn.Call(context.Background(), acpwire.MethodPrompt, acpwire.PromptCallParams{
		SessionID: controlSession,
		Prompt:    []acpwire.TextBlock{{Type: "text", Text: "queued text"}},
		Queued:    true,
	}, &res)
	if err == nil {
		t.Fatal("a prompt with no runner answered no error")
	}
	if !acpclient.IsCode(err, acpwire.CodeNoRunner) {
		t.Fatalf("err = %v, want CodeNoRunner", err)
	}
	queue := state.SteeringQueue()
	if len(queue) != 1 || queue[0] != "queued text" {
		t.Fatalf("steering queue = %v, want the queued text back", queue)
	}
}

// TestStopStopsAServiceTurnWithItsCause pins session/stop: it stops the
// service's run and the prompt answers cancelled.
func TestStopStopsAServiceTurnWithItsCause(t *testing.T) {
	runner := newControlRunner()
	runner.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	conn, _ := startControlAgent(t, true,
		service.Deps{State: seamState(t, controlSession), Runner: func() service.Runner { return runner }},
		nil)

	done := promptInBackground(conn, "hold this turn open")
	runner.waitStart(t)

	var stopRes json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodStop, acpwire.StopParams{
		SessionID: controlSession,
		Cause:     "turn stopped",
	}, &stopRes); err != nil {
		t.Fatalf("session/stop: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("session/prompt after stop: %v", r.err)
		}
		if r.res.StopReason != acpwire.StopReasonCancelled {
			t.Fatalf("stop reason = %q, want %q", r.res.StopReason, acpwire.StopReasonCancelled)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the prompt did not return after session/stop")
	}
}

// TestCancellingThePromptStopsTheRun pins R2's cancellation for the long calls:
// the client's context ending stops the run, as in process.
func TestCancellingThePromptStopsTheRun(t *testing.T) {
	ended := make(chan struct{})
	runner := newControlRunner()
	runner.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		close(ended)
		return ctx.Err()
	})
	conn, _ := startControlAgent(t, true,
		service.Deps{State: seamState(t, controlSession), Runner: func() service.Runner { return runner }},
		nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		var res acpwire.PromptResult
		_ = conn.Call(ctx, acpwire.MethodPrompt, acpwire.PromptCallParams{
			SessionID: controlSession,
			Prompt:    []acpwire.TextBlock{{Type: "text", Text: "hi"}},
		}, &res)
	}()
	runner.waitStart(t)
	cancel()

	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the run's context never ended after the client cancelled the prompt")
	}
}

// TestTheTurnCallsReachTheService pins that the five calls reach the service's
// dependencies and return their results.
func TestTheTurnCallsReachTheService(t *testing.T) {
	var (
		gotReview  acpwire.ReviewParams
		gotRun     acpwire.AgentRunParams
		gotSuggest string
		proposed   int
	)
	deps := service.Deps{
		State: seamState(t, controlSession),
		Review: func(ctx context.Context, p acpwire.ReviewParams) error {
			gotReview = p
			return nil
		},
		RunAgent: func(ctx context.Context, p acpwire.AgentRunParams) error {
			gotRun = p
			return nil
		},
		Suggest: func(ctx context.Context, lastMessage string) (string, error) {
			gotSuggest = lastMessage
			return "a suggestion", nil
		},
		VerifyProposal: func(ctx context.Context) (acpwire.VerifyCommands, error) {
			proposed++
			return acpwire.VerifyCommands{Build: "go build ./...", Test: "go test ./..."}, nil
		},
	}
	conn, _ := startControlAgent(t, true, deps, nil)

	wantReview := acpwire.ReviewParams{Focus: "the diff", Base: "main"}
	var reviewRes json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodReview, acpwire.ReviewCallParams{
		SessionID: controlSession,
		Review:    wantReview,
	}, &reviewRes); err != nil {
		t.Fatalf("session/review: %v", err)
	}
	if !reflect.DeepEqual(gotReview, wantReview) {
		t.Fatalf("review params = %+v, want %+v", gotReview, wantReview)
	}

	wantRun := acpwire.AgentRunParams{Agent: "reviewer", Goal: "look at the diff"}
	var runRes json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodAgentRun, acpwire.AgentRunCallParams{
		SessionID: controlSession,
		Run:       wantRun,
	}, &runRes); err != nil {
		t.Fatalf("session/agent_run: %v", err)
	}
	if !reflect.DeepEqual(gotRun, wantRun) {
		t.Fatalf("agent run params = %+v, want %+v", gotRun, wantRun)
	}

	var suggestRes acpwire.SuggestResult
	if err := conn.Call(context.Background(), acpwire.MethodSuggest, acpwire.SuggestParams{
		SessionID:   controlSession,
		LastMessage: "the last thing I said",
	}, &suggestRes); err != nil {
		t.Fatalf("session/suggest: %v", err)
	}
	if gotSuggest != "the last thing I said" {
		t.Fatalf("suggest lastMessage = %q", gotSuggest)
	}
	if suggestRes.Text != "a suggestion" {
		t.Fatalf("suggest result = %q, want %q", suggestRes.Text, "a suggestion")
	}

	var verifyRes acpwire.VerifyCommands
	if err := conn.Call(context.Background(), acpwire.MethodVerifyProposal, acpwire.ControlParams{
		SessionID: controlSession,
	}, &verifyRes); err != nil {
		t.Fatalf("session/verify_proposal: %v", err)
	}
	if proposed != 1 {
		t.Fatalf("VerifyProposal ran %d times, want 1", proposed)
	}
	if verifyRes.Build != "go build ./..." || verifyRes.Test != "go test ./..." {
		t.Fatalf("verify commands = %+v", verifyRes)
	}
}

// TestTheTurnCallsRefuseBadParams pins the two refusals every turn call shares:
// a missing session id is invalid params, and an unknown one is the
// unknown-session error.
func TestTheTurnCallsRefuseBadParams(t *testing.T) {
	conn, _ := startControlAgent(t, true, service.Deps{State: seamState(t, controlSession)}, nil)

	methods := []struct {
		name   string
		method string
	}{
		{"prompt", acpwire.MethodPrompt},
		{"stop", acpwire.MethodStop},
		{"review", acpwire.MethodReview},
		{"agent_run", acpwire.MethodAgentRun},
		{"suggest", acpwire.MethodSuggest},
		{"verify_proposal", acpwire.MethodVerifyProposal},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var res json.RawMessage
			err := conn.Call(context.Background(), m.method, json.RawMessage(`{}`), &res)
			var rpcErr *rpc.Error
			if err == nil || !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
				t.Fatalf("err = %v, want the invalid-params refusal", err)
			}
			err = conn.Call(context.Background(), m.method, json.RawMessage(`{"sessionId":"nope"}`), &res)
			if err == nil || !strings.Contains(err.Error(), "unknown session") {
				t.Fatalf("err = %v, want the unknown-session refusal", err)
			}
		})
	}
}

// TestAServiceTurnReachesTheStackThroughTheIdleLoop pins decision 4: the idle
// stack loop carries a service turn's transcript, so a marshal client sees the
// turn move without the turn manager holding a slot.
func TestAServiceTurnReachesTheStackThroughTheIdleLoop(t *testing.T) {
	state := seamState(t, controlSession)
	broker := pubsub.NewBroker[session.Event]()
	const said = "hello from the service turn"
	started := make(chan struct{})
	held := make(chan struct{})

	runner := newControlRunner()
	runner.setBody(func(ctx context.Context) error {
		state.AddMessage(session.RoleAssistant, said, session.ContentTypePlain)
		broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
			Role:    session.RoleAssistant,
			Content: said,
		}})
		close(started)
		<-held
		return nil
	})

	conn, a := startControlAgentWith(t, controlSetup{
		marshal: true,
		deps:    service.Deps{State: state, Runner: func() service.Runner { return runner }},
		broker:  broker,
	})

	// The first stack read starts the idle loop (P3 plan 4b).
	var snap acpwire.StackSnapshot
	if err := conn.Call(context.Background(), acpwire.MethodStack, acpwire.StackParams{
		SessionID: controlSession,
	}, &snap); err != nil {
		t.Fatalf("session/stack: %v", err)
	}

	done := promptInBackground(conn, "say something")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner never started")
	}

	// While the prompt is still in flight, the message reaches the client.
	waitForFrame(t, 500*time.Millisecond, "a stack_patch carrying the turn's message", func() bool {
		for _, f := range a.recorded() {
			var peek struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(f.Update, &peek); err != nil {
				continue
			}
			if peek.Kind != acpwire.UpdateStackPatch {
				continue
			}
			if strings.Contains(string(f.Update), said) {
				return true
			}
		}
		return false
	})

	close(held)
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("session/prompt: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt did not return")
	}
}

// TestAMarshalConnectionBindsNoWatchResume pins decision 3: a marshal client
// resumes a watch itself, so the hook starts no turn.
func TestAMarshalConnectionBindsNoWatchResume(t *testing.T) {
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	goals := make(chan string, 4)
	tm := newResumeTestManager(t, state, goals, nil)

	hook := &app.WatchResumeHook{}
	alive := &atomic.Bool{}
	alive.Store(true)
	bindWatchResume(hook, tm, controlSession, alive, func() bool { return true })

	hook.Invoke(watch.Report{Name: "build", Resume: true})
	select {
	case got := <-goals:
		t.Fatalf("a marshal connection's hook started a turn with %q, want none", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestAConnectionWithoutTheCapabilityBindsWatchResume pins the other half: every
// other client keeps the bound hook.
func TestAConnectionWithoutTheCapabilityBindsWatchResume(t *testing.T) {
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	goals := make(chan string, 4)
	tm := newResumeTestManager(t, state, goals, nil)

	hook := &app.WatchResumeHook{}
	alive := &atomic.Bool{}
	alive.Store(true)
	bindWatchResume(hook, tm, controlSession, alive, func() bool { return false })

	hook.Invoke(watch.Report{Name: "build", Resume: true})
	select {
	case <-goals:
	case <-time.After(2 * time.Second):
		t.Fatal("the hook started no turn for a client without the capability")
	}
}

// TestSteerQueuesWithNoTurnInFlight pins the difference from ACP's own handler:
// the service's Steer queues even when no turn runs, and the call answers {}.
func TestSteerQueuesWithNoTurnInFlight(t *testing.T) {
	state := seamState(t, controlSession)
	conn, _ := startControlAgent(t, true, service.Deps{State: state}, nil)

	var res json.RawMessage
	if err := conn.Call(context.Background(), acpwire.MethodSteer, acpwire.SteerCallParams{
		SessionID: controlSession,
		Text:      "queued with no turn",
	}, &res); err != nil {
		t.Fatalf("session/steer: %v", err)
	}
	if got := strings.TrimSpace(string(res)); got != "{}" {
		t.Fatalf("session/steer result = %s, want {}", got)
	}
	queue := state.SteeringQueue()
	if len(queue) != 1 || queue[0] != "queued with no turn" {
		t.Fatalf("steering queue = %v, want the queued text", queue)
	}
}

// TestSendNowAnswersTheReceipt pins the Send now delivery: with a turn the
// state reports active and supported, the call answers the receipt.
func TestSendNowAnswersTheReceipt(t *testing.T) {
	state := seamState(t, controlSession)
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{}, nil
	})
	turn, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	if snap := state.SteeringSnapshot(); !snap.Active || !snap.Supported || snap.TurnID != turn {
		t.Fatalf("steering snapshot = %+v, want the active, supported turn %s", snap, turn)
	}

	conn, _ := startControlAgent(t, true, service.Deps{State: state}, nil)

	var res acpwire.SteeringReceipt
	if err := conn.Call(context.Background(), acpwire.MethodSteer, acpwire.SteerCallParams{
		SessionID: controlSession,
		Text:      "do this instead",
		Delivery:  acpwire.SteerDeliveryNow,
		TurnID:    turn,
		RequestID: "r1",
	}, &res); err != nil {
		t.Fatalf("session/steer delivery=now: %v", err)
	}
	if res.RequestID != "r1" || res.TurnID != turn || res.Status != acpwire.SteeringAccepted {
		t.Fatalf("receipt = %+v, want r1 on turn %s, status %q", res, turn, acpwire.SteeringAccepted)
	}
}

// TestSetModeTakesSystemAccess pins that the handler decodes ACP's own params,
// which name system access "system_access", and answers the committed state.
func TestSetModeTakesSystemAccess(t *testing.T) {
	var recorded interaction.Patch
	conn, _ := startControlAgent(t, true, service.Deps{
		State: seamState(t, controlSession),
		SetInteraction: func(p interaction.Patch) (interaction.State, error) {
			recorded = p
			return interaction.State{Mode: interaction.ModeAct, System: true, Revision: 7}, nil
		},
	}, nil)

	var res acpwire.Interaction
	if err := conn.Call(context.Background(), acpwire.MethodSetMode,
		json.RawMessage(`{"sessionId":"s1","system_access":true}`), &res); err != nil {
		t.Fatalf("session/set_mode: %v", err)
	}
	if recorded.System == nil || !*recorded.System {
		t.Fatalf("the setter saw system %v, want true", recorded.System)
	}
	if recorded.Mode != nil || recorded.Auto != nil {
		t.Fatalf("the setter saw mode %v and auto %v, want both nil", recorded.Mode, recorded.Auto)
	}
	want := acpwire.Interaction{Mode: "act", System: true, Revision: 7}
	if res != want {
		t.Fatalf("interaction = %+v, want %+v", res, want)
	}

	err := conn.Call(context.Background(), acpwire.MethodSetMode,
		json.RawMessage(`{"sessionId":"s1","bogus":true}`), &res)
	var rpcErr *rpc.Error
	if err == nil || !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
		t.Fatalf("err = %v, want the invalid-params refusal for an unknown key", err)
	}
}

// TestSetEffortAnswersWhenItApplies pins both arms: the setter's applies value
// comes back as appliesFrom, and the setter's own error is not reframed.
func TestSetEffortAnswersWhenItApplies(t *testing.T) {
	var got string
	conn, _ := startControlAgent(t, true, service.Deps{
		State: seamState(t, controlSession),
		SetEffort: func(level string) (string, error) {
			got = level
			return session.EffortAppliesNextTurn, nil
		},
	}, nil)

	var res acpwire.EffortResult
	if err := conn.Call(context.Background(), acpwire.MethodSetEffort, acpwire.EffortParams{
		SessionID: controlSession,
		Effort:    "high",
	}, &res); err != nil {
		t.Fatalf("session/set_effort: %v", err)
	}
	if got != "high" {
		t.Fatalf("the setter saw level %q, want high", got)
	}
	if res.Effort != "high" || res.AppliesFrom != session.EffortAppliesNextTurn {
		t.Fatalf("effort result = %+v, want high from %q", res, session.EffortAppliesNextTurn)
	}

	refused := errors.New("effort refused")
	refusing, _ := startControlAgent(t, true, service.Deps{
		State: seamState(t, controlSession),
		SetEffort: func(string) (string, error) {
			return "", refused
		},
	}, nil)
	err := refusing.Call(context.Background(), acpwire.MethodSetEffort, acpwire.EffortParams{
		SessionID: controlSession,
		Effort:    "low",
	}, &res)
	var rpcErr *rpc.Error
	if err == nil || !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want the setter's error to travel", err)
	}
	// The setter's error is not reframed as a bad request: the server carries
	// it as an internal error, and the canonical text replaces its message.
	if rpcErr.Code != internalError {
		t.Fatalf("err = %v (code %d), want the setter's error at code %d, not invalid params",
			err, rpcErr.Code, internalError)
	}
}

// TestTheControlsReachTheService pins that each of the six new control methods
// reaches the service and answers what it returned.
func TestTheControlsReachTheService(t *testing.T) {
	state := seamState(t, controlSession)
	view := state.RegisterSubagent("task 1", seamState(t, "child"))
	cancelled := false
	state.SetSubagentCancel(view.ID, func() { cancelled = true })
	state.SetWatchResume("ci", true)
	state.SetSDDProgress(session.SDDProgress{PlanName: "a plan"})
	state.AddRunEvent(session.RunEvent{Kind: session.RunEventTaskDone})
	state.PushSteering("first")
	state.PushSteering("second")

	deps := service.Deps{State: state}
	conn, _ := startControlAgent(t, true, deps, nil)
	ctx := context.Background()
	params := acpwire.ControlParams{SessionID: controlSession}

	// steering_pop answers the oldest text, then reports an empty queue.
	for _, want := range []string{"first", "second"} {
		var pop acpwire.SteeringPopResult
		if err := conn.Call(ctx, acpwire.MethodSteeringPop, params, &pop); err != nil {
			t.Fatalf("session/steering_pop: %v", err)
		}
		if !pop.OK || pop.Text != want {
			t.Fatalf("pop = %+v, want %q with ok", pop, want)
		}
	}
	var empty acpwire.SteeringPopResult
	if err := conn.Call(ctx, acpwire.MethodSteeringPop, params, &empty); err != nil {
		t.Fatalf("session/steering_pop on an empty queue: %v", err)
	}
	if empty.OK || empty.Text != "" {
		t.Fatalf("pop on an empty queue = %+v, want ok false", empty)
	}

	// steering_clear empties the queue.
	state.PushSteering("drop me")
	var cleared json.RawMessage
	if err := conn.Call(ctx, acpwire.MethodSteeringClear, params, &cleared); err != nil {
		t.Fatalf("session/steering_clear: %v", err)
	}
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("steering queue = %v, want it empty", got)
	}

	// set_sudo_unlock reaches Status().SudoUnlock.
	var unlocked json.RawMessage
	if err := conn.Call(ctx, acpwire.MethodSetSudoUnlock, acpwire.SudoUnlockParams{
		SessionID: controlSession,
		On:        true,
	}, &unlocked); err != nil {
		t.Fatalf("session/set_sudo_unlock: %v", err)
	}
	_, probe := service.New(deps)
	t.Cleanup(func() { _ = probe.Close(context.Background()) })
	if !probe.Status().SudoUnlock {
		t.Fatal("Status().SudoUnlock = false, want true")
	}

	// subagent_cancel stops the running child, and reports false for a
	// subagent that is not running.
	var cancel acpwire.SubagentCancelResult
	if err := conn.Call(ctx, acpwire.MethodSubagentCancel, acpwire.SubagentCancelParams{
		SessionID:  controlSession,
		SubagentID: view.ID,
	}, &cancel); err != nil {
		t.Fatalf("session/subagent_cancel: %v", err)
	}
	if !cancel.Cancelled || !cancelled {
		t.Fatalf("cancel = %+v with cancel ran %v, want both true", cancel, cancelled)
	}
	var unknown acpwire.SubagentCancelResult
	if err := conn.Call(ctx, acpwire.MethodSubagentCancel, acpwire.SubagentCancelParams{
		SessionID:  controlSession,
		SubagentID: 999,
	}, &unknown); err != nil {
		t.Fatalf("session/subagent_cancel for an unknown ID: %v", err)
	}
	if unknown.Cancelled {
		t.Fatalf("cancel for an unknown ID = %+v, want false", unknown)
	}

	// sdd_clear clears the finished run's progress and event log.
	var sddCleared json.RawMessage
	if err := conn.Call(ctx, acpwire.MethodSDDClear, params, &sddCleared); err != nil {
		t.Fatalf("session/sdd_clear: %v", err)
	}
	if got := state.SDDProgress().PlanName; got != "" {
		t.Fatalf("SDDProgress().PlanName = %q, want empty", got)
	}
	if got := state.RunEvents(); len(got) != 0 {
		t.Fatalf("RunEvents() = %v, want empty", got)
	}

	// watch_resume_take takes the latch once.
	var take acpwire.WatchResumeTakeResult
	if err := conn.Call(ctx, acpwire.MethodWatchResumeTake, params, &take); err != nil {
		t.Fatalf("session/watch_resume_take: %v", err)
	}
	if !take.OK || take.Name != "ci" || !take.Repeat {
		t.Fatalf("take = %+v, want ci, repeat, ok", take)
	}
	var again acpwire.WatchResumeTakeResult
	if err := conn.Call(ctx, acpwire.MethodWatchResumeTake, params, &again); err != nil {
		t.Fatalf("session/watch_resume_take again: %v", err)
	}
	if again.OK {
		t.Fatalf("take = %+v, want ok false the second time", again)
	}
}

// TestAControlWithoutTheCapabilityReachesTheTurnManager pins R9's compatibility
// arm for session/steer: without the capability ACP's own handler answers, and
// a queued text with no turn in flight is its refusal.
func TestAControlWithoutTheCapabilityReachesTheTurnManager(t *testing.T) {
	state := seamState(t, controlSession)
	turns := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: id}, id == controlSession
		},
		Notify: func(string, any) error { return nil },
	})
	conn, _ := startControlAgent(t, false, service.Deps{State: state},
		map[string]Handler{acpwire.MethodSteer: turns.Steer})

	var res json.RawMessage
	err := conn.Call(context.Background(), acpwire.MethodSteer, acpwire.SteerCallParams{
		SessionID: controlSession,
		Text:      "queued with no turn",
	}, &res)
	if err == nil || !strings.Contains(err.Error(), "has no active turn") {
		t.Fatalf("err = %v, want ACP's no-active-turn refusal", err)
	}
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("steering queue = %v, want the service never to have seen it", got)
	}
}

// TestTheControlsRefuseBadParams pins the refusals the controls share: a
// missing session id is invalid params, an unknown one is the unknown-session
// error, and an unknown steer delivery is invalid params.
func TestTheControlsRefuseBadParams(t *testing.T) {
	conn, _ := startControlAgent(t, true, service.Deps{State: seamState(t, controlSession)}, nil)

	methods := []struct {
		name   string
		method string
	}{
		{"steer", acpwire.MethodSteer},
		{"steering_pop", acpwire.MethodSteeringPop},
		{"steering_clear", acpwire.MethodSteeringClear},
		{"set_mode", acpwire.MethodSetMode},
		{"set_effort", acpwire.MethodSetEffort},
		{"set_sudo_unlock", acpwire.MethodSetSudoUnlock},
		{"subagent_cancel", acpwire.MethodSubagentCancel},
		{"sdd_clear", acpwire.MethodSDDClear},
		{"watch_resume_take", acpwire.MethodWatchResumeTake},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var res json.RawMessage
			err := conn.Call(context.Background(), m.method, json.RawMessage(`{}`), &res)
			var rpcErr *rpc.Error
			if err == nil || !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
				t.Fatalf("err = %v, want the invalid-params refusal", err)
			}
			err = conn.Call(context.Background(), m.method, json.RawMessage(`{"sessionId":"nope"}`), &res)
			if err == nil || !strings.Contains(err.Error(), "unknown session") {
				t.Fatalf("err = %v, want the unknown-session refusal", err)
			}
		})
	}

	t.Run("steer_delivery", func(t *testing.T) {
		var res json.RawMessage
		err := conn.Call(context.Background(), acpwire.MethodSteer, acpwire.SteerCallParams{
			SessionID: controlSession,
			Text:      "x",
			Delivery:  "later",
		}, &res)
		var rpcErr *rpc.Error
		if err == nil || !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
			t.Fatalf("err = %v, want the invalid-params refusal for an unknown delivery", err)
		}
	})
}
