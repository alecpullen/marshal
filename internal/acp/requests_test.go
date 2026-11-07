package acp

import (
	"context"
	"encoding/json"
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
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// requestSession is the session every request test serves.
const requestSession = "s1"

// requestAgent is the agent end of a request test: a service over one state,
// the connection's request manager, the barrier it feeds, and the
// pending_requests updates the client recorded.
type requestAgent struct {
	requests *RequestManager
	state    *session.State
	barrier  func(ctx context.Context, sessionID string)

	// lookup is what the request manager's Lookup reports for the session.
	lookup atomic.Bool

	mu      sync.Mutex
	updates []acpwire.PendingState
}

// record keeps one received pending_requests update.
func (a *requestAgent) record(p acpwire.PendingState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updates = append(a.updates, p)
}

// recorded returns the updates received so far, in arrival order.
func (a *requestAgent) recorded() []acpwire.PendingState {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]acpwire.PendingState, len(a.updates))
	copy(out, a.updates)
	return out
}

// count is how many updates the client has received.
func (a *requestAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.updates)
}

// notifyClient decodes a notification the agent sent, keeping each
// pending_requests update.
func (a *requestAgent) notifyClient(method string, params json.RawMessage) {
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
	var peek struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(env.Update, &peek); err != nil {
		return
	}
	if peek.Kind != acpwire.UpdatePending {
		return
	}
	var u acpwire.PendingUpdate
	if err := json.Unmarshal(env.Update, &u); err != nil {
		return
	}
	a.record(u.Pending)
}

// startRequestAgent serves the request handlers on the agent end of a pipe and
// returns a client connection that has completed initialize. A nonzero delay
// slows every session/update frame, so a call that returns before its update is
// written shows up as an ordering failure.
func startRequestAgent(t *testing.T, delay time.Duration, marshal bool) (*rpc.Conn, *requestAgent) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: requestSession})
	return startRequestAgentDeps(t, delay, marshal, service.Deps{State: state})
}

// startRequestAgentDeps is startRequestAgent over explicit service deps, for
// the cases that need the runtime's access seam.
func startRequestAgentDeps(t *testing.T, delay time.Duration, marshal bool, deps service.Deps) (*rpc.Conn, *requestAgent) {
	t.Helper()

	_, served := service.New(deps)
	a := &requestAgent{state: deps.State}
	a.lookup.Store(true)

	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes

	notify := func(method string, params any) error {
		if delay > 0 && method == acpwire.MethodSessionUpdate {
			time.Sleep(delay)
		}
		return srv.Notify(method, params)
	}
	registerInitialize(srv)
	a.requests = NewRequestManager(RequestManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			if id != requestSession || !a.lookup.Load() {
				return nil, false
			}
			return served, true
		},
		Notify:  notify,
		Marshal: srv.marshalClient,
	})
	registerRequests(srv, a.requests)
	a.barrier = responseBarrier(a.requests)
	srv.SetResponseBarrier(a.barrier)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()

	conn := rpc.NewConn(client, rpc.Handlers{Notify: a.notifyClient})

	t.Cleanup(func() {
		_ = conn.Close()
		a.requests.Close()
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
	if marshal {
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

// waitForRequest polls cond until it reports true or timeout expires.
func waitForRequest(t *testing.T, timeout time.Duration, what string, cond func() bool) {
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

// readPending calls session/pending and returns its result.
func readPending(t *testing.T, conn *rpc.Conn) acpwire.PendingState {
	t.Helper()
	var got acpwire.PendingState
	if err := conn.Call(context.Background(), acpwire.MethodPending, acpwire.PendingParams{SessionID: requestSession}, &got); err != nil {
		t.Fatalf("session/pending: %v", err)
	}
	return got
}

// testApprovalChan is a buffered response channel of the width a registration
// needs, so settling one never blocks the test.
func testApprovalChan() chan session.UserApprovalDecision {
	return make(chan session.UserApprovalDecision, 1)
}

// registerTestApproval registers one ordinary approval on the state and returns
// its key.
func registerTestApproval(t *testing.T, state *session.State) string {
	t.Helper()
	key, err := state.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		Reason:       "policy: ask",
		ResponseChan: testApprovalChan(),
	})
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}
	return key.ID
}

// TestTheFirstPendingArrivesBeforeTheResponse pins that session/pending's first
// call subscribes and that the state it returns is the one already sent.
func TestTheFirstPendingArrivesBeforeTheResponse(t *testing.T) {
	conn, a := startRequestAgent(t, 0, true)
	key := registerTestApproval(t, a.state)

	got := readPending(t, conn)

	updates := a.recorded()
	if len(updates) != 1 {
		t.Fatalf("recorded %d updates before the response, want exactly 1", len(updates))
	}
	if !reflect.DeepEqual(updates[0], got) {
		t.Errorf("the update = %+v, want the call's result %+v", updates[0], got)
	}
	if len(got.Requests.Approvals) != 1 || got.Requests.Approvals[0].Key != key {
		t.Errorf("Requests.Approvals = %+v, want the approval with key %q", got.Requests.Approvals, key)
	}
}

// TestANewRequestIsSentWithinTheInterval pins the forwarder's cadence: a request
// that opens after the first read reaches the client by the next tick, and a
// quiet session sends nothing.
func TestANewRequestIsSentWithinTheInterval(t *testing.T) {
	conn, a := startRequestAgent(t, 0, true)
	readPending(t, conn)

	key := registerTestApproval(t, a.state)

	waitForRequest(t, 500*time.Millisecond, "the new approval to arrive", func() bool {
		for _, u := range a.recorded() {
			for _, ap := range u.Requests.Approvals {
				if ap.Key == key {
					return true
				}
			}
		}
		return false
	})

	// Three quiet intervals send nothing: the encoding is unchanged.
	before := a.count()
	time.Sleep(3 * requestInterval)
	if after := a.count(); after != before {
		t.Errorf("a quiet session sent %d more updates, want none", after-before)
	}
}

// TestAnAnswerReturnsAfterItsUpdate is V3 for each request kind: with every
// session/update frame slowed, the answer's response follows the update that
// removes the request.
func TestAnAnswerReturnsAfterItsUpdate(t *testing.T) {
	const delay = 50 * time.Millisecond
	cases := []struct {
		name  string
		setup func(t *testing.T, state *session.State, conn *rpc.Conn) (listed func(acpwire.PendingState) bool, answer func() error)
	}{
		{
			name: "an approval",
			setup: func(t *testing.T, state *session.State, conn *rpc.Conn) (func(acpwire.PendingState) bool, func() error) {
				key := registerTestApproval(t, state)
				return func(s acpwire.PendingState) bool {
						return len(s.Requests.Approvals) == 1 && s.Requests.Approvals[0].Key == key
					}, func() error {
						return conn.Call(context.Background(), acpwire.MethodAnswerApproval, acpwire.AnswerApprovalParams{
							SessionID: requestSession,
							Key:       key,
							Answer:    acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce},
						}, nil)
					}
			},
		},
		{
			name: "a question",
			setup: func(t *testing.T, state *session.State, conn *rpc.Conn) (func(acpwire.PendingState) bool, func() error) {
				key, err := state.RegisterQuestion(context.Background(), &session.PendingQuestion{
					Questions:    []session.Question{{Question: "Which?"}},
					ResponseChan: make(chan []session.Answer, 1),
				})
				if err != nil {
					t.Fatalf("RegisterQuestion: %v", err)
				}
				return func(s acpwire.PendingState) bool {
						return s.Requests.Question != nil && s.Requests.Question.Key == key.ID
					}, func() error {
						return conn.Call(context.Background(), acpwire.MethodAnswerQuestion, acpwire.AnswerQuestionParams{
							SessionID: requestSession,
							Key:       key.ID,
							Answers:   []acpwire.Answer{{Question: "Which?", Answer: "A"}},
						}, nil)
					}
			},
		},
		{
			name: "a skill gate",
			setup: func(t *testing.T, state *session.State, conn *rpc.Conn) (func(acpwire.PendingState) bool, func() error) {
				key, err := state.RegisterSkillGate(context.Background(), &session.PendingSkillGate{
					Skill:        "tdd",
					ResponseChan: make(chan session.SkillGateChoice, 1),
				})
				if err != nil {
					t.Fatalf("RegisterSkillGate: %v", err)
				}
				return func(s acpwire.PendingState) bool {
						return s.Requests.SkillGate != nil && s.Requests.SkillGate.Key == key.ID
					}, func() error {
						return conn.Call(context.Background(), acpwire.MethodAnswerSkillGate, acpwire.AnswerSkillGateParams{
							SessionID: requestSession,
							Key:       key.ID,
							Choice:    acpwire.SkillGateAllowOnce,
						}, nil)
					}
			},
		},
		{
			name: "a sudo prompt",
			setup: func(t *testing.T, state *session.State, conn *rpc.Conn) (func(acpwire.PendingState) bool, func() error) {
				key, err := state.RegisterSudoAuth(context.Background(), &session.PendingSudoAuth{
					Command:      "apt install jq",
					ResponseChan: make(chan session.SudoAuthDecision, 1),
				})
				if err != nil {
					t.Fatalf("RegisterSudoAuth: %v", err)
				}
				return func(s acpwire.PendingState) bool {
						return s.Requests.Sudo != nil && s.Requests.Sudo.Key == key.ID
					}, func() error {
						return conn.Call(context.Background(), acpwire.MethodAnswerSudo, acpwire.AnswerSudoParams{
							SessionID: requestSession,
							Key:       key.ID,
							Answer:    acpwire.SudoAnswer{OK: true, Password: []byte("pw")},
						}, nil)
					}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, a := startRequestAgent(t, delay, true)
			listed, answer := tc.setup(t, a.state, conn)
			readPending(t, conn)
			waitForRequest(t, 2*time.Second, "the request to be listed", func() bool {
				for _, u := range a.recorded() {
					if listed(u) {
						return true
					}
				}
				return false
			})

			if err := answer(); err != nil {
				t.Fatalf("answer: %v", err)
			}

			// The call's response arrived after the update that removes it.
			updates := a.recorded()
			if len(updates) == 0 {
				t.Fatal("no update was recorded")
			}
			if last := updates[len(updates)-1]; listed(last) {
				t.Errorf("the last update before the response still lists the request: %+v", last)
			}
		})
	}
}

// TestAnAnswerToNothingOpenIsRequestSettled pins that the service's code
// travels as error data.
func TestAnAnswerToNothingOpenIsRequestSettled(t *testing.T) {
	conn, _ := startRequestAgent(t, 0, true)
	err := conn.Call(context.Background(), acpwire.MethodAnswerApproval, acpwire.AnswerApprovalParams{
		SessionID: requestSession,
		Key:       "nope",
		Answer:    acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce},
	}, nil)
	if err == nil {
		t.Fatal("the answer succeeded, want CodeRequestSettled")
	}
	if !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Fatalf("err = %v, want an error carrying CodeRequestSettled", err)
	}
}

// TestASatisfiedAccessRequestIsSettledByTheForwarder pins that a pass settles a
// request that asks for nothing, through the runtime's own reader, and presents
// only one that still asks.
func TestASatisfiedAccessRequestIsSettledByTheForwarder(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: requestSession})
	conn, a := startRequestAgentDeps(t, 0, true, service.Deps{
		State: state,
		AccessRequest: func() (session.AccessRequest, bool) {
			// The runtime's reader settles a request that asks for nothing:
			// this state grants nothing, so it answers none.
			req, ok := state.AccessRequest()
			if !ok {
				return session.AccessRequest{}, false
			}
			state.CancelAccess(req.RequestID, session.AccessCanceled, "")
			return state.AccessRequest()
		},
		ResolveAccess: func(session.AccessChoice) (session.AccessResult, error) {
			return session.AccessResult{}, nil
		},
	})
	if _, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: testApprovalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"}); err != nil || !waiting {
		t.Fatalf("RegisterAccess = (waiting %v, err %v), want a published request", waiting, err)
	}

	got := readPending(t, conn)
	if got.Access != nil {
		t.Errorf("the update presents %+v, want none: the reader settled it", got.Access)
	}
	if _, open := state.AccessRequest(); open {
		t.Error("the state's access request is still open, want it settled by the pass")
	}
	if len(got.Requests.Approvals) != 0 {
		t.Errorf("Requests.Approvals = %+v, want the settled access request gone", got.Requests.Approvals)
	}
	_ = a
}

// TestAClientWithoutTheCapabilityGetsNoPendingUpdates is V6: the calls work and
// nothing is pushed.
func TestAClientWithoutTheCapabilityGetsNoPendingUpdates(t *testing.T) {
	conn, a := startRequestAgent(t, 0, false)
	key := registerTestApproval(t, a.state)

	got := readPending(t, conn)
	if len(got.Requests.Approvals) != 1 || got.Requests.Approvals[0].Key != key {
		t.Fatalf("Requests.Approvals = %+v, want the approval with key %q", got.Requests.Approvals, key)
	}
	if err := conn.Call(context.Background(), acpwire.MethodAnswerApproval, acpwire.AnswerApprovalParams{
		SessionID: requestSession,
		Key:       key,
		Answer:    acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce},
	}, nil); err != nil {
		t.Fatalf("answer_approval: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if n := a.count(); n != 0 {
		t.Fatalf("a client without the capability received %d pending_requests updates, want none", n)
	}
}

// TestRequestsRefuseBadParams pins the two refusals every method makes.
func TestRequestsRefuseBadParams(t *testing.T) {
	conn, _ := startRequestAgent(t, 0, true)
	ctx := context.Background()
	methods := []string{
		acpwire.MethodPending,
		acpwire.MethodAnswerApproval,
		acpwire.MethodAnswerQuestion,
		acpwire.MethodAnswerSkillGate,
		acpwire.MethodAnswerSudo,
		acpwire.MethodAnswerAccess,
		acpwire.MethodRollback,
	}
	for _, method := range methods {
		err := conn.Call(ctx, method, map[string]any{}, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid params") {
			t.Errorf("%s with no sessionId = %v, want invalid params", method, err)
		}
		err = conn.Call(ctx, method, map[string]any{"sessionId": "nope"}, nil)
		if err == nil || !strings.Contains(err.Error(), "unknown session") {
			t.Errorf("%s for an unknown session = %v, want the unknown-session error", method, err)
		}
	}
}

// TestTheRequestForwarderStopsWhenTheSessionGoes pins that a vanished session
// ends its forwarder and its fence, so a barrier answers at once.
func TestTheRequestForwarderStopsWhenTheSessionGoes(t *testing.T) {
	conn, a := startRequestAgent(t, 0, true)
	readPending(t, conn)

	a.lookup.Store(false)

	waitForRequest(t, 500*time.Millisecond, "the forwarder to stop", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		a.barrier(ctx, requestSession)
		return ctx.Err() == nil
	})
}
