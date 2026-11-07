package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// orderingSession is the session every ordering test serves.
const orderingSession = "sess_ordering"

// syncBuffer is a log sink the server's goroutines can write while a test
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// orderingAgent is the agent end of an ordering test: the three managers
// registerHandlers builds for a marshal client, the handlers the tests call,
// and the barrier between a call's handler and its response.
type orderingAgent struct {
	srv     *Server
	turns   *TurnManager
	works   *WorkManagerACP
	watches *WatchManagerACP
	st      *session.State
	broker  *pubsub.Broker[session.Event]
	workSvc *fakeWorkService
	watchWM *watch.Manager
	watchBr *pubsub.Broker[watch.Event]
	logs    *syncBuffer

	turnStarted chan struct{}
	turnOnce    sync.Once

	// notifyDelay slows every session/update frame, so an ordering test sees
	// a response that did not wait for its updates. A budget test sets it to
	// zero: it measures the barrier, not an artificial write cost.
	notifyDelay time.Duration

	mu      sync.Mutex
	fetches int
	barrier int
}

// notify is the managers' sink. Every session/update frame is slowed, so a
// response that did not wait for the updates it caused is written first.
func (a *orderingAgent) notify(method string, params any) error {
	if method == acpwire.MethodSessionUpdate && a.notifyDelay > 0 {
		time.Sleep(a.notifyDelay)
	}
	return a.srv.Notify(method, params)
}

func (a *orderingAgent) markTurnStarted() {
	a.turnOnce.Do(func() { close(a.turnStarted) })
}

func (a *orderingAgent) install(srv *Server) {
	a.srv = srv
	a.turns = NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				State:     a.st,
				Events:    a.broker,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					// A turn that stays open until the test cancels it, so
					// session/steer has a live turn to steer.
					a.markTurnStarted()
					<-ctx.Done()
					return ctx.Err()
				}),
				Steer: func(text string) {
					// Publish one steering snapshot, as the runtime does.
					a.broker.Publish(session.EventSteeringState, session.Event{
						Steering: &session.SteeringSnapshot{
							Version: 1, TurnID: "turn-1", Active: true, Supported: true,
						},
					})
				},
			}, sessionID == orderingSession
		},
		Notify:  a.notify,
		Marshal: func() bool { return true },
	})
	a.works = NewWorkManagerACP(func(sessionID string) (viewmodel.WorkService, bool) {
		return a.workSvc, sessionID == orderingSession
	}, a.notify)
	a.watches = NewWatchManagerACP(func(sessionID string) (*watch.Manager, *pubsub.Broker[watch.Event], bool) {
		return a.watchWM, a.watchBr, sessionID == orderingSession
	}, a.notify)

	srv.Handle(acpwire.MethodStack, func(ctx context.Context, params json.RawMessage) (any, error) {
		a.mu.Lock()
		a.fetches++
		a.mu.Unlock()
		return a.turns.Stack(ctx, params)
	})
	srv.Handle(acpwire.MethodStackUnsubscribe, a.turns.StackUnsubscribe)
	srv.Handle("session/steer", a.turns.Steer)
	srv.Handle("session/work_list", a.works.List)
	srv.Handle("session/watch_list", a.watches.List)
	srv.Handle("test/ping", func(context.Context, json.RawMessage) (any, error) {
		return struct{}{}, nil
	})
	// test/mutate is the call V3 is proved on: it changes the transcript and
	// publishes the event the runtime would publish for that change.
	srv.Handle("test/mutate", func(context.Context, json.RawMessage) (any, error) {
		a.st.AddMessage(session.RoleAssistant, "after the call", session.ContentTypePlain)
		a.broker.Publish(session.EventMessageAdded, session.Event{})
		return struct{}{}, nil
	})
	srv.Handle("test/publish_work", func(context.Context, json.RawMessage) (any, error) {
		a.workSvc.subscription <- pubsub.Event[viewmodel.WorkSnapshot]{
			Type:    "work",
			Payload: viewmodel.WorkSnapshot{RuntimeEpoch: "epoch-1"},
		}
		return struct{}{}, nil
	})
	srv.Handle("test/publish_watch", func(context.Context, json.RawMessage) (any, error) {
		a.watchBr.Publish("watch", watch.Event{WatchID: "watch-1", Name: "build"})
		return struct{}{}, nil
	})

	srv.SetResponseBarrier(responseBarrier(a.turns, a.works, a.watches, nil))
	srv.setMarshalClient(true)
}

func (a *orderingAgent) fetchCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fetches
}

func (a *orderingAgent) barrierRuns() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.barrier
}

// countBarrier is a barrier that only counts its runs, for the test that pins
// which calls run it at all.
func (a *orderingAgent) countBarrier() {
	a.mu.Lock()
	a.barrier++
	a.mu.Unlock()
}

// orderingClient is the client end: the connection, the updates it has
// already applied, and the stack mirror the tests read.
type orderingClient struct {
	conn   *rpc.Conn
	mirror *rpc.Stacks

	mu     sync.Mutex
	frames []string
}

// notify is one Handlers.Notify: it records the update and applies it to the
// mirror, as a client's handler chain would.
func (c *orderingClient) notify(method string, params json.RawMessage) {
	if method == acpwire.MethodSessionUpdate {
		c.mu.Lock()
		c.frames = append(c.frames, string(params))
		c.mu.Unlock()
	}
	c.mirror.Notify(method, params)
}

// kinds returns the kind of every update the client has applied, in order.
func (c *orderingClient) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.frames))
	for _, frame := range c.frames {
		out = append(out, fenceUpdateKind(frame))
	}
	return out
}

// wrote reports whether an applied update of kind carries every needle.
func (c *orderingClient) wrote(kind string, needles ...string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, frame := range c.frames {
		if fenceUpdateKind(frame) != kind {
			continue
		}
		found := true
		for _, needle := range needles {
			if !strings.Contains(frame, needle) {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}

func (c *orderingClient) call(ctx context.Context, method string, params any) error {
	return c.conn.Call(ctx, method, params, nil)
}

// startOrderingAgent serves the ordering agent on a pipe and connects a client
// that records every update, builds a marshal-capable set of managers exactly
// as registerHandlers does, and installs the host's barrier.
func startOrderingAgent(tb testing.TB) (*orderingAgent, *orderingClient) {
	tb.Helper()
	return startOrderingAgentWith(tb,
		session.New(config.Default(), tb.TempDir(), time.Now(), session.Persistence{}),
		50*time.Millisecond)
}

// startOrderingAgentWith is startOrderingAgent over a caller's session and
// update delay, so a budget test can use the 2,000-call session and measure the
// barrier without an artificial write cost.
func startOrderingAgentWith(tb testing.TB, st *session.State, notifyDelay time.Duration) (*orderingAgent, *orderingClient) {
	tb.Helper()
	a := &orderingAgent{
		st:          st,
		broker:      pubsub.NewBroker[session.Event](),
		workSvc:     &fakeWorkService{snapshot: viewmodel.WorkSnapshot{RuntimeEpoch: "epoch-1"}, subscription: make(chan pubsub.Event[viewmodel.WorkSnapshot], 4)},
		watchWM:     watch.NewManager(context.Background(), watch.Deps{}),
		watchBr:     pubsub.NewBroker[watch.Event](),
		logs:        &syncBuffer{},
		turnStarted: make(chan struct{}),
		notifyDelay: notifyDelay,
	}
	c := &orderingClient{mirror: rpc.NewStacks()}

	client, agentTransport := rpc.Pipe()
	srv := NewServer(agentTransport, agentTransport,
		WithLogger(slog.New(slog.NewTextHandler(a.logs, nil))))
	srv.MaxLineBytes = rpc.MaxFrameBytes
	a.install(srv)

	serveCtx, stopServe := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(serveCtx)
	}()

	c.conn = rpc.NewConn(client, rpc.Handlers{Notify: c.notify})
	c.mirror.Bind(c.conn)

	tb.Cleanup(func() {
		_ = c.conn.Close()
		stopServe()
		_ = agentTransport.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			tb.Error("the server did not stop")
		}
		a.works.Close()
		watchCtx, cancelWatch := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelWatch()
		_ = a.watchWM.Close(watchCtx)
		a.broker.Close()
		a.watchBr.Close()
	})
	return a, c
}

// orderingAddedNodes returns the IDs in after that before did not hold.
func orderingAddedNodes(before, after []acpwire.WireNode) []string {
	known := make(map[string]bool, len(before))
	for _, n := range before {
		known[n.ID] = true
	}
	var added []string
	for _, n := range after {
		if !known[n.ID] {
			added = append(added, n.ID)
		}
	}
	return added
}

// TestAReadAfterACallSeesItsTranscriptChange (V3) checks that a marshal
// client's read after a call already holds the change the call caused, with no
// second fetch, even when the update is slow to write.
func TestAReadAfterACallSeesItsTranscriptChange(t *testing.T) {
	agent, client := startOrderingAgent(t)
	ctx := context.Background()

	before := viewmodel.ProjectState(agent.st, false, time.Now()).Tree
	// One read subscribes the connection; every later read is the mirror's.
	if _, err := client.mirror.Stack(ctx, orderingSession, 0); err != nil {
		t.Fatalf("first read: %v", err)
	}
	fetches := agent.fetchCount()

	if err := client.call(ctx, "test/mutate", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("test/mutate: %v", err)
	}

	after := viewmodel.ProjectState(agent.st, false, time.Now()).Tree
	added := orderingAddedNodes(before.Nodes, after.Nodes)
	if len(added) == 0 {
		t.Fatal("the call added no node to the projection")
	}

	snap, err := client.mirror.Stack(ctx, orderingSession, 0)
	if err != nil {
		t.Fatalf("read after the call: %v", err)
	}
	held := make(map[string]bool, len(snap.Nodes))
	for _, n := range snap.Nodes {
		held[n.ID] = true
	}
	for _, id := range added {
		if !held[id] {
			t.Fatalf("the mirror does not hold %q when the call returned: kinds %v", id, client.kinds())
		}
	}
	if got := agent.fetchCount(); got != fetches {
		t.Fatalf("the read after the call fetched again: %d fetches, want %d", got, fetches)
	}
}

// TestAResponseFollowsTheSteeringItCaused checks that session/steer returns
// only after the client has the steering_state update it caused.
func TestAResponseFollowsTheSteeringItCaused(t *testing.T) {
	agent, client := startOrderingAgent(t)
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	t.Cleanup(cancelTurn)

	go func() {
		_, _ = agent.turns.PromptTurn(turnCtx,
			json.RawMessage(`{"sessionId":"`+orderingSession+`","prompt":[{"type":"text","text":"hi"}]}`))
	}()
	select {
	case <-agent.turnStarted:
	case <-time.After(fenceTestTimeout):
		t.Fatal("the turn never started")
	}

	if err := client.call(context.Background(), "session/steer",
		map[string]any{"sessionId": orderingSession, "text": "focus on tests"}); err != nil {
		t.Fatalf("session/steer: %v", err)
	}
	if !client.wrote("steering_state", `"turnId":"turn-1"`) {
		t.Fatalf("session/steer returned before its update: kinds %v", client.kinds())
	}
}

// TestAResponseFollowsTheWorkStateItCaused checks that a call returns only
// after the work_state update it caused.
func TestAResponseFollowsTheWorkStateItCaused(t *testing.T) {
	_, client := startOrderingAgent(t)
	ctx := context.Background()

	// One work_list starts the session's forwarder.
	if err := client.call(ctx, "session/work_list", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("session/work_list: %v", err)
	}
	if err := client.call(ctx, "test/publish_work", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("test/publish_work: %v", err)
	}
	if !client.wrote("work_state", `"runtimeEpoch":"epoch-1"`) {
		t.Fatalf("the call returned before its work_state update: kinds %v", client.kinds())
	}
}

// TestAResponseFollowsTheWatchEventItCaused checks that a call returns only
// after the watch update it caused.
func TestAResponseFollowsTheWatchEventItCaused(t *testing.T) {
	_, client := startOrderingAgent(t)
	ctx := context.Background()

	// One watch_list starts the session's forwarder.
	if err := client.call(ctx, "session/watch_list", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("session/watch_list: %v", err)
	}
	if err := client.call(ctx, "test/publish_watch", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("test/publish_watch: %v", err)
	}
	if !client.wrote("watch", `"watchId":"watch-1"`) {
		t.Fatalf("the call returned before its watch update: kinds %v", client.kinds())
	}
}

// TestTheBarrierWaitsOnlyForMarshalSessionCalls (V6) checks that the barrier
// runs for a marshal client's session call and for nothing else.
func TestTheBarrierWaitsOnlyForMarshalSessionCalls(t *testing.T) {
	agent, client := startOrderingAgent(t)
	ctx := context.Background()
	agent.srv.SetResponseBarrier(func(context.Context, string) { agent.countBarrier() })

	// A client the server has not recorded as marshal.
	agent.srv.setMarshalClient(false)
	if err := client.call(ctx, "test/mutate", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("test/mutate: %v", err)
	}
	if got := agent.barrierRuns(); got != 0 {
		t.Fatalf("a non-marshal call ran the barrier %d times", got)
	}

	agent.srv.setMarshalClient(true)

	// Params that name no session.
	if err := client.call(ctx, "test/ping", map[string]any{"view": "live"}); err != nil {
		t.Fatalf("test/ping: %v", err)
	}
	if got := agent.barrierRuns(); got != 0 {
		t.Fatalf("a call naming no session ran the barrier %d times", got)
	}

	// Params that are an array, not an object.
	if err := client.call(ctx, "test/ping", []any{"s1"}); err != nil {
		t.Fatalf("test/ping with array params: %v", err)
	}
	if got := agent.barrierRuns(); got != 0 {
		t.Fatalf("a call with array params ran the barrier %d times", got)
	}

	// A notification.
	if err := client.conn.Notify("test/mutate", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	// Settle: the notification runs on its own goroutine, and a ping names no
	// session, so it cannot change the count.
	if err := client.call(ctx, "test/ping", nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := agent.barrierRuns(); got != 0 {
		t.Fatalf("a notification ran the barrier %d times", got)
	}

	// One marshal session call.
	if err := client.call(ctx, "test/mutate", map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("test/mutate: %v", err)
	}
	if got := agent.barrierRuns(); got != 1 {
		t.Fatalf("the marshal session call ran the barrier %d times, want 1", got)
	}
}

// TestAStuckFenceDoesNotHoldTheResponse (R11) checks that a forwarder that
// never answers costs the response its bound, not a hang, and is reported.
func TestAStuckFenceDoesNotHoldTheResponse(t *testing.T) {
	agent, client := startOrderingAgent(t)
	agent.srv.stateMu.Lock()
	agent.srv.barrierTimeout = 50 * time.Millisecond
	agent.srv.stateMu.Unlock()

	// A fence no forwarder serves: the barrier's wait can end only on its
	// bound.
	stuck := newFence()
	agent.turns.fences.add(orderingSession, stuck)
	t.Cleanup(func() {
		agent.turns.fences.remove(orderingSession, stuck)
		stuck.close()
	})

	start := time.Now()
	if err := client.call(context.Background(), "test/mutate",
		map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("test/mutate: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a stuck fence held the response for %v", elapsed)
	}

	logs := agent.logs.String()
	if !strings.Contains(logs, "acp: response barrier timed out") {
		t.Fatalf("the timeout was not logged: %q", logs)
	}
	if !strings.Contains(logs, "test/mutate") || !strings.Contains(logs, orderingSession) {
		t.Fatalf("the warning names neither the method nor the session: %q", logs)
	}
}

// TestAPanickingBarrierStillAnswersTheCall checks that a barrier which panics
// costs the call a logged error rather than the server. dispatchRequest's
// recover runs on the handler goroutine and cannot reach the barrier's own.
func TestAPanickingBarrierStillAnswersTheCall(t *testing.T) {
	agent, client := startOrderingAgent(t)
	agent.srv.SetResponseBarrier(func(context.Context, string) {
		panic("barrier exploded")
	})

	start := time.Now()
	if err := client.call(context.Background(), "test/mutate",
		map[string]any{"sessionId": orderingSession}); err != nil {
		t.Fatalf("test/mutate: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a panicking barrier held the response for %v", elapsed)
	}

	logs := agent.logs.String()
	if !strings.Contains(logs, "acp: response barrier panicked") {
		t.Fatalf("the panic was not logged: %q", logs)
	}
	if !strings.Contains(logs, "test/mutate") || !strings.Contains(logs, orderingSession) {
		t.Fatalf("the panic record names neither the method nor the session: %q", logs)
	}

	// The server is still serving: a later call answers normally.
	if err := client.call(context.Background(), "test/ping", nil); err != nil {
		t.Fatalf("the server did not survive the panic: %v", err)
	}
}

// TestResponseBarrierBudget holds R10's synchronous-call budget for a marshal
// client's session call over the pipe on the 2,000-call session, with the main
// stack subscribed. A call that changes nothing must round-trip in under 1ms
// median; a call that adds one message is measured too, with no budget.
func TestResponseBarrierBudget(t *testing.T) {
	withoutMemoCheck(t)
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	_, client := startOrderingAgentWith(t, st, 0)
	ctx := context.Background()

	// One read subscribes the connection and starts the idle stack loop, which
	// is the forwarder this call's fence asks.
	if _, err := client.mirror.Stack(ctx, orderingSession, 0); err != nil {
		t.Fatalf("first read: %v", err)
	}

	const noChangeRuns = 30
	noChange := make([]time.Duration, 0, noChangeRuns)
	for i := 0; i < noChangeRuns; i++ {
		start := time.Now()
		if err := client.call(ctx, "test/ping", map[string]any{"sessionId": orderingSession}); err != nil {
			t.Fatalf("no-change call: %v", err)
		}
		noChange = append(noChange, time.Since(start))
	}
	noChangeMedian := medianDuration(noChange)

	const changeRuns = 10
	change := make([]time.Duration, 0, changeRuns)
	for i := 0; i < changeRuns; i++ {
		start := time.Now()
		if err := client.call(ctx, "test/mutate", map[string]any{"sessionId": orderingSession}); err != nil {
			t.Fatalf("change call: %v", err)
		}
		change = append(change, time.Since(start))
	}
	changeMedian := medianDuration(change)

	t.Logf("response barrier: no-change median %v over %d calls (budget 1ms); change median %v over %d calls (no budget)",
		noChangeMedian, noChangeRuns, changeMedian, changeRuns)
	if noChangeMedian >= time.Millisecond {
		t.Fatalf("no-change median = %v, budget 1ms (R10): do not loosen it", noChangeMedian)
	}
}

// BenchmarkResponseBarrierNoChange records the same no-change call for the
// record.
func BenchmarkResponseBarrierNoChange(b *testing.B) {
	withoutMemoCheck(b)
	st := largeStackSession(b)
	_, client := startOrderingAgentWith(b, st, 0)
	ctx := context.Background()
	if _, err := client.mirror.Stack(ctx, orderingSession, 0); err != nil {
		b.Fatalf("first read: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.call(ctx, "test/ping", map[string]any{"sessionId": orderingSession}); err != nil {
			b.Fatalf("no-change call: %v", err)
		}
	}
}
