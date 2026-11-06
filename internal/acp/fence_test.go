package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// fenceTestTimeout bounds every wait in this file so a broken fence fails
// the test instead of hanging the suite.
const fenceTestTimeout = 5 * time.Second

// fenceTestServer records the order in which fences were served. Each
// received request is appended to order and then answered, so a caller that
// blocks on ask observes the service before ask returns.
type fenceTestServer struct {
	mu    sync.Mutex
	order []string
	stop  chan struct{}
	wg    sync.WaitGroup
}

// fenceTestServe starts a forwarder loop for f that records name when it
// serves a request. The returned func stops the loop.
func fenceTestServe(f *fence, name string, rec *fenceTestServer) func() {
	rec.mu.Lock()
	if rec.stop == nil {
		rec.stop = make(chan struct{})
	}
	stop := rec.stop
	rec.mu.Unlock()
	rec.wg.Add(1)
	go func() {
		defer rec.wg.Done()
		for {
			select {
			case req := <-f.requests():
				rec.mu.Lock()
				rec.order = append(rec.order, name)
				rec.mu.Unlock()
				close(req)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		rec.mu.Lock()
		if rec.stop != nil {
			close(rec.stop)
			rec.stop = nil
		}
		rec.mu.Unlock()
		rec.wg.Wait()
	}
}

// recorded returns a copy of the names served so far.
func (rec *fenceTestServer) recorded() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, len(rec.order))
	copy(out, rec.order)
	return out
}

func (rec *fenceTestServer) reset() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.order = nil
}

// TestAFenceWaitsForItsForwarder checks that ask returns only once the
// forwarder has served the request, not when the request is handed over.
func TestAFenceWaitsForItsForwarder(t *testing.T) {
	t.Parallel()
	f := newFence()

	served := make(chan struct{})
	gate := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case req := <-f.requests():
				close(served)
				<-gate
				close(req)
			case <-stop:
				return
			}
		}
	}()

	asked := make(chan struct{})
	go func() {
		f.ask(context.Background())
		close(asked)
	}()

	select {
	case <-served:
	case <-time.After(fenceTestTimeout):
		t.Fatal("forwarder never received the request")
	}
	select {
	case <-asked:
		t.Fatal("ask returned before the forwarder answered")
	default:
	}

	close(gate)
	select {
	case <-asked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask did not return after the forwarder answered")
	}
}

// TestAFenceReturnsWhenItsForwarderIsGone checks that a closed fence makes
// pending and later asks return, and that close is idempotent.
func TestAFenceReturnsWhenItsForwarderIsGone(t *testing.T) {
	t.Parallel()

	closed := newFence()
	closed.close()
	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	asked := make(chan struct{})
	go func() {
		closed.ask(ctx)
		close(asked)
	}()
	select {
	case <-asked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask on a closed fence did not return")
	}
	closed.close() // must not panic

	unserved := newFence()
	blocked := make(chan struct{})
	go func() {
		unserved.ask(context.Background())
		close(blocked)
	}()
	// Give the ask a chance to block on the unserved requests channel; the
	// later close must release it either way.
	select {
	case <-blocked:
		t.Fatal("ask returned while nobody had served or closed the fence")
	default:
	}
	unserved.close()
	select {
	case <-blocked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask did not return when the fence was closed")
	}
	unserved.close() // must not panic
}

// TestAFenceReturnsWhenTheContextEnds checks that a fence nobody serves
// releases its asker when the context ends.
func TestAFenceReturnsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	f := newFence()
	ctx, cancel := context.WithCancel(context.Background())

	asked := make(chan struct{})
	go func() {
		f.ask(ctx)
		close(asked)
	}()
	select {
	case <-asked:
		t.Fatal("ask returned before the context ended")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-asked:
	case <-time.After(fenceTestTimeout):
		t.Fatal("ask did not return when the context ended")
	}
}

// TestAFenceSetAsksEveryFenceInOrder checks that wait serves a session's
// fences in the order they were added, ignores other sessions, and stops
// asking a fence once it is removed.
func TestAFenceSetAsksEveryFenceInOrder(t *testing.T) {
	t.Parallel()
	rec := &fenceTestServer{}
	first := newFence()
	second := newFence()
	other := newFence()
	stopFirst := fenceTestServe(first, "first", rec)
	defer stopFirst()
	stopSecond := fenceTestServe(second, "second", rec)
	defer stopSecond()
	stopOther := fenceTestServe(other, "other", rec)
	defer stopOther()

	var s fenceSet
	s.add("session-1", first)
	s.add("session-1", second)
	s.add("session-2", other)

	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	s.wait(ctx, "session-1")

	if got := rec.recorded(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("wait served %v, want [first second]", got)
	}

	// Unknown fences and sessions remove nothing and must not panic.
	s.remove("session-1", newFence())
	s.remove("no-such-session", other)

	rec.reset()
	s.remove("session-1", first)
	s.wait(ctx, "session-1")
	if got := rec.recorded(); len(got) != 1 || got[0] != "second" {
		t.Fatalf("after remove wait served %v, want [second]", got)
	}

	rec.reset()
	s.remove("session-1", second)
	if _, ok := s.fences["session-1"]; ok {
		t.Fatal("removing the last fence left an empty slice behind")
	}
	s.wait(ctx, "session-1")
	if got := rec.recorded(); len(got) != 0 {
		t.Fatalf("wait on a session with no fences served %v, want none", got)
	}
}

// The sessions these forwarder tests serve.
const (
	fenceTurnSession  = "sess_fence_turn"
	fenceStackSession = "sess_fence_stack"
	fenceWorkSession  = "sess_fence_work"
	fenceWatchSession = "sess_fence_watch"
	fenceDoneSession  = "sess_fence_done"
)

// fenceUpdateRecorder records the session/update frames a manager writes, as
// JSON, so a test can inspect kinds and payloads without depending on each
// envelope's Go type. delay slows every update, so a fence that did not wait
// for its forwarder answers before the update lands.
type fenceUpdateRecorder struct {
	mu     sync.Mutex
	delay  time.Duration
	frames []string
}

func (r *fenceUpdateRecorder) notify(method string, params any) error {
	if method == acpwire.MethodSessionUpdate && r.delay > 0 {
		time.Sleep(r.delay)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil
	}
	r.mu.Lock()
	r.frames = append(r.frames, string(encoded))
	r.mu.Unlock()
	return nil
}

// fenceUpdateKind is the update kind a recorded frame carries, or "".
func fenceUpdateKind(frame string) string {
	var envelope struct {
		Update struct {
			Kind string `json:"kind"`
		} `json:"update"`
	}
	if err := json.Unmarshal([]byte(frame), &envelope); err != nil {
		return ""
	}
	return envelope.Update.Kind
}

// kinds returns the kind of every recorded frame, in order.
func (r *fenceUpdateRecorder) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.frames))
	for _, frame := range r.frames {
		out = append(out, fenceUpdateKind(frame))
	}
	return out
}

// wrote reports whether some recorded frame of kind carries every needle.
func (r *fenceUpdateRecorder) wrote(kind string, needles ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, frame := range r.frames {
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

// fenceSetLen is how many fences are registered for a session.
func fenceSetLen(s *fenceSet, sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.fences[sessionID])
}

// TestTheTurnForwarderFenceCoversEventsPublishedBefore checks that a fence
// asked after an event was published answers only once that event's update is
// written. Updates are slowed, so a fence that did not drain would return
// first.
func TestTheTurnForwarderFenceCoversEventsPublishedBefore(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	// A burst: the loop is busy writing the first update while the rest wait in
	// the subscription's buffer, which is the state the fence must drain.
	const published = 10
	turnStarted := make(chan struct{})
	rec := &fenceUpdateRecorder{delay: 30 * time.Millisecond}

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					close(turnStarted)
					<-ctx.Done()
					return ctx.Err()
				}),
				Events: broker,
				Steer: func(text string) {
					// Publish steering snapshots, as the runtime does when
					// steers are pushed.
					for i := 0; i < published; i++ {
						broker.Publish(session.EventSteeringState, session.Event{
							Steering: &session.SteeringSnapshot{
								Version:   uint64(i + 1),
								TurnID:    fmt.Sprintf("turn-%02d", i),
								Active:    true,
								Supported: true,
							},
						})
					}
				},
			}, sessionID == fenceTurnSession
		},
		Notify: rec.notify,
	})
	t.Cleanup(func() {
		_, _ = manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"`+fenceTurnSession+`"}`))
	})

	go func() {
		_, _ = manager.PromptTurn(context.Background(),
			json.RawMessage(`{"sessionId":"`+fenceTurnSession+`","prompt":[{"type":"text","text":"hi"}]}`))
	}()
	select {
	case <-turnStarted:
	case <-time.After(fenceTestTimeout):
		t.Fatal("the turn never started")
	}

	if _, err := manager.Steer(context.Background(),
		json.RawMessage(`{"sessionId":"`+fenceTurnSession+`","text":"focus on tests"}`)); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	manager.fenceSession(ctx, fenceTurnSession)

	for i := 0; i < published; i++ {
		marker := fmt.Sprintf(`"turnId":"turn-%02d"`, i)
		if !rec.wrote("steering_state", marker) {
			t.Fatalf("the fence answered before the update for event %d was written: kinds %v", i, rec.kinds())
		}
	}
}

// TestTheIdleStackFenceFlushesAChange checks that a fence on the idle stack
// loop flushes a change published before it was asked, so a read after the
// call sees the new node.
func TestTheIdleStackFenceFlushesAChange(t *testing.T) {
	rec := &fenceUpdateRecorder{}
	broker := pubsub.NewBroker[session.Event]()
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID, State: st, Events: broker}, sessionID == fenceStackSession
		},
		Notify: rec.notify,
	})

	// One main-stack read activates the projector and starts the idle loop.
	if _, err := manager.Stack(context.Background(),
		json.RawMessage(`{"sessionId":"`+fenceStackSession+`"}`)); err != nil {
		t.Fatalf("Stack: %v", err)
	}

	before := viewmodel.ProjectState(st, false, time.Now()).Tree
	st.AddMessage(session.RoleAssistant, "after the call", session.ContentTypePlain)
	after := viewmodel.ProjectState(st, false, time.Now()).Tree
	known := make(map[string]bool, len(before.Nodes))
	for _, n := range before.Nodes {
		known[n.ID] = true
	}
	var added []string
	for _, n := range after.Nodes {
		if !known[n.ID] {
			added = append(added, n.ID)
		}
	}
	if len(added) == 0 {
		t.Fatal("the added message produced no new node")
	}

	broker.Publish(session.EventMessageAdded, session.Event{})

	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	manager.fenceSession(ctx, fenceStackSession)

	if !rec.wrote("stack_patch", added[0]) {
		t.Fatalf("the fence answered before the patch for %q: kinds %v", added[0], rec.kinds())
	}
}

// TestTheWorkForwarderFenceCoversASnapshot checks that a fence on the work
// forwarder covers a snapshot published before it was asked.
func TestTheWorkForwarderFenceCoversASnapshot(t *testing.T) {
	rec := &fenceUpdateRecorder{}
	svc := &fakeWorkService{
		snapshot:     viewmodel.WorkSnapshot{RuntimeEpoch: "epoch-1"},
		subscription: make(chan pubsub.Event[viewmodel.WorkSnapshot], 4),
	}
	manager := NewWorkManagerACP(func(sessionID string) (viewmodel.WorkService, bool) {
		return svc, sessionID == fenceWorkSession
	}, rec.notify)
	t.Cleanup(manager.Close)

	// One work_list starts the session's forwarder.
	if _, err := manager.List(context.Background(),
		json.RawMessage(`{"sessionId":"`+fenceWorkSession+`"}`)); err != nil {
		t.Fatalf("List: %v", err)
	}

	svc.subscription <- pubsub.Event[viewmodel.WorkSnapshot]{
		Type:    "work",
		Payload: viewmodel.WorkSnapshot{RuntimeEpoch: "epoch-1"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	manager.fenceSession(ctx, fenceWorkSession)

	if !rec.wrote("work_state", `"runtimeEpoch":"epoch-1"`) {
		t.Fatalf("the fence answered before the work_state update: kinds %v", rec.kinds())
	}
}

// TestTheWatchForwarderFenceCoversAnEvent checks that a fence on the watch
// forwarder covers an event published before it was asked.
func TestTheWatchForwarderFenceCoversAnEvent(t *testing.T) {
	rec := &fenceUpdateRecorder{}
	broker := pubsub.NewBroker[watch.Event]()
	wm := watch.NewManager(context.Background(), watch.Deps{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = wm.Close(ctx)
		broker.Close()
	})
	manager := NewWatchManagerACP(func(sessionID string) (*watch.Manager, *pubsub.Broker[watch.Event], bool) {
		return wm, broker, sessionID == fenceWatchSession
	}, rec.notify)

	// One watch_list starts the session's forwarder.
	if _, err := manager.List(context.Background(),
		json.RawMessage(`{"sessionId":"`+fenceWatchSession+`"}`)); err != nil {
		t.Fatalf("List: %v", err)
	}

	broker.Publish("watch", watch.Event{WatchID: "watch-1", Name: "build"})

	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	manager.fenceSession(ctx, fenceWatchSession)

	if !rec.wrote("watch", `"watchId":"watch-1"`) {
		t.Fatalf("the fence answered before the watch update: kinds %v", rec.kinds())
	}
}

// TestAFenceOnAFinishedTurnReturnsAtOnce checks that a turn's fence is gone
// with its forwarder, so asking it afterwards answers without waiting.
func TestAFenceOnAFinishedTurnReturnsAtOnce(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run:       RunnerFunc(func(context.Context, string) error { return nil }),
				Events:    broker,
			}, sessionID == fenceDoneSession
		},
		Notify: func(string, any) error { return nil },
	})

	if _, err := manager.PromptTurn(context.Background(),
		json.RawMessage(`{"sessionId":"`+fenceDoneSession+`","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}
	if left := fenceSetLen(&manager.fences, fenceDoneSession); left != 0 {
		t.Fatalf("%d fences left after the turn ended, want none", left)
	}

	ctx, cancel := context.WithTimeout(context.Background(), fenceTestTimeout)
	defer cancel()
	start := time.Now()
	manager.fenceSession(ctx, fenceDoneSession)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("the fence on a finished turn took %v", elapsed)
	}
}
