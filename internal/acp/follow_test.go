package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// followSession is the session id every follow test uses.
const followSession = "s1"

// scriptedFollowRunner is the service's runner: it runs the body a test set, so
// a test decides when the run's work emits an event or ends.
type scriptedFollowRunner struct {
	mu   sync.Mutex
	body func(ctx context.Context) error
}

func newScriptedFollowRunner() *scriptedFollowRunner { return &scriptedFollowRunner{} }

func (r *scriptedFollowRunner) Run(ctx context.Context, goal string) error {
	r.mu.Lock()
	body := r.body
	r.mu.Unlock()
	if body != nil {
		return body(ctx)
	}
	return nil
}

// setBody replaces what Run does.
func (r *scriptedFollowRunner) setBody(body func(ctx context.Context) error) {
	r.mu.Lock()
	r.body = body
	r.mu.Unlock()
}

// followHarness is one session with a real service session over a real state,
// a TurnManager that can follow it, a scripted runner for each run path, and
// the recording sink every update is written to.
type followHarness struct {
	state  *session.State
	broker *pubsub.Broker[session.Event]
	served *service.Session
	turns  *TurnManager
	runner *scriptedFollowRunner

	mu      sync.Mutex
	acpBody func(ctx context.Context, prompt string) error
	updates []map[string]any
	// notifyHook, when set, is called with every recorded update after it is
	// recorded. A test uses it to hold the follower inside one notify, which
	// is how a run can end while the follower still holds a later event.
	notifyHook func(map[string]any)
}

// newFollowHarness builds the harness. client is the connection's permission
// client; nil means the connection has no bridge.
func newFollowHarness(t *testing.T, client PermissionClient) *followHarness {
	t.Helper()
	h := &followHarness{runner: newScriptedFollowRunner()}
	h.state = session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{SessionID: followSession})
	h.broker = pubsub.NewBroker[session.Event]()
	h.state.SetEventBroker(h.broker)

	_, served := service.New(service.Deps{
		State:  h.state,
		Runner: func() service.Runner { return h.runner },
	})
	h.served = served

	tm := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			if id != followSession {
				return nil, false
			}
			return &TurnRuntime{
				SessionID: id,
				BeginWork: identityBeginWork,
				Run:       h.runTurnRunner,
				Events:    h.broker,
				State:     h.state,
			}, true
		},
		Notify: h.notify,
		Perms:  client,
		Service: func(id string) (*service.Session, bool) {
			return served, id == followSession
		},
	})
	h.turns = tm
	t.Cleanup(tm.CloseFollowers)
	return h
}

// runTurnRunner is this connection's own turn path's runner.
func (h *followHarness) runTurnRunner(ctx context.Context, prompt string) error {
	h.mu.Lock()
	body := h.acpBody
	h.mu.Unlock()
	if body != nil {
		return body(ctx, prompt)
	}
	return nil
}

// notify records every session/update body written to this connection.
func (h *followHarness) notify(method string, params any) error {
	if method != "session/update" {
		return nil
	}
	p, ok := params.(SessionUpdateParams)
	if !ok {
		return nil
	}
	update := p.Update
	if update == nil {
		return nil
	}
	h.mu.Lock()
	h.updates = append(h.updates, update)
	hook := h.notifyHook
	h.mu.Unlock()
	if hook != nil {
		hook(update)
	}
	return nil
}

// recorded returns the updates received so far, in order.
func (h *followHarness) recorded() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.updates...)
}

// follow is session/follow for the harness's session.
func (h *followHarness) follow(t *testing.T) {
	t.Helper()
	if _, err := h.turns.Follow(context.Background(), json.RawMessage(
		`{"sessionId":"`+followSession+`"}`)); err != nil {
		t.Fatalf("session/follow: %v", err)
	}
}

// startRun starts one run on the service and reports its error.
func (h *followHarness) startRun(t *testing.T) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := h.served.Prompt(context.Background(), acpwire.PromptParams{Text: "hello"})
		done <- err
	}()
	return done
}

// waitRun waits for a started path to return.
func (h *followHarness) waitRun(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return", what)
		return nil
	}
}

// waitUpdate waits for the first update matching want and returns it.
func (h *followHarness) waitUpdate(t *testing.T, what string, want func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, u := range h.recorded() {
			if want(u) {
				return u
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; recorded %v", what, h.labels())
	return nil
}

// labels names the recorded updates the ordering assertions care about, in the
// order they arrived.
func (h *followHarness) labels() []string {
	var out []string
	for _, u := range h.recorded() {
		switch u["kind"] {
		case updateFollowedRun, "tool_call", "session_telemetry":
			label, _ := u["kind"].(string)
			if state, ok := u["state"].(string); ok && state != "" {
				label += "/" + state
			}
			out = append(out, label)
		}
	}
	return out
}

// countKind counts the recorded updates with the given kind.
func (h *followHarness) countKind(kind string) int {
	n := 0
	for _, u := range h.recorded() {
		if u["kind"] == kind {
			n++
		}
	}
	return n
}

// followedUpdateWith matches one followed_run update in the given state.
func followedUpdateWith(state string) func(map[string]any) bool {
	return func(u map[string]any) bool {
		return u["kind"] == updateFollowedRun && u["state"] == state
	}
}

// toolCallUpdate matches any tool_call update.
func toolCallUpdate(u map[string]any) bool { return u["kind"] == "tool_call" }

// waitUntil polls cond until it reports true or the timeout expires.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after 5s waiting for %s", what)
}

// TestAFollowedRunIsReportedStartToEnd pins decision 3's order: a run started
// on the service is reported as started, its tool call is forwarded, then its
// telemetry, and only then ended with the run's outcome.
func TestAFollowedRunIsReportedStartToEnd(t *testing.T) {
	h := newFollowHarness(t, &recordingPermissionClient{decision: PermissionDecision{Approved: true}})
	h.follow(t)

	release := make(chan struct{})
	h.runner.setBody(func(ctx context.Context) error {
		h.state.SetActiveToolCall(session.ActiveToolCall{
			Name:       "shell.run",
			ToolCallID: "call-1",
			Args:       "date",
			StartedAt:  time.Unix(100, 0),
		})
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	done := h.startRun(t)

	h.waitUpdate(t, "the run's started update", followedUpdateWith(followedRunStarted))
	toolCall := h.waitUpdate(t, "the run's tool call", toolCallUpdate)
	if got := toolCall["toolName"]; got != "shell.run" {
		t.Fatalf("tool_call name = %v, want shell.run", got)
	}

	close(release)
	ended := h.waitUpdate(t, "the run's ended update", followedUpdateWith(followedRunEnded))
	if got := ended["outcome"]; got != acpwire.RunDone {
		t.Fatalf("ended outcome = %v, want %q", got, acpwire.RunDone)
	}
	if err := h.waitRun(t, done, "the service run"); err != nil {
		t.Fatalf("the service run: %v", err)
	}

	want := []string{"followed_run/started", "tool_call", "session_telemetry", "followed_run/ended"}
	if got := h.labels(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the followed run's updates = %v, want %v", got, want)
	}
}

// TestAFollowedRunsApprovalGoesToTheBridge pins that a followed run's approval
// reaches this connection's permission client, and that the answer settles it
// in the session's own state.
func TestAFollowedRunsApprovalGoesToTheBridge(t *testing.T) {
	client := &recordingPermissionClient{decision: PermissionDecision{Approved: true}}
	h := newFollowHarness(t, client)
	h.follow(t)

	answered := make(chan session.UserApprovalDecision, 1)
	h.runner.setBody(func(ctx context.Context) error {
		response := make(chan session.UserApprovalDecision, 1)
		pending := &session.PendingToolCall{
			ID:           "call-1",
			Name:         "shell.run",
			Command:      "date",
			ResponseChan: response,
		}
		if _, err := h.state.RegisterApproval(ctx, pending); err != nil {
			return err
		}
		select {
		case decision := <-response:
			answered <- decision
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	done := h.startRun(t)

	waitUntil(t, "the approval to reach the permission client", func() bool {
		return client.count() > 0
	})
	if got := client.last().ToolCallID; got != "call-1" {
		t.Fatalf("the forwarded request names %q, want call-1", got)
	}
	select {
	case decision := <-answered:
		if !decision.Approved {
			t.Fatalf("the run received %+v, want the bridge's approval", decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run never received the bridge's answer")
	}
	if got := h.state.PendingApproval(); got != nil {
		t.Fatalf("the approval was not settled in the state: %+v", got)
	}
	if err := h.waitRun(t, done, "the service run"); err != nil {
		t.Fatalf("the service run: %v", err)
	}
}

// TestAViewersAnswerEndsAFollowedRunsRequest pins that an answer given through
// the session's state ends the forwarded request's context, and the run goes
// on with the decision the viewer made.
func TestAViewersAnswerEndsAFollowedRunsRequest(t *testing.T) {
	// The child approval tests' blocking client is exactly this shape: it
	// blocks until its context ends and reports both moments.
	client := newSettledChildApprovalClient()
	h := newFollowHarness(t, client)
	h.follow(t)

	keys := make(chan session.PromptKey, 1)
	h.runner.setBody(func(ctx context.Context) error {
		response := make(chan session.UserApprovalDecision, 1)
		pending := &session.PendingToolCall{
			ID:           "call-1",
			Name:         "shell.run",
			Command:      "date",
			ResponseChan: response,
		}
		key, err := h.state.RegisterApproval(ctx, pending)
		if err != nil {
			return err
		}
		keys <- key
		select {
		case <-response:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	done := h.startRun(t)

	select {
	case <-client.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the approval was never forwarded to the permission client")
	}
	var key session.PromptKey
	select {
	case key = <-keys:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never registered its approval")
	}
	// The viewer's answer, through the session's own settlement path.
	if _, err := h.state.ResolveApproval(key, session.UserApprovalDecision{Approved: true}, nil); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	select {
	case <-client.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the viewer's answer did not end the forwarded request")
	}

	if err := h.waitRun(t, done, "the followed run"); err != nil {
		t.Fatalf("the followed run: %v", err)
	}
	ended := h.waitUpdate(t, "the run's ended update", followedUpdateWith(followedRunEnded))
	if got := ended["outcome"]; got != acpwire.RunDone {
		t.Fatalf("ended outcome = %v, want %q", got, acpwire.RunDone)
	}
}

// TestTheConnectionsOwnTurnIsNotFollowed pins decision 2's boundary: the
// bridge's own turn is reported once, by the turn, and the follower sends no
// started for it.
func TestTheConnectionsOwnTurnIsNotFollowed(t *testing.T) {
	h := newFollowHarness(t, &recordingPermissionClient{decision: PermissionDecision{Approved: true}})
	h.follow(t)

	h.mu.Lock()
	h.acpBody = func(ctx context.Context, prompt string) error {
		h.state.SetActiveToolCall(session.ActiveToolCall{
			Name:       "shell.run",
			ToolCallID: "call-own",
			Args:       "date",
			StartedAt:  time.Unix(100, 0),
		})
		return nil
	}
	h.mu.Unlock()

	if _, err := h.turns.PromptTurn(context.Background(), json.RawMessage(
		`{"sessionId":"`+followSession+`","prompt":[{"type":"text","text":"hello"}]}`)); err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	h.waitUpdate(t, "the turn's tool call", toolCallUpdate)

	for _, u := range h.recorded() {
		if u["kind"] == updateFollowedRun {
			t.Fatalf("the follower reported the bridge's own turn: %+v", u)
		}
	}
	if got := h.countKind("tool_call"); got != 1 {
		t.Fatalf("the turn's tool call was reported %d times, want once", got)
	}
}

// TestTheBridgesCancelStopsAFollowedRun pins that a session/cancel for a
// session with no turn of this connection's own stops the service's run, which
// ends cancelled.
func TestTheBridgesCancelStopsAFollowedRun(t *testing.T) {
	h := newFollowHarness(t, &recordingPermissionClient{decision: PermissionDecision{Approved: true}})
	h.follow(t)

	h.runner.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	done := h.startRun(t)
	h.waitUpdate(t, "the run's started update", followedUpdateWith(followedRunStarted))

	if _, err := h.turns.Cancel(context.Background(), json.RawMessage(
		`{"sessionId":"`+followSession+`"}`)); err != nil {
		t.Fatalf("session/cancel: %v", err)
	}
	ended := h.waitUpdate(t, "the run's ended update", followedUpdateWith(followedRunEnded))
	if got := ended["outcome"]; got != acpwire.RunCancelled {
		t.Fatalf("ended outcome = %v, want %q", got, acpwire.RunCancelled)
	}
	if err := h.waitRun(t, done, "the service run"); err != nil {
		t.Fatalf("the service run: %v", err)
	}
}

// TestAMarshalClientCannotFollow pins the refusal: a marshal client reads its
// runs from the session's status.
func TestAMarshalClientCannotFollow(t *testing.T) {
	tm := NewTurnManager(TurnManagerConfig{
		// The marshal refusal precedes the lookup: no runtime is needed, and
		// providing one would imply the refusal depends on it.
		Lookup:  func(string) (*TurnRuntime, bool) { return nil, false },
		Notify:  func(string, any) error { return nil },
		Marshal: func() bool { return true },
	})
	_, err := tm.Follow(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err == nil {
		t.Fatal("Follow accepted a marshal client")
	}
	const refusal = "a marshal client reads its runs from the session's status"
	if !strings.Contains(err.Error(), refusal) {
		t.Fatalf("Follow's refusal = %q, want %q", err.Error(), refusal)
	}
	var rpcErr *jsonRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != invalidParams {
		t.Fatalf("Follow's refusal code = %v, want invalidParams", err)
	}
}

// TestFollowIsIdempotent pins that a second Follow for the same session and
// runtime reports one run once.
func TestFollowIsIdempotent(t *testing.T) {
	h := newFollowHarness(t, &recordingPermissionClient{decision: PermissionDecision{Approved: true}})
	h.follow(t)
	h.follow(t)

	release := make(chan struct{})
	h.runner.setBody(func(ctx context.Context) error {
		h.state.SetActiveToolCall(session.ActiveToolCall{
			Name:       "shell.run",
			ToolCallID: "call-1",
			Args:       "date",
			StartedAt:  time.Unix(100, 0),
		})
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	done := h.startRun(t)

	h.waitUpdate(t, "the run's tool call", toolCallUpdate)
	close(release)
	h.waitUpdate(t, "the run's ended update", followedUpdateWith(followedRunEnded))
	if err := h.waitRun(t, done, "the service run"); err != nil {
		t.Fatalf("the service run: %v", err)
	}

	if got := h.countKind(updateFollowedRun); got != 2 {
		t.Fatalf("Follow reported %d followed_run updates, want one started and one ended", got)
	}
	if got := h.countKind("tool_call"); got != 1 {
		t.Fatalf("the run's tool call was reported %d times, want once", got)
	}
}

// TestAFollowedRunsTailIsNotDropped pins the handover at the end of a run: the
// event the follower is holding when the run's slot is released still reaches
// the viewer. endRun closes the run's forwarder, so an event forwarded after it
// would be dropped — which is exactly the last event of a run that ends in a
// burst.
func TestAFollowedRunsTailIsNotDropped(t *testing.T) {
	h := newFollowHarness(t, &recordingPermissionClient{decision: PermissionDecision{Approved: true}})
	h.follow(t)

	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// Hold the follower inside the first tool call's notify, so the run's next
	// event is already waiting on its channel when the run ends.
	h.notifyHook = func(u map[string]any) {
		if u["kind"] != "tool_call" {
			return
		}
		once.Do(func() {
			close(held)
			<-release
		})
	}

	h.runner.setBody(func(ctx context.Context) error {
		h.state.SetActiveToolCall(session.ActiveToolCall{
			Name:       "shell.run",
			ToolCallID: "call-1",
			Args:       "date",
			StartedAt:  time.Unix(100, 0),
		})
		select {
		case <-held:
		case <-ctx.Done():
			return ctx.Err()
		}
		h.state.SetActiveToolCall(session.ActiveToolCall{
			Name:       "shell.run",
			ToolCallID: "call-2",
			Args:       "date",
			StartedAt:  time.Unix(101, 0),
		})
		return nil
	})
	done := h.startRun(t)

	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the follower never notified the run's first tool call")
	}
	// The run's slot is released with call-2 still queued behind the notify the
	// follower is stuck in.
	if err := h.waitRun(t, done, "the service run"); err != nil {
		t.Fatalf("the service run: %v", err)
	}
	close(release)

	h.waitUpdate(t, "the run's ended update", followedUpdateWith(followedRunEnded))
	var ids []string
	for _, u := range h.recorded() {
		if u["kind"] != "tool_call" {
			continue
		}
		if id, ok := u["toolCallId"].(string); ok {
			ids = append(ids, id)
		}
	}
	if fmt.Sprint(ids) != fmt.Sprint([]string{"call-1", "call-2"}) {
		t.Fatalf("the run's tool calls = %v, want [call-1 call-2]", ids)
	}
}

// TestAShutdownEndsTheRunItWasFollowing pins that a runtime going away while a
// run is followed reports "ended": a bridge showing that row as running must be
// able to clear it, and a follower that simply vanished would leave it running.
func TestAShutdownEndsTheRunItWasFollowing(t *testing.T) {
	h := newFollowHarness(t, &recordingPermissionClient{decision: PermissionDecision{Approved: true}})
	h.follow(t)

	release := make(chan struct{})
	h.runner.setBody(func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	done := h.startRun(t)
	started := h.waitUpdate(t, "the run's started update", followedUpdateWith(followedRunStarted))

	// The run is still in flight when the runtime goes away: nothing but the
	// follower can tell the bridge that this run is over.
	h.state.Shutdown()

	ended := h.waitUpdate(t, "the run's ended update", followedUpdateWith(followedRunEnded))
	if got, want := ended["runId"], started["runId"]; got != want {
		t.Fatalf("ended names run %v, want %v", got, want)
	}
	close(release)
	_ = h.waitRun(t, done, "the service run")
}

// TestFollowNamesWhatItCannotFollow pins Follow's error contract: an unknown
// session and a session nothing can run are reported differently, because only
// one of them is the caller's mistake.
func TestFollowNamesWhatItCannotFollow(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{SessionID: followSession})
	tm := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			if id != followSession {
				return nil, false
			}
			return &TurnRuntime{
				SessionID: id,
				State:     state,
				Events:    pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify:  func(string, any) error { return nil },
		Perms:   &recordingPermissionClient{decision: PermissionDecision{Approved: true}},
		Service: func(string) (*service.Session, bool) { return nil, false },
	})
	t.Cleanup(tm.CloseFollowers)

	cases := []struct {
		name, sessionID, want string
	}{
		{"unknown session", "nope", "session nope not found"},
		{"no run service", followSession, "session " + followSession + " has no run service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tm.Follow(context.Background(), json.RawMessage(`{"sessionId":"`+tc.sessionID+`"}`))
			if err == nil {
				t.Fatalf("Follow(%q) succeeded", tc.sessionID)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Follow(%q) = %q, want %q", tc.sessionID, err.Error(), tc.want)
			}
		})
	}
}

// TestAReplacedRuntimeGetsItsOwnFollower pins ensureFollower's replacement
// branch: a new State for the same session stops the old follower and starts a
// fresh one, so the events forwarded belong to the runtime that is live.
func TestAReplacedRuntimeGetsItsOwnFollower(t *testing.T) {
	oldState := session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{SessionID: followSession})
	newState := session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{SessionID: followSession})
	_, served := service.New(service.Deps{
		State:  oldState,
		Runner: func() service.Runner { return newScriptedFollowRunner() },
	})

	var mu sync.Mutex
	live := &TurnRuntime{
		SessionID: followSession,
		State:     oldState,
		Events:    pubsub.NewBroker[session.Event](),
	}
	tm := NewTurnManager(TurnManagerConfig{
		Lookup: func(id string) (*TurnRuntime, bool) {
			mu.Lock()
			defer mu.Unlock()
			if id != followSession {
				return nil, false
			}
			return live, true
		},
		Notify:  func(string, any) error { return nil },
		Perms:   &recordingPermissionClient{decision: PermissionDecision{Approved: true}},
		Service: func(id string) (*service.Session, bool) { return served, id == followSession },
	})
	t.Cleanup(tm.CloseFollowers)

	params := json.RawMessage(`{"sessionId":"` + followSession + `"}`)
	if _, err := tm.Follow(context.Background(), params); err != nil {
		t.Fatalf("session/follow: %v", err)
	}
	before := tm.followerFor(followSession)
	if before == nil {
		t.Fatal("Follow started no follower")
	}

	mu.Lock()
	live = &TurnRuntime{
		SessionID: followSession,
		State:     newState,
		Events:    pubsub.NewBroker[session.Event](),
	}
	mu.Unlock()
	if _, err := tm.Follow(context.Background(), params); err != nil {
		t.Fatalf("session/follow after the runtime was replaced: %v", err)
	}

	after := tm.followerFor(followSession)
	if after == before {
		t.Fatal("the replaced runtime kept the old follower")
	}
	if before.ctx.Err() == nil {
		t.Fatal("the replaced runtime's follower is still running")
	}
	if after.ctx.Err() != nil {
		t.Fatal("the new runtime's follower is already stopped")
	}
}
