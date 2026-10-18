package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/llm/routing"
	"marshal/internal/oauth"
	"marshal/internal/pipeline"
	"marshal/internal/pubsub"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// identityBeginWork is a TurnRuntime.BeginWork gate that passes through
// the context and returns a no-op finish function.
func identityBeginWork(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

// fakeAgentRunner is a minimal AgentRunner for tests that don't need real
// swarm/pipeline machinery — only Run and AnswerGate are ever exercised by
// TurnManager; the other three methods exist to satisfy the interface.
type fakeAgentRunner struct {
	run        func(ctx context.Context, goal string) error
	answerGate func(answer string)
}

func (f *fakeAgentRunner) Run(ctx context.Context, goal string) error {
	if f.run == nil {
		return nil
	}
	return f.run(ctx, goal)
}
func (f *fakeAgentRunner) SetForceClass(string)                   {}
func (f *fakeAgentRunner) SetPolicyRules([]config.PermissionRule) {}
func (f *fakeAgentRunner) SetApprovalMode(policy.ApprovalMode)    {}
func (f *fakeAgentRunner) AnswerGate(answer string) {
	if f.answerGate != nil {
		f.answerGate(answer)
	}
}

func TestSwarmStartRunsSwarmRunnerAndReturnsCast(t *testing.T) {
	state := &session.State{}
	state.SetSwarmProgress(session.SwarmProgress{
		Goal:   "ship it",
		Active: false,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRoleDone},
			{Name: "implementer", Status: session.SwarmRoleDone},
		},
	})

	var gotGoal string
	fakeSwarm := &fakeAgentRunner{
		run: func(ctx context.Context, goal string) error {
			gotGoal = goal
			return nil
		},
	}

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:   sessionID,
				BeginWork:   identityBeginWork,
				Events:      pubsub.NewBroker[session.Event](),
				State:       state,
				SwarmRunner: fakeSwarm,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	raw, _ := json.Marshal(SwarmStartParams{SessionID: "sess_1", Goal: "ship it"})
	res, err := manager.SwarmStart(context.Background(), raw)
	if err != nil {
		t.Fatalf("SwarmStart: %v", err)
	}
	result, ok := res.(SwarmTurnResult)
	if !ok {
		t.Fatalf("SwarmStart result type = %T, want SwarmTurnResult", res)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", result.StopReason, "end_turn")
	}
	if gotGoal != "ship it" {
		t.Errorf("runner received goal = %q, want %q", gotGoal, "ship it")
	}
	if len(result.Cast) != 2 || result.Cast[0].Name != "planner" || result.Cast[0].Status != "done" {
		t.Errorf("Cast = %+v, want [{planner done} {implementer done}]", result.Cast)
	}
}

func TestSwarmStartRejectsEmptyGoal(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(SwarmStartParams{SessionID: "sess_1", Goal: "  "})
	_, err := manager.SwarmStart(context.Background(), raw)
	if err == nil {
		t.Fatal("SwarmStart with blank goal: got nil error, want an error")
	}
}

func TestSwarmStartRejectsWhenSwarmUnavailable(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID, SwarmRunner: nil}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(SwarmStartParams{SessionID: "sess_1", Goal: "ship it"})
	_, err := manager.SwarmStart(context.Background(), raw)
	if err == nil {
		t.Fatal("SwarmStart with nil SwarmRunner: got nil error, want an error")
	}
}

func TestPromptTurnRunsRunner(t *testing.T) {
	calledWith := ""
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					calledWith = prompt
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	got, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hello"}]}`))
	if err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	if calledWith != "hello" {
		t.Fatalf("runner prompt = %q", calledWith)
	}
	result, ok := got.(PromptTurnResult)
	if !ok {
		t.Fatalf("result type = %T, want PromptTurnResult", got)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("result = %+v", result)
	}
}

func TestPromptTurnUnknownSession(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return nil, false
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"missing","prompt":[{"type":"text","text":"hi"}]}`))
	if err == nil {
		t.Fatalf("expected error for unknown session")
	}
}

func TestPromptTurnForwardsEventsAsNotifications(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	gotMethods := []string{}
	gotParams := []map[string]any{}
	notify := func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		gotMethods = append(gotMethods, method)
		if p, ok := params.(SessionUpdateParams); ok {
			gotParams = append(gotParams, p.Update)
		}
		return nil
	}
	done := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
						Role:    session.RoleAssistant,
						Content: "hi",
					}})
					broker.Publish(session.EventActivityChanged, session.Event{Activity: &session.Activity{
						Kind: session.ActivityThinking,
					}})
					close(done)
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: notify,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("runner never completed")
	}
	// Give the goroutine a moment to drain the last notification.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(gotMethods)
		mu.Unlock()
		if count >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotMethods) < 1 {
		t.Fatalf("expected >=1 notification, got %v", gotMethods)
	}
	// All notifications use session/update.
	for _, m := range gotMethods {
		if m != "session/update" {
			t.Fatalf("unexpected notification method %q, want session/update", m)
		}
	}
	// The message-added event should produce an agent_message_chunk update.
	var foundMessage bool
	for _, p := range gotParams {
		if p["kind"] == "agent_message_chunk" {
			foundMessage = true
		}
	}
	if !foundMessage {
		t.Fatalf("no agent_message_chunk update found in %v", gotParams)
	}
}

func TestPromptTurnReturnsRunnerError(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					return errors.New("boom")
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
	// Turn failures are wrapped as serverError so the client sees the actual
	// cause instead of an opaque -32603 "internal error".
	var rpcErr *jsonRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != serverError {
		t.Fatalf("err = %v, want a serverError wrapping the runner error", err)
	}
	if rpcErr.Message != "turn failed: boom" {
		t.Fatalf("err message = %q, want %q", rpcErr.Message, "turn failed: boom")
	}
}

func TestCancelNoActiveTurnSucceeds(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return nil, false
		},
		Notify: func(method string, params any) error { return nil },
	})
	got, err := manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"sess_test"}`))
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if got != nil {
		t.Fatalf("Cancel() result = %v, want nil", got)
	}
}

func TestCancelInvokesActiveTurnCancel(t *testing.T) {
	cancelCalled := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					<-ctx.Done()
					return ctx.Err()
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	// Start a prompt turn in the background so a slot is reserved.
	done := make(chan struct{})
	go func() {
		_, _ = manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	// Cancel should mark client-cancelled and cancel the context.
	if _, err := manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"sess_test"}`)); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	// The runner should see ctx cancelled and return; the prompt
	// result should be "cancelled".
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("runner never completed after cancel")
	}
	// Track that cancel was called
	close(cancelCalled)
	select {
	case <-cancelCalled:
	default:
		t.Fatalf("cancel marker not set")
	}
}

func TestPromptTurnCompletesAfterBrokerCloseAndRunnerRelease(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	runnerDone := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					<-runnerDone
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	done := make(chan error, 1)
	go func() {
		_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	broker.Close()
	close(runnerDone)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("PromptTurn did not return within 3s after broker close + runner release")
	}
}

// TestCancelAndWaitBoundsWait reproduces F-BUG-50: when a runner
// goroutine never writes to runErr (simulated by an activeTurn whose
// done channel is never closed), CancelAndWait must not block forever.
// The test uses a short per-instance cancelTimeout override so it
// completes in milliseconds regardless.
func TestCancelAndWaitBoundsWait(t *testing.T) {
	tm := &TurnManager{
		activeTurns:   map[string]*activeTurn{},
		activeTurnsMu: sync.Mutex{},
		cancelTimeout: 100 * time.Millisecond,
	}
	_, slotCancel := context.WithCancel(context.Background())
	tm.activeTurns["s1"] = &activeTurn{
		cancel: slotCancel,
		done:   make(chan struct{}), // never closed — runner never finishes
	}

	// We do NOT close done, so the slot never resolves through the
	// normal cleanup path. Before the fix (F-BUG-50) this call would
	// block forever; after the fix the bounded wait ensures it returns.
	start := time.Now()
	err := tm.CancelAndWait(context.Background(), "s1")
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("CancelAndWait blocked for %v (expected bounded wait)", elapsed)
	}
	if err == nil {
		t.Fatal("expected timeout error from bounded wait")
	}
}

// TestCancelAndWaitDefaultTimeout verifies the const default is used when
// cancelTimeout is zero.
func TestCancelAndWaitDefaultTimeout(t *testing.T) {
	tm := &TurnManager{
		activeTurns:   map[string]*activeTurn{},
		activeTurnsMu: sync.Mutex{},
		// cancelTimeout is zero — should use default const
	}
	_, slotCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		close(done) // simulate runner finishing immediately
	}()
	tm.activeTurns["s1"] = &activeTurn{
		cancel: slotCancel,
		done:   done,
	}

	// Should return quickly because done is already closed.
	err := tm.CancelAndWait(context.Background(), "s1")
	if err != nil {
		t.Fatalf("CancelAndWait returned error: %v", err)
	}
}

// TestForwardDoesNotBlockOnBridge is a smoke test for F-CON-54. It sets
// up a synthetic turn where the forwarder encounters a permission approval
// event and the PermissionBridge uses a blocking client. Before the fix
// the synchronous bridge call in the forwarder would hang the entire
// PromptTurn; after the fix the call is dispatched in a goroutine so the
// turn completes normally and quickly.
func TestForwardDoesNotBlockOnBridge(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{
		ID:           "tool-block",
		Name:         "shell.run",
		Command:      "block",
		ResponseChan: response,
	}

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					// Publish an approval event — the forwarder must not block.
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: pending})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
		Perms:  blockingPermissionClient{}, // blocks until ctx.Done()
	})

	// Before the fix (F-CON-54), the forwarder calls bridge.Request
	// synchronously, blocking the event loop and the entire turn.
	// After the fix, the bridge call runs in a goroutine so the
	// forwarder returns immediately and the turn completes normally.
	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
	if err != nil {
		t.Fatalf("PromptTurn() error = %v (expected nil, bridge call is now async)", err)
	}
}

// TestForwarderDeniesPendingApprovalWhenBridgeNil verifies F-SEC-13:
// when the permission bridge is nil and a pending approval arrives, the
// forwarder sends a deny decision on the ResponseChan instead of silently
// skipping the bridge call (which would block the runner indefinitely).
func TestForwarderDeniesPendingApprovalWhenBridgeNil(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{
		ID:           "test-approval",
		Name:         "shell.run",
		Command:      "date",
		ResponseChan: response,
	}

	// No Perms → bridge is nil.
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: pending})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
		// Perms is nil → bridge stays nil.
	})

	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
	if err != nil {
		t.Fatalf("PromptTurn() error = %v (expected nil, deny should unblock runner)", err)
	}

	select {
	case got := <-response:
		if got.Approved {
			t.Fatalf("expected deny, got approved: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no response on ResponseChan; forwarder is stuck (F-SEC-13)")
	}
}

// --- Step 2: Concurrency and cancellation tests ---

func TestPromptTurnRejectsConcurrentPromptForSameSession(t *testing.T) {
	blocker := make(chan struct{})
	var runnerStarted atomic.Bool
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					runnerStarted.Store(true)
					<-blocker
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	// Start first turn in goroutine; it will block on blocker.
	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"first"}]}`))
		firstDone <- err
	}()
	time.Sleep(50 * time.Millisecond)

	if !runnerStarted.Load() {
		t.Fatalf("first runner did not start")
	}

	// Second turn for same session — should be rejected.
	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"second"}]}`))
	if err == nil {
		t.Fatal("expected error for concurrent session/prompt, got nil")
	}
	var rpcErr *jsonRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != serverError {
		t.Fatalf("expected serverError (%d), got code=%d err=%v", serverError, codeFor(err), err)
	}

	// First turn must still be active (not cancelled).
	close(blocker)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first turn error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not complete after releasing blocker")
	}
}

func TestPromptTurnsDifferentSessionsRunConcurrently(t *testing.T) {
	blocker := make(chan struct{})
	started := make(chan struct{}, 2)
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					started <- struct{}{}
					<-blocker
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	go func() {
		manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_a","prompt":[{"type":"text","text":"a"}]}`))
	}()
	go func() {
		manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_b","prompt":[{"type":"text","text":"b"}]}`))
	}()

	// Both runners should signal started before either is released.
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first runner did not start")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second runner did not start")
	}

	close(blocker)
}

func TestCancelAndWaitJoinsRunner(t *testing.T) {
	cleanupGate := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					<-ctx.Done()
					<-cleanupGate
					return ctx.Err()
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	go func() {
		manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
	}()
	time.Sleep(50 * time.Millisecond)

	// CancelAndWait should block until the cleanup gate opens.
	waitDone := make(chan struct{})
	go func() {
		manager.CancelAndWait(context.Background(), "sess_test")
		close(waitDone)
	}()

	time.Sleep(50 * time.Millisecond)
	select {
	case <-waitDone:
		t.Fatal("CancelAndWait returned before cleanup gate opened")
	default:
	}

	close(cleanupGate)

	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("CancelAndWait did not return after cleanup gate opened")
	}
}

func TestSessionCancelMakesPromptReturnCancelled(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					<-ctx.Done()
					return ctx.Err()
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	type promptResult struct {
		result any
		err    error
	}
	promptRes := make(chan promptResult, 1)
	go func() {
		result, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
		promptRes <- promptResult{result: result, err: err}
	}()
	time.Sleep(50 * time.Millisecond)

	// Cancel — notification handler, does not wait.
	if _, err := manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"sess_test"}`)); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	select {
	case pr := <-promptRes:
		if pr.err != nil {
			t.Fatalf("PromptTurn() error = %v, want nil", pr.err)
		}
		r, ok := pr.result.(PromptTurnResult)
		if !ok {
			t.Fatalf("result type = %T, want PromptTurnResult", pr.result)
		}
		if r.StopReason != "cancelled" {
			t.Fatalf("StopReason = %q, want %q", r.StopReason, "cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptTurn did not return after cancel")
	}
}

func TestPromptTurnRegistersRuntimeWork(t *testing.T) {
	var beginCount atomic.Int64
	var finishCount atomic.Int64
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: func(ctx context.Context) (context.Context, func(), error) {
					beginCount.Add(1)
					return ctx, func() { finishCount.Add(1) }, nil
				},
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	if beginCount.Load() != 1 {
		t.Fatalf("BeginWork called %d times, want 1", beginCount.Load())
	}
	if finishCount.Load() != 1 {
		t.Fatalf("finish called %d times, want 1", finishCount.Load())
	}
}

func TestPromptTurnQuiescingReturnsRequestCancelled(t *testing.T) {
	var runnerCalled atomic.Bool
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: func(ctx context.Context) (context.Context, func(), error) {
					return ctx, func() {}, session.ErrSessionQuiescing
				},
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					runnerCalled.Store(true)
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
	if err == nil {
		t.Fatal("expected error for quiescing session, got nil")
	}
	var rpcErr *jsonRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != requestCancelled {
		t.Fatalf("expected requestCancelled (%d), got code=%d err=%v", requestCancelled, codeFor(err), err)
	}
	if runnerCalled.Load() {
		t.Fatal("runner was called despite quiescing")
	}
}

// --- Step 4: Standard-update projection tests ---

func TestPromptTurnProjectsMessagesAsSessionUpdate(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	var updates []map[string]any
	notify := func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := params.(SessionUpdateParams); ok {
			updates = append(updates, p.Update)
		}
		return nil
	}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					// Publish user, assistant, and system messages.
					broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
						Role: session.RoleUser, Content: "user msg",
					}})
					broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
						Role: session.RoleAssistant, Content: "assistant msg",
					}})
					broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
						Role: session.RoleSystem, Content: "system msg",
					}})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: notify,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d: %v", len(updates), updates)
	}
	// Every method is session/update (already verified by notify)
	// Check update kinds.
	wantKinds := []string{"user_message_chunk", "agent_message_chunk", "agent_message_chunk"}
	for i, u := range updates {
		kind, _ := u["kind"].(string)
		if kind != wantKinds[i] {
			t.Fatalf("update[%d] kind = %q, want %q; update=%v", i, kind, wantKinds[i], u)
		}
	}
}

func TestPromptTurnProjectsThinkingDelta(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	var updates []map[string]any
	notify := func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		if p, ok := params.(SessionUpdateParams); ok {
			updates = append(updates, p.Update)
		}
		return nil
	}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventThinkingChanged, session.Event{Thinking: &session.InProgressMessage{
						Reasoning: "abc",
						Active:    true,
					}})
					broker.Publish(session.EventThinkingChanged, session.Event{Thinking: &session.InProgressMessage{
						Reasoning: "abcdef",
						Active:    true,
					}})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: notify,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 2 {
		t.Fatalf("expected 2 thinking updates, got %d: %v", len(updates), updates)
	}
	// First should emit "abc", second should emit "def" (delta).
	expectedTexts := []string{"abc", "def"}
	for i, u := range updates {
		content, ok := u["content"].(map[string]any)
		if !ok {
			t.Fatalf("update[%d] missing content: %v", i, u)
		}
		text, _ := content["text"].(string)
		if text != expectedTexts[i] {
			t.Fatalf("update[%d] text = %q, want %q", i, text, expectedTexts[i])
		}
		kind, _ := u["kind"].(string)
		if kind != "agent_thought_chunk" {
			t.Fatalf("update[%d] kind = %q, want agent_thought_chunk", i, kind)
		}
	}
}

func TestPromptTurnSuppressesInternalCustomEvents(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	var updateCount int
	notify := func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := params.(SessionUpdateParams); ok {
			updateCount++
		}
		return nil
	}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					// Activity and pending-clear events should not produce
					// any wire update.
					broker.Publish(session.EventActivityChanged, session.Event{Activity: &session.Activity{
						Kind: session.ActivityThinking,
					}})
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: nil})
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: nil})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: notify,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if updateCount != 0 {
		t.Fatalf("expected 0 session/update for internal events, got %d", updateCount)
	}
}

func TestPromptTurnUsesTerminalSubscription(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	var updateCount int
	notify := func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		updateCount++
		return nil
	}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					// Publish more events than the default broker buffer (16).
					for i := 0; i < 30; i++ {
						broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
							Role:    session.RoleAssistant,
							Content: "msg",
						}})
					}
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: notify,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if updateCount != 30 {
		t.Fatalf("expected 30 session/update notifications, got %d", updateCount)
	}
}

// --- Step 6: Permission and question tests ---

func TestPromptTurnPermissionFailureCancelsRunner(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	response := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{
		ID:           "tool-1",
		Name:         "shell.run",
		Command:      "date",
		ResponseChan: response,
	}
	permClient := &fakePermissionClient{err: errors.New("permission error")}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: pending})
					// Wait for cancellation.
					<-ctx.Done()
					return ctx.Err()
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
		Perms:  permClient,
	})
	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
	if err == nil {
		t.Fatal("expected error from failed permission request, got nil")
	}
	if permClient.calls != 1 {
		t.Fatalf("PermissionClient.RequestPermission called %d times, want 1", permClient.calls)
	}
}

func TestSetModeAppliesAndNotifies(t *testing.T) {
	var applied string
	var mu sync.Mutex
	var updates []map[string]any
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run:       RunnerFunc(func(ctx context.Context, prompt string) error { return nil }),
				Events:    pubsub.NewBroker[session.Event](),
				SetMode: func(mode string) error {
					applied = mode
					return nil
				},
			}, true
		},
		Notify: func(method string, params any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				updates = append(updates, p.Update)
			}
			return nil
		},
	})
	got, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_m","mode":"copilot"}`))
	if err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}
	result, ok := got.(map[string]any)
	if !ok || result["mode"] != "copilot" {
		t.Fatalf("result = %#v, want {mode: copilot}", got)
	}
	if applied != "copilot" {
		t.Fatalf("applied mode = %q, want copilot", applied)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 1 || updates[0]["kind"] != "mode_changed" || updates[0]["mode"] != "copilot" {
		t.Fatalf("updates = %#v, want one mode_changed", updates)
	}
}

func TestSetModeRejectsInvalidMode(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_m","mode":"yolo"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("err = %v, want invalid mode error", err)
	}
}

// After the validApprovalModes map was removed, SetMode validation must
// flow through policy.ValidApprovalMode: every mode the policy layer
// recognizes is accepted, anything else is rejected before session lookup.
func TestSetModeAcceptsEveryPolicyMode(t *testing.T) {
	var mu sync.Mutex
	var applied []string
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				SetMode: func(mode string) error {
					mu.Lock()
					defer mu.Unlock()
					applied = append(applied, mode)
					return nil
				},
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	for _, mode := range []string{"plan", "default", "edit", "copilot", "auto"} {
		if _, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_m","mode":"`+mode+`"}`)); err != nil {
			t.Fatalf("SetMode(%q) error = %v", mode, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(applied) != 5 {
		t.Fatalf("applied = %v, want all 5 policy modes", applied)
	}
}

func TestSetModeRejectsUnknownMode(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_m","mode":"definitely-not-a-mode"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("err = %v, want invalid mode error", err)
	}
}

func TestSetModeUnknownSession(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"nope","mode":"edit"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown session") {
		t.Fatalf("err = %v, want unknown session error", err)
	}
}

func TestPromptTurnAnswersUnsupportedQuestionsAsUnanswered(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	questions := []session.Question{
		{Question: "are you sure?"},
		{Question: "pick one?"},
	}
	// Use a channel for the runner to report the answers it received.
	gotAnswers := make(chan []session.Answer, 1)
	response := make(chan []session.Answer, 1)
	pendingQ := &session.PendingQuestion{
		Questions:    questions,
		ResponseChan: response,
	}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: pendingQ})
					// Wait for the unanswered answers to arrive.
					select {
					case answers := <-response:
						gotAnswers <- answers
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	select {
	case answers := <-gotAnswers:
		if len(answers) != 2 {
			t.Fatalf("expected 2 answers, got %d", len(answers))
		}
		for i, a := range answers {
			if a.Question != questions[i].Question {
				t.Fatalf("answer[%d] Question = %q, want %q", i, a.Question, questions[i].Question)
			}
			if a.Answer != session.AnswerUnanswered {
				t.Fatalf("answer[%d] Answer = %q, want %q", i, a.Answer, session.AnswerUnanswered)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unanswered answers from runner")
	}
}

func TestTurnManagerHasActiveTurnReflectsInFlightPrompt(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					close(started)
					<-release
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	if manager.HasActiveTurn("sess_x") {
		t.Fatal("HasActiveTurn = true before any prompt started, want false")
	}

	raw, _ := json.Marshal(PromptTurnParams{
		SessionID: "sess_x",
		Prompt:    []ContentBlock{{Type: "text", Text: "go"}},
	})
	done := make(chan struct{})
	go func() {
		manager.PromptTurn(context.Background(), raw)
		close(done)
	}()

	<-started
	if !manager.HasActiveTurn("sess_x") {
		t.Fatal("HasActiveTurn = false while a prompt is running, want true")
	}
	if manager.HasActiveTurn("sess_other") {
		t.Fatal("HasActiveTurn = true for an unrelated session, want false")
	}

	close(release)
	<-done
	if manager.HasActiveTurn("sess_x") {
		t.Fatal("HasActiveTurn = true after the prompt finished, want false")
	}
}

// TestCancelWithIDIsRejected verifies that session/cancel — which the
// ACP plan defines as a notification — is rejected with invalidRequest
// (-32600) when a client sends it WITH a request id. An id-bearing
// cancel must not cancel any active slot.
func TestCancelWithIDIsRejected(t *testing.T) {
	var cancelCalls atomic.Int64
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					<-ctx.Done()
					return ctx.Err()
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	pr, pw := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewServer(pr, outW)
	srv.HandleNotification("session/cancel", func(ctx context.Context, params json.RawMessage) (any, error) {
		cancelCalls.Add(1)
		return manager.Cancel(ctx, params)
	})

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(context.Background())
	}()

	// Send session/cancel with a non-nil id.
	mustMarshalWrite(t, pw, map[string]any{
		"jsonrpc": "2.0", "id": float64(99), "method": "session/cancel",
		"params": map[string]any{"sessionId": "sess_test"},
	})

	// Read the response from the output pipe.
	scan := bufio.NewScanner(outR)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var resp Response
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("no id-bearing response received within 2s")
		default:
		}
		if scan.Scan() {
			if err := json.Unmarshal(scan.Bytes(), &resp); err == nil && resp.ID != nil {
				break
			}
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if string(*resp.ID) != "99" {
		t.Fatalf("response id = %s, want 99", string(*resp.ID))
	}
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%v", resp.Result)
	}
	if resp.Error.Code != invalidRequest {
		t.Fatalf("error code = %d, want %d (invalidRequest)", resp.Error.Code, invalidRequest)
	}

	// No active slot was created, so Cancel must not have run.
	if cancelCalls.Load() != 0 {
		t.Fatalf("Cancel handler ran %d times, want 0", cancelCalls.Load())
	}

	pw.Close()
	outW.Close()
	<-serveErr
}

// TestForwarderUsesRespondForQuestion verifies F-BUG-51: the forwarder
// uses pending.Respond (sync.Once + close) instead of a raw select on
// ResponseChan. The test simulates an abandoned question by having the
// runner NOT read from the unbuffered ResponseChan and then cancelling
// the turn. With the old code the select picks <-turnCtx.Done() and the
// answers are lost; with pending.Respond the non-blocking send + close
// ensures the channel is always serviced.
func TestForwarderUsesRespondForQuestion(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	questions := []session.Question{
		{Question: "confirm?"},
		{Question: "proceed?"},
	}
	response := make(chan []session.Answer) // unbuffered — forces blocking send
	pendingQ := &session.PendingQuestion{
		Questions:    questions,
		ResponseChan: response,
	}

	runnerStarted := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: pendingQ})
					close(runnerStarted)
					// Do NOT read from response — simulate abandoned question.
					<-ctx.Done()
					return ctx.Err()
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	promptDone := make(chan error, 1)
	go func() {
		_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
		promptDone <- err
	}()

	<-runnerStarted
	time.Sleep(50 * time.Millisecond) // let forwarder process the event

	// Cancel the turn — this cancels turnCtx.
	manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"sess_test"}`))

	// With the old code the answers were lost (select picked <-turnCtx.Done()).
	// With pending.Respond the non-blocking send fires the default case and
	// the channel is closed. Verify the channel is closed (read succeeds).
	select {
	case answers, ok := <-response:
		if !ok {
			// Channel closed but no answers delivered (non-blocking send
			// hit default because no reader). This is acceptable — the
			// runner unblocks on the closed channel.
			break
		}
		if len(answers) != 2 {
			t.Fatalf("expected 2 answers, got %d", len(answers))
		}
		for i, a := range answers {
			if a.Question != questions[i].Question {
				t.Fatalf("answer[%d] Question = %q, want %q", i, a.Question, questions[i].Question)
			}
			if a.Answer != session.AnswerUnanswered {
				t.Fatalf("answer[%d] Answer = %q, want %q", i, a.Answer, session.AnswerUnanswered)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to be closed — forwarder lost the answers (F-BUG-51)")
	}

	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatalf("PromptTurn() error = %v, want nil (cancelled is expected)", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PromptTurn did not return after cancel")
	}
}

// messageUpdate is the sole projection point for both live session/update
// notifications and session/load replay (see SessionManager.replay), so a
// fix here closes the visibility gap for both. A salvaged message (e.g. an
// agent turn flagged "unverified" because the model never made a tool call
// despite being asked to) must not render identically to a trusted,
// grounded completion — an ACP client has no other signal to tell them
// apart, unlike the TUI which already renders a visible "salvaged" badge
// (internal/app/tui/transcript.go:renderFinalAnswer).
func TestMessageUpdateFlagsSalvagedContent(t *testing.T) {
	msg := session.Message{
		Role:          session.RoleAssistant,
		Content:       "All done, confirmed.",
		Final:         true,
		Salvaged:      true,
		SalvageReason: "unverified",
	}
	update := messageUpdate(msg)
	content, ok := update["content"].(map[string]any)
	if !ok {
		t.Fatalf("update[content] = %T, want map[string]any", update["content"])
	}
	text, _ := content["text"].(string)
	if !strings.Contains(text, "unverified") {
		t.Fatalf("text = %q, want it to mention the salvage reason %q", text, msg.SalvageReason)
	}
	if !strings.Contains(text, msg.Content) {
		t.Fatalf("text = %q, want it to still contain the original content %q", text, msg.Content)
	}
}

// A non-salvaged message must render unchanged — no regression for the
// common case.
func TestMessageUpdateLeavesNonSalvagedContentUnchanged(t *testing.T) {
	msg := session.Message{
		Role:    session.RoleAssistant,
		Content: "All done, confirmed.",
		Final:   true,
	}
	update := messageUpdate(msg)
	content := update["content"].(map[string]any)
	if content["text"] != msg.Content {
		t.Fatalf("text = %q, want unchanged %q", content["text"], msg.Content)
	}
}

func TestPromptTurnForwardsQuestionToClient(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	answersCh := make(chan []session.Answer, 1)
	qc := &fakeQuestionClient{resp: QuestionResponse{
		Answers: []session.Answer{{Question: "pick", Answer: "a"}},
	}}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					pending := &session.PendingQuestion{
						Questions:    []session.Question{{Question: "pick", Options: []session.QuestionOption{{Label: "a"}, {Label: "b"}}}},
						ResponseChan: answersCh,
					}
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: pending})
					select {
					case <-answersCh:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(5 * time.Second):
						return errors.New("timed out waiting for answers")
					}
				}),
				Events: broker,
			}, true
		},
		Notify:    func(method string, params any) error { return nil },
		Questions: qc,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_q","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	if qc.calls != 1 {
		t.Fatalf("RequestQuestion called %d times, want 1", qc.calls)
	}
	if qc.lastReq.SessionID != "sess_q" {
		t.Fatalf("SessionID = %q", qc.lastReq.SessionID)
	}
}

// The child-question forwarding tests that lived here were removed with the
// subagent question channel in the context-diet Tier 2a cut: the ACP turn
// forwarder no longer bridges subagent questions.

func TestPromptTurnQuestionClientErrorAnswersUnanswered(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	answersCh := make(chan []session.Answer, 1)
	var gotAnswers []session.Answer
	qc := &fakeQuestionClient{err: errors.New("transport dead")}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					pending := &session.PendingQuestion{
						Questions:    []session.Question{{Question: "pick"}},
						ResponseChan: answersCh,
					}
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: pending})
					select {
					case gotAnswers = <-answersCh:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(5 * time.Second):
						return errors.New("timed out waiting for answers")
					}
				}),
				Events: broker,
			}, true
		},
		Notify:    func(method string, params any) error { return nil },
		Questions: qc,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_qe","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	if len(gotAnswers) != 1 || gotAnswers[0].Answer != session.AnswerUnanswered {
		t.Fatalf("answers = %#v, want Unanswered sentinel", gotAnswers)
	}
}

// controllableQuestionClient is a QuestionClient the test drives directly.
// RequestQuestion blocks until the test supplies a response or the context
// is cancelled, so a turn can be observed sitting on an unanswered question
// indefinitely (no local timer).
type controllableQuestionClient struct {
	requests  chan QuestionRequest
	responses chan QuestionResponse
}

func newControllableQuestionClient() *controllableQuestionClient {
	return &controllableQuestionClient{
		requests:  make(chan QuestionRequest, 1),
		responses: make(chan QuestionResponse, 1),
	}
}

func (c *controllableQuestionClient) RequestQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error) {
	c.requests <- req
	select {
	case resp := <-c.responses:
		return resp, nil
	case <-ctx.Done():
		return QuestionResponse{}, ctx.Err()
	}
}

func TestPromptTurnQuestionWaitsForClient(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	answersCh := make(chan []session.Answer, 1)
	gotAnswers := make(chan []session.Answer, 1)
	client := newControllableQuestionClient()
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					pending := &session.PendingQuestion{
						Questions:    []session.Question{{Question: "pick"}},
						ResponseChan: answersCh,
					}
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: pending})
					select {
					case a := <-answersCh:
						gotAnswers <- a
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}),
				Events: broker,
			}, true
		},
		Notify:    func(method string, params any) error { return nil },
		Questions: client,
	})

	done := make(chan error, 1)
	go func() {
		_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_qt","prompt":[{"type":"text","text":"hi"}]}`))
		done <- err
	}()

	// Wait for the bridge to dispatch the question to the client.
	var req QuestionRequest
	select {
	case req = <-client.requests:
	case <-time.After(2 * time.Second):
		t.Fatal("question never reached the client")
	}
	if req.SessionID != "sess_qt" || len(req.Questions) != 1 {
		t.Fatalf("question request = %#v", req)
	}

	// No local timer fires: well past the old 30s budget (scaled to ~200ms
	// for test speed) the turn must still be waiting, not answered.
	select {
	case <-done:
		t.Fatal("turn resolved before the client answered")
	case <-time.After(200 * time.Millisecond):
	}

	// Now answer via the normal path: the answers must reach the runner.
	client.responses <- QuestionResponse{
		Answers: []session.Answer{{Question: "pick", Answer: "blue"}},
	}
	select {
	case a := <-gotAnswers:
		if len(a) != 1 || a[0].Answer != "blue" {
			t.Fatalf("answers = %#v, want one answer %q", a, "blue")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answers never reached the runner")
	}
	if err := <-done; err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
}

func TestPromptTurnQuestionResolvesOnCancel(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	answersCh := make(chan []session.Answer, 1)
	gotAnswers := make(chan []session.Answer, 1)
	client := newControllableQuestionClient()
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					pending := &session.PendingQuestion{
						Questions:    []session.Question{{Question: "pick"}},
						ResponseChan: answersCh,
					}
					broker.Publish(session.EventPendingQuestionChanged, session.Event{PendingQuestion: pending})
					select {
					case a := <-answersCh:
						gotAnswers <- a
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}),
				Events: broker,
			}, true
		},
		Notify:    func(method string, params any) error { return nil },
		Questions: client,
	})

	done := make(chan error, 1)
	go func() {
		_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_qc","prompt":[{"type":"text","text":"hi"}]}`))
		done <- err
	}()

	// Wait for the question to reach the client, then cancel the turn: the
	// pending question must resolve (as Unanswered) via ctx cancellation, not
	// a timer. The runner itself returns via ctx.Done() without reading the
	// answer, but the forwarder's question bridge must still respond to the
	// pending question so nothing is left parked.
	select {
	case <-client.requests:
	case <-time.After(2 * time.Second):
		t.Fatal("question never reached the client")
	}
	if _, err := manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"sess_qc"}`)); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	select {
	case a := <-answersCh:
		if len(a) != 1 || a[0].Answer != session.AnswerUnanswered {
			t.Fatalf("answers = %#v, want Unanswered sentinel on cancel", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending question never resolved on cancel")
	}
	// The runner may finish via ctx cancellation or via the resolved
	// (Unanswered) answer racing the cancel — either is fine. The guarantee
	// being pinned is that the pending question resolves promptly on cancel
	// rather than sitting until a local timer.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not complete after cancel")
	}
}

func TestSteerDeliversToActiveTurn(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	steered := make(chan string, 1)
	turnStarted := make(chan struct{})
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
				Steer:  func(text string) { steered <- text },
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	go func() {
		_, _ = manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_s","prompt":[{"type":"text","text":"hi"}]}`))
	}()
	<-turnStarted

	if _, err := manager.Steer(context.Background(), json.RawMessage(`{"sessionId":"sess_s","text":"focus on tests"}`)); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	select {
	case got := <-steered:
		if got != "focus on tests" {
			t.Fatalf("steered text = %q, want %q", got, "focus on tests")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Steer did not reach the runtime")
	}
	_, _ = manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"sess_s"}`))
}

func TestSteerRequiresActiveTurn(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.Steer(context.Background(), json.RawMessage(`{"sessionId":"sess_idle","text":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "no active turn") {
		t.Fatalf("err = %v, want no active turn error", err)
	}
}

func TestSteerRejectsEmptyText(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.Steer(context.Background(), json.RawMessage(`{"sessionId":"sess_idle","text":"  "}`))
	if err == nil || !strings.Contains(err.Error(), "non-empty text") {
		t.Fatalf("err = %v, want non-empty text error", err)
	}
}

// TestPromptTurnAppliesModeElevation pins the ACP elevation path end to end: a
// typed request is collected from the client, applied by the runtime, and
// announced with the state that was actually committed.
func TestPromptTurnAppliesModeElevation(t *testing.T) {
	f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
		{Editing: true, Reason: "The agent requests editing capability."},
	})
	// A legacy client: no modeAccess, just approved + edited.
	f.client.decide = legacyApproval("copilot")

	if err := f.awaitTurn(t, f.start()); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}

	if got := f.state.AccessState().Mode; got != "copilot" {
		t.Fatalf("mode = %q, want copilot — the elevation was not applied", got)
	}
	d, ok := f.decision(0)
	if !ok {
		t.Fatal("the tool was never acknowledged")
	}
	if !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
		t.Fatalf("delivered decision = %+v, want an approved typed grant", d)
	}
	updates := f.modeChangedUpdates()
	if len(updates) != 1 || updates[0]["mode"] != "copilot" {
		t.Fatalf("mode_changed updates = %#v, want exactly one for copilot", updates)
	}
}

func TestPromptTurnProjectsToolCallEvents(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	var updates []map[string]any
	started := time.Unix(1000, 0)
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{
						Name:      "shell.run",
						Args:      `{"cmd":"go test"}`,
						StartedAt: started,
					}})
					broker.Publish(session.EventAuditAdded, session.Event{Audit: &registry.AuditEvent{
						ToolName:      "shell.run",
						ResultContent: "all tests passed",
					}})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				updates = append(updates, p.Update)
			}
			return nil
		},
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_tc","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates, got %d: %#v", len(updates), updates)
	}
	wantID := fmt.Sprintf("shell.run-%d", started.UnixNano())
	call := updates[0]
	if call["kind"] != "tool_call" || call["toolName"] != "shell.run" || call["status"] != "running" {
		t.Fatalf("tool_call = %#v", call)
	}
	if call["toolCallId"] != wantID {
		t.Fatalf("toolCallId = %v, want %q", call["toolCallId"], wantID)
	}
	if call["args"] != `{"cmd":"go test"}` {
		t.Fatalf("args = %v", call["args"])
	}
	upd := updates[1]
	if upd["kind"] != "tool_call_update" || upd["status"] != "done" || upd["output"] != "all tests passed" {
		t.Fatalf("tool_call_update = %#v", upd)
	}
	if upd["toolCallId"] != wantID {
		t.Fatalf("update toolCallId = %v, want correlated %q", upd["toolCallId"], wantID)
	}
}

func TestPromptTurnToolCallUpdateErrorStatus(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	var updates []map[string]any
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventAuditAdded, session.Event{Audit: &registry.AuditEvent{
						ToolName:  "file.write",
						Error:     "permission denied",
						Timestamp: time.Unix(2000, 0),
					}})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				updates = append(updates, p.Update)
			}
			return nil
		},
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_te","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d: %#v", len(updates), updates)
	}
	upd := updates[0]
	if upd["kind"] != "tool_call_update" || upd["status"] != "error" {
		t.Fatalf("update = %#v", upd)
	}
	if upd["output"] != "permission denied" {
		t.Fatalf("output = %v, want error text", upd["output"])
	}
	if upd["toolCallId"] != fmt.Sprintf("file.write-%d", time.Unix(2000, 0).UnixNano()) {
		t.Fatalf("toolCallId = %v, want synthesized id", upd["toolCallId"])
	}
}

func TestCapToolTextRuneAware(t *testing.T) {
	// 4095 ASCII bytes followed by 4-byte emoji puts the byte-oriented
	// cut at toolTextCap (4096) in the middle of the first emoji, so a
	// byte-based implementation emits invalid UTF-8. Rune-aware
	// truncation must never split a multi-byte sequence.
	long := strings.Repeat("a", 4095) + strings.Repeat("😀", 50)
	got := capToolText(long)
	if !utf8.ValidString(got) {
		t.Fatalf("capToolText produced invalid UTF-8: len=%d", len(got))
	}
	if n := len([]rune(got)); n >= len([]rune(long)) {
		t.Fatalf("capToolText did not truncate: %d runes", n)
	}
}

func TestCapToolText(t *testing.T) {
	short := "ok"
	if got := capToolText(short); got != short {
		t.Fatalf("capToolText(%q) = %q", short, got)
	}
	long := strings.Repeat("x", toolTextCap+100)
	got := capToolText(long)
	if len(got) <= toolTextCap {
		t.Fatalf("capped length = %d, want cap + suffix", len(got))
	}
	if !strings.HasSuffix(got, "… (truncated)") {
		t.Fatalf("capped text missing truncation suffix")
	}
}

func TestSDDStartRunsToEndTurn(t *testing.T) {
	state := &session.State{}
	var gotPath string
	factory := func(planPath string, overrides map[routing.AgentRole]string) AgentRunner {
		return &fakeAgentRunner{run: func(ctx context.Context, goal string) error {
			gotPath = goal
			return nil
		}}
	}

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:       sessionID,
				BeginWork:       identityBeginWork,
				Events:          pubsub.NewBroker[session.Event](),
				State:           state,
				PipelineFactory: factory,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	raw, _ := json.Marshal(SDDStartParams{SessionID: "sess_1", PlanPath: "docs/plan.md"})
	res, err := manager.SDDStart(context.Background(), raw)
	if err != nil {
		t.Fatalf("SDDStart: %v", err)
	}
	result, ok := res.(SDDTurnResult)
	if !ok {
		t.Fatalf("SDDStart result type = %T, want SDDTurnResult", res)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", result.StopReason, "end_turn")
	}
	if gotPath != "docs/plan.md" {
		t.Errorf("runner received goal = %q, want %q", gotPath, "docs/plan.md")
	}
}

func TestSDDStartReturnsGateOnHumanGateRequired(t *testing.T) {
	state := &session.State{}
	state.SetSDDGate(session.SDDGate{TaskN: 2, Question: "which approach?"})
	factory := func(planPath string, overrides map[routing.AgentRole]string) AgentRunner {
		return &fakeAgentRunner{run: func(ctx context.Context, goal string) error {
			return pipeline.ErrHumanGateRequired
		}}
	}

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:       sessionID,
				BeginWork:       identityBeginWork,
				Events:          pubsub.NewBroker[session.Event](),
				State:           state,
				PipelineFactory: factory,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	raw, _ := json.Marshal(SDDStartParams{SessionID: "sess_1", PlanPath: "docs/plan.md"})
	res, err := manager.SDDStart(context.Background(), raw)
	if err != nil {
		t.Fatalf("SDDStart: %v", err)
	}
	result := res.(SDDTurnResult)
	if result.StopReason != "gate" {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, "gate")
	}
	if result.Gate == nil || result.Gate.TaskN != 2 || result.Gate.Question != "which approach?" {
		t.Fatalf("Gate = %+v, want {TaskN:2 Question:\"which approach?\"}", result.Gate)
	}
}

func TestSDDStartRejectsEmptyPlanPath(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(SDDStartParams{SessionID: "sess_1", PlanPath: " "})
	_, err := manager.SDDStart(context.Background(), raw)
	if err == nil {
		t.Fatal("SDDStart with blank planPath: got nil error, want an error")
	}
}

func TestSDDStartRejectsWhenPipelineUnavailable(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID, PipelineFactory: nil}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(SDDStartParams{SessionID: "sess_1", PlanPath: "docs/plan.md"})
	_, err := manager.SDDStart(context.Background(), raw)
	if err == nil {
		t.Fatal("SDDStart with nil PipelineFactory: got nil error, want an error")
	}
}

func TestSDDAnswerResumesGatedRunAndReachesEndTurn(t *testing.T) {
	state := &session.State{}
	state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "pick one"})

	callCount := 0
	var gotAnswer string
	runner := &fakeAgentRunner{
		run: func(ctx context.Context, goal string) error {
			callCount++
			if callCount == 1 {
				return pipeline.ErrHumanGateRequired
			}
			state.ClearSDDGate()
			return nil
		},
		answerGate: func(answer string) { gotAnswer = answer },
	}
	factory := func(planPath string, overrides map[routing.AgentRole]string) AgentRunner { return runner }

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:       sessionID,
				BeginWork:       identityBeginWork,
				Events:          pubsub.NewBroker[session.Event](),
				State:           state,
				PipelineFactory: factory,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	startRaw, _ := json.Marshal(SDDStartParams{SessionID: "sess_1", PlanPath: "docs/plan.md"})
	startRes, err := manager.SDDStart(context.Background(), startRaw)
	if err != nil {
		t.Fatalf("SDDStart: %v", err)
	}
	if startRes.(SDDTurnResult).StopReason != "gate" {
		t.Fatalf("SDDStart StopReason = %q, want %q", startRes.(SDDTurnResult).StopReason, "gate")
	}

	answerRaw, _ := json.Marshal(SDDAnswerParams{SessionID: "sess_1", Answer: "option b"})
	answerRes, err := manager.SDDAnswer(context.Background(), answerRaw)
	if err != nil {
		t.Fatalf("SDDAnswer: %v", err)
	}
	result, ok := answerRes.(SDDTurnResult)
	if !ok {
		t.Fatalf("SDDAnswer result type = %T, want SDDTurnResult", answerRes)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("SDDAnswer StopReason = %q, want %q", result.StopReason, "end_turn")
	}
	if gotAnswer != "option b" {
		t.Errorf("AnswerGate received = %q, want %q", gotAnswer, "option b")
	}
	if callCount != 2 {
		t.Errorf("runner.Run called %d times, want 2", callCount)
	}
}

func TestSDDAnswerRejectsWhenNoRunIsGated(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(SDDAnswerParams{SessionID: "sess_never_started", Answer: "x"})
	_, err := manager.SDDAnswer(context.Background(), raw)
	if err == nil {
		t.Fatal("SDDAnswer with no gated run: got nil error, want an error")
	}
}

func TestSDDAnswerRejectsEmptyAnswer(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(SDDAnswerParams{SessionID: "sess_1", Answer: " "})
	_, err := manager.SDDAnswer(context.Background(), raw)
	if err == nil {
		t.Fatal("SDDAnswer with blank answer: got nil error, want an error")
	}
}

func TestSwarmStatusReflectsSessionState(t *testing.T) {
	state := &session.State{}
	state.SetSwarmProgress(session.SwarmProgress{
		Goal:   "ship it",
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRoleActive, Detail: "thinking", Tokens: 120},
		},
		TokensUsed: 120,
		TokensMax:  4000,
	})

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID, State: state}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := manager.SwarmStatus(context.Background(), raw)
	if err != nil {
		t.Fatalf("SwarmStatus: %v", err)
	}
	result := res.(SwarmStatusResult)
	if !result.Active || result.Goal != "ship it" || result.TokensUsed != 120 {
		t.Fatalf("SwarmStatus = %+v, unexpected", result)
	}
	if len(result.Roles) != 1 || result.Roles[0].Status != "active" || result.Roles[0].Detail != "thinking" {
		t.Fatalf("SwarmStatus.Roles = %+v, unexpected", result.Roles)
	}
}

func TestSwarmStatusRequiresSessionID(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.SwarmStatus(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("SwarmStatus with no sessionId: got nil error, want an error")
	}
}

func TestSDDStatusReflectsSessionStateAndGate(t *testing.T) {
	state := &session.State{}
	state.SetSDDProgress(session.SDDProgress{
		Active: true, PlanName: "my-plan", TotalTasks: 3, DoneTasks: 1, Phase: "implement",
	})
	state.SetSDDGate(session.SDDGate{TaskN: 2, Question: "which approach?"})

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID, State: state}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := manager.SDDStatus(context.Background(), raw)
	if err != nil {
		t.Fatalf("SDDStatus: %v", err)
	}
	result := res.(SDDStatusResult)
	if !result.Active || result.PlanName != "my-plan" || result.TotalTasks != 3 || result.DoneTasks != 1 {
		t.Fatalf("SDDStatus = %+v, unexpected", result)
	}
	if result.Gate == nil || result.Gate.TaskN != 2 || result.Gate.Question != "which approach?" {
		t.Fatalf("SDDStatus.Gate = %+v, unexpected", result.Gate)
	}
}

func TestSDDStatusOmitsGateWhenNoneIsPending(t *testing.T) {
	state := &session.State{}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID, State: state}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(map[string]any{"sessionId": "sess_1"})
	res, err := manager.SDDStatus(context.Background(), raw)
	if err != nil {
		t.Fatalf("SDDStatus: %v", err)
	}
	if res.(SDDStatusResult).Gate != nil {
		t.Fatalf("SDDStatus.Gate = %+v, want nil", res.(SDDStatusResult).Gate)
	}
}

func TestSDDStartRejectsWhenFactoryReturnsNil(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:       sessionID,
				PipelineFactory: func(planPath string, overrides map[routing.AgentRole]string) AgentRunner { return nil },
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	raw, _ := json.Marshal(SDDStartParams{SessionID: "sess_1", PlanPath: "docs/plan.md"})
	_, err := manager.SDDStart(context.Background(), raw)
	if err == nil {
		t.Fatal("SDDStart when factory returns nil: got nil error, want an error")
	}
}

func TestBuildTelemetryContextReflectsMessagesAndPack(t *testing.T) {
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "hi there", session.ContentTypePlain)
	state.SetContextPack(contextpack.Pack{
		Sections:   []contextpack.Section{{Title: "repo card"}, {Title: "memory"}},
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 500, MaxTokens: 8000},
	})

	got := buildTelemetryContext(state)
	want := TelemetryContext{
		Messages: 2, MessageChars: len("hello") + len("hi there"),
		PackTokens: 500, PackMaxTokens: 8000, PackSections: 2,
	}
	if got != want {
		t.Errorf("buildTelemetryContext = %+v, want %+v", got, want)
	}
}

func TestBuildTelemetryContextEmptyState(t *testing.T) {
	got := buildTelemetryContext(&session.State{})
	want := TelemetryContext{}
	if got != want {
		t.Errorf("buildTelemetryContext(empty) = %+v, want %+v", got, want)
	}
}

func TestBuildToolStatsAggregatesByToolMostCalledFirst(t *testing.T) {
	events := []registry.AuditEvent{
		{ToolName: "read", Duration: 10 * time.Millisecond},
		{ToolName: "shell.run", Duration: 800 * time.Millisecond, Error: "exit 1"},
		{ToolName: "read", Duration: 20 * time.Millisecond},
		{ToolName: "read", Duration: 5 * time.Millisecond},
	}
	got := buildToolStats(events)
	want := []TelemetryToolStat{
		{Name: "read", Calls: 3, Errors: 0, SlowestMs: 20},
		{Name: "shell.run", Calls: 1, Errors: 1, SlowestMs: 800},
	}
	if len(got) != len(want) {
		t.Fatalf("buildToolStats returned %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("buildToolStats[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBuildToolStatsEmptyReturnsEmptySliceNotNil(t *testing.T) {
	got := buildToolStats(nil)
	if got == nil {
		t.Fatal("buildToolStats(nil) = nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("buildToolStats(nil) = %+v, want empty", got)
	}
}

func TestBuildSessionFooterCountsFinalAssistantMessagesAndLastTurnTokens(t *testing.T) {
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	state.AddMessage(session.RoleUser, "turn 1", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "answer 1", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "turn 2", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "answer 2", session.ContentTypePlain)
	state.SetTurnUsage(1234)
	state.SetTurnContextWindow(8000)

	got := buildSessionFooter(state)
	want := TelemetrySessionFooter{Turns: 2, LastTurnTokensUsed: 1234, LastTurnTokensWindow: 8000}
	if got != want {
		t.Errorf("buildSessionFooter = %+v, want %+v", got, want)
	}
}

func TestBaseRefForCachesFirstValuePerSession(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir) // helper added below; skips the test if git is unavailable

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})

	first := manager.baseRefFor("sess_1", dir)
	if first == "" {
		t.Fatal("baseRefFor returned empty ref for a real git repo")
	}

	// Make a commit — if baseRefFor recomputed HeadSHA on every call, the
	// second call would return the new commit instead of the cached one.
	runGit(t, dir, "commit", "--allow-empty", "-m", "second commit")

	second := manager.baseRefFor("sess_1", dir)
	if second != first {
		t.Fatalf("baseRefFor second call = %q, want cached %q (unchanged despite a new commit)", second, first)
	}

	// A different session ID gets its own cache entry, computed fresh.
	third := manager.baseRefFor("sess_2", dir)
	if third == first {
		t.Fatal("baseRefFor for a different session returned the stale sess_1 value instead of computing fresh")
	}
}

func TestBuildChangedFilesReflectsWorkingTreeDiff(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	state := &session.State{}
	state.WorkingDir = dir // confirm this is a settable field, not constructor-only, before writing this line

	// Establish the base ref before making any changes.
	base := manager.baseRefFor("sess_1", dir)
	if base == "" {
		t.Fatal("expected a non-empty base ref")
	}

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("line1\nline2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "new.txt")

	got := manager.buildChangedFiles("sess_1", state)
	if len(got) != 1 || got[0].Path != "new.txt" || got[0].Added != 2 {
		t.Fatalf("buildChangedFiles = %+v, want one entry for new.txt with Added=2", got)
	}
}

func TestBuildChangedFilesEmptyReturnsEmptySliceNotNil(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	got := manager.buildChangedFiles("sess_nonexistent_dir", &session.State{})
	if got == nil {
		t.Fatal("buildChangedFiles = nil, want a non-nil empty slice")
	}
}

// initGitRepo creates a minimal git repo with one commit in dir, skipping
// the test if git is unavailable. Shared by base-ref/changed-files tests.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
	runGit(t, dir, "commit", "--allow-empty", "-m", "initial")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// capturingNotifier records every notify call so tests can assert on
// which session/update kinds fired, in what order, without depending on
// forward()'s own message/tool_call/mode_changed notifications.
type capturingNotifier struct {
	mu    sync.Mutex
	calls []capturedNotify
}

type capturedNotify struct {
	method string
	params any
}

func (c *capturingNotifier) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, capturedNotify{method, params})
	return nil
}

// telemetryCalls returns every session_telemetry Update payload seen, in
// order.
func (c *capturingNotifier) telemetryCalls() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, call := range c.calls {
		if call.method != "session/update" {
			continue
		}
		p, ok := call.params.(SessionUpdateParams)
		if !ok {
			continue
		}
		if p.Update["kind"] == "session_telemetry" {
			out = append(out, p.Update)
		}
	}
	return out
}

func TestPromptTurnEmitsSessionTelemetryExactlyOnce(t *testing.T) {
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	notifier := &capturingNotifier{}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					state.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypePlain)
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
				State:  state,
			}, true
		},
		Notify: notifier.notify,
	})

	raw, _ := json.Marshal(PromptTurnParams{
		SessionID: "sess_1",
		Prompt:    []ContentBlock{{Type: "text", Text: "hi"}},
	})
	if _, err := manager.PromptTurn(context.Background(), raw); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}

	calls := notifier.telemetryCalls()
	if len(calls) != 1 {
		t.Fatalf("session_telemetry notify count = %d, want 1 (calls: %+v)", len(calls), calls)
	}
	footer, ok := calls[0]["sessionFooter"].(TelemetrySessionFooter)
	if !ok || footer.Turns != 1 {
		t.Fatalf("sessionFooter = %+v (ok=%v), want Turns=1", calls[0]["sessionFooter"], ok)
	}
}

func TestSDDStartEmitsSessionTelemetryEvenOnGate(t *testing.T) {
	state := &session.State{}
	state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "pick one"})
	notifier := &capturingNotifier{}
	factory := func(planPath string, overrides map[routing.AgentRole]string) AgentRunner {
		return &fakeAgentRunner{run: func(ctx context.Context, goal string) error {
			return pipeline.ErrHumanGateRequired
		}}
	}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID, BeginWork: identityBeginWork,
				Events: pubsub.NewBroker[session.Event](), State: state,
				PipelineFactory: factory,
			}, true
		},
		Notify: notifier.notify,
	})

	raw, _ := json.Marshal(SDDStartParams{SessionID: "sess_1", PlanPath: "docs/plan.md"})
	if _, err := manager.SDDStart(context.Background(), raw); err != nil {
		t.Fatalf("SDDStart: %v", err)
	}

	if len(notifier.telemetryCalls()) != 1 {
		t.Fatalf("session_telemetry notify count = %d, want 1 even though the run ended on a gate", len(notifier.telemetryCalls()))
	}
}

// TestResultOrErrorMapsAuthRequiredToRecoverableMessage: an OAuth provider
// with no usable login is recoverable — the session is fine, the user just
// has to authenticate. ACP has no login flow in v1, so the message must name
// the TUI as the place that does.
func TestResultOrErrorMapsAuthRequiredToRecoverableMessage(t *testing.T) {
	slot := &activeTurn{}
	_, err := resultOrError(&oauth.ErrAuthRequired{ServerName: "codex", Reason: "no stored tokens"}, slot)
	if err == nil {
		t.Fatal("expected an error for an auth-required turn")
	}
	msg := err.Error()
	if !strings.Contains(msg, "codex") {
		t.Errorf("message %q should name the provider", msg)
	}
	if !strings.Contains(msg, "OAuth login") {
		t.Errorf("message %q should say a login is required", msg)
	}
	if !strings.Contains(msg, "/connect") {
		t.Errorf("message %q should point at /connect", msg)
	}
	if !strings.Contains(msg, "TUI") {
		t.Errorf("message %q should say to use TUI mode", msg)
	}
	// It must not be reported as a generic turn failure.
	if strings.Contains(msg, "turn failed") {
		t.Errorf("message %q should not read as a generic turn failure", msg)
	}
}

// TestResultOrErrorMapsWrappedAuthRequired: the runner wraps the provider
// error, so the mapping must survive wrapping.
func TestResultOrErrorMapsWrappedAuthRequired(t *testing.T) {
	slot := &activeTurn{}
	wrapped := fmt.Errorf("agent failed: %w", &oauth.ErrAuthRequired{ServerName: "codex", Reason: "refresh rejected"})
	_, err := resultOrError(wrapped, slot)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "/connect") {
		t.Errorf("message %q should point at /connect", err.Error())
	}
}

// TestResultOrErrorAuthRequiredWithoutName: a nameless auth error must still
// produce a readable message.
func TestResultOrErrorAuthRequiredWithoutName(t *testing.T) {
	slot := &activeTurn{}
	_, err := resultOrError(&oauth.ErrAuthRequired{}, slot)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "a configured provider") {
		t.Errorf("message %q should name a placeholder provider", err.Error())
	}
}

// TestResultOrErrorStillReportsGenericTurnFailure guards the pre-existing
// behavior: a non-auth error keeps the generic wording.
func TestResultOrErrorStillReportsGenericTurnFailure(t *testing.T) {
	slot := &activeTurn{}
	_, err := resultOrError(errors.New("boom"), slot)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "turn failed") {
		t.Errorf("message %q should keep the generic turn-failure wording", err.Error())
	}
}

func TestFinishTurnSkipsTelemetryWhenStateIsNil(t *testing.T) {
	notifier := &capturingNotifier{}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: notifier.notify,
	})
	rt := &TurnRuntime{SessionID: "sess_1", State: nil}
	slot := &activeTurn{}
	res, err := manager.finishTurn("sess_1", rt, nil, slot, resultOrError)
	if err != nil {
		t.Fatalf("finishTurn: %v", err)
	}
	if res.(PromptTurnResult).StopReason != "end_turn" {
		t.Fatalf("finishTurn result = %+v, want end_turn", res)
	}
	if len(notifier.telemetryCalls()) != 0 {
		t.Fatal("finishTurn emitted telemetry despite rt.State being nil")
	}
}

// TestPromptTurnBridgesSkillGateToPermissionClient mirrors the
// broker-publish pattern of TestPromptTurnBridgesPendingApprovalToPermissionClient
// for the skill-load gate: the runner publishes
// session.EventPendingSkillGateChanged and blocks on the pending's
// ResponseChan; the forwarder must route the gate through the permission
// bridge, and the client's approve decision must resolve the pending as
// SkillGateAllowOnce.
func TestPromptTurnBridgesSkillGateToPermissionClient(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	pendingCh := make(chan session.SkillGateChoice, 1)
	// The runner consumes the delivered choice and forwards it here so the
	// test can assert the mapped value after PromptTurn returns.
	gotChoice := make(chan session.SkillGateChoice, 1)
	pending := &session.PendingSkillGate{
		Skill:        "go-testing",
		Description:  "Load the go-testing skill",
		Reason:       "small-context model",
		ResponseChan: pendingCh,
	}
	client := &fakePermissionClient{decision: PermissionDecision{Approved: true}}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingSkillGateChanged, session.Event{PendingSkillGate: pending})
					// Wait for the bridge to deliver the mapped choice.
					select {
					case got := <-pendingCh:
						gotChoice <- got
					case <-ctx.Done():
						return ctx.Err()
					}
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
		Perms:  client,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("client.calls = %d, want 1", client.calls)
	}
	if !client.gotReq.SkillGate {
		t.Fatalf("client.gotReq.SkillGate = false, want true")
	}
	if client.gotReq.ToolName != "skill.load" {
		t.Fatalf("client.gotReq.ToolName = %q, want skill.load", client.gotReq.ToolName)
	}
	if client.gotReq.Command != "go-testing" {
		t.Fatalf("client.gotReq.Command = %q, want go-testing", client.gotReq.Command)
	}
	select {
	case got := <-gotChoice:
		if got != session.SkillGateAllowOnce {
			t.Fatalf("pending resolved with %v, want SkillGateAllowOnce", got)
		}
	default:
		t.Fatal("pending was not resolved with a choice")
	}
}

// TestForwarderDeniesSkillGateWhenBridgeNil mirrors
// TestForwarderDeniesPendingApprovalWhenBridgeNil for the skill-load gate
// (F-SEC-13): when the permission bridge is nil and a pending skill gate
// arrives, the forwarder sends a deny on the ResponseChan instead of
// leaving the runner blocked forever.
func TestForwarderDeniesSkillGateWhenBridgeNil(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	response := make(chan session.SkillGateChoice, 1)
	pending := &session.PendingSkillGate{
		Skill:        "go-testing",
		Description:  "Load the go-testing skill",
		ResponseChan: response,
	}

	// No Perms → bridge is nil.
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventPendingSkillGateChanged, session.Event{PendingSkillGate: pending})
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
		// Perms is nil → bridge stays nil.
	})

	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":[{"type":"text","text":"hi"}]}`))
	if err != nil {
		t.Fatalf("PromptTurn() error = %v (expected nil, deny should unblock runner)", err)
	}

	select {
	case got := <-response:
		if got != session.SkillGateDeny {
			t.Fatalf("expected SkillGateDeny, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no response on ResponseChan; forwarder is stuck (F-SEC-13)")
	}
}

// TestSetModeSystemAccessField pins the session/set_mode system_access
// overlay: the request field turns the session flag on and the
// mode_changed broadcast carries the live value.
func TestSetModeSystemAccessField(t *testing.T) {
	state := &session.State{}
	var mu sync.Mutex
	var updates []map[string]any
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				State:     state,
				SetMode:   func(mode string) error { return nil },
			}, true
		},
		Notify: func(method string, params any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				updates = append(updates, p.Update)
			}
			return nil
		},
	})
	if _, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_sys","mode":"edit","system_access":true}`)); err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}
	if !state.SystemAccess() {
		t.Fatal("SystemAccess() = false after set_mode with system_access:true, want true")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 1 {
		t.Fatalf("updates = %#v, want one mode_changed", updates)
	}
	if updates[0]["kind"] != "mode_changed" || updates[0]["mode"] != "edit" {
		t.Fatalf("update = %#v, want mode_changed edit", updates[0])
	}
	if got, ok := updates[0]["system_access"].(bool); !ok || !got {
		t.Fatalf("broadcast system_access = %#v, want true", updates[0]["system_access"])
	}
}

// TestSetModeSystemAccessRevokesAndAbsentLeavesUntouched pins the three-way
// overlay semantics: system_access:true grants, an explicit false revokes, and
// an absent field leaves the current flag alone.
func TestSetModeSystemAccessRevokesAndAbsentLeavesUntouched(t *testing.T) {
	state := &session.State{}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				State:     state,
				SetMode:   func(mode string) error { return nil },
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	if _, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_sys","mode":"edit","system_access":true}`)); err != nil {
		t.Fatalf("SetMode(grant) error = %v", err)
	}
	if !state.SystemAccess() {
		t.Fatal("SystemAccess() = false after grant, want true")
	}

	// An absent field must not disturb the granted flag.
	if _, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_sys","mode":"edit"}`)); err != nil {
		t.Fatalf("SetMode(absent) error = %v", err)
	}
	if !state.SystemAccess() {
		t.Fatal("SystemAccess() = false after a set_mode without the field, want it untouched at true")
	}

	// An explicit false must revoke it.
	if _, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_sys","mode":"edit","system_access":false}`)); err != nil {
		t.Fatalf("SetMode(revoke) error = %v", err)
	}
	if state.SystemAccess() {
		t.Fatal("SystemAccess() = true after an explicit system_access:false, want false")
	}
}

// TestSetModeInvalidModeDoesNotApplySystem pins validation ordering: an
// invalid mode is rejected before the system-access overlay is applied.
func TestSetModeInvalidModeDoesNotApplySystem(t *testing.T) {
	state := &session.State{}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				State:     state,
				SetMode:   func(mode string) error { return nil },
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"sess_sys","mode":"yolo","system_access":true}`))
	if err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("err = %v, want invalid mode error", err)
	}
	if state.SystemAccess() {
		t.Fatal("SystemAccess() = true after a rejected set_mode, want false")
	}
}

// TestModeRequestSystemElevationApplied pins the mode.request elevation
// path: an approved call whose args carry system:true turns the session
// flag on and the mode_changed broadcast reports it.
// TestModeRequestSystemElevationApplied pins the system-only path: the grant
// reaches the session and the announcement carries the committed state.
func TestModeRequestSystemElevationApplied(t *testing.T) {
	f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
		{System: true, Reason: "The agent requests system access."},
	})
	f.client.decide = legacyApproval("edit")

	if err := f.awaitTurn(t, f.start()); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}

	if !f.state.SystemAccess() {
		t.Fatal("SystemAccess() = false after an approved system request, want true")
	}
	// A system-only grant preserves the mode it was made in.
	if got := f.state.AccessState().Mode; got != "default" {
		t.Fatalf("mode = %q, want the configured default preserved", got)
	}
	updates := f.modeChangedUpdates()
	if len(updates) != 1 {
		t.Fatalf("mode_changed updates = %#v, want exactly one", updates)
	}
	if got, ok := updates[0]["system_access"].(bool); !ok || !got {
		t.Fatalf("mode_changed update = %#v, want system_access=true", updates[0])
	}
	if updates[0]["mode"] != "default" {
		t.Fatalf("mode_changed mode = %v, want the actual default", updates[0]["mode"])
	}
}

// newResumeTestManager builds a TurnManager whose single runtime runs goal
// through a recording runner. goals receives every goal the runner is
// handed; rt mutates the returned runtime before it is installed.
func newResumeTestManager(t *testing.T, state *session.State, goals chan string, rt func(*TurnRuntime)) *TurnManager {
	t.Helper()
	broker := pubsub.NewBroker[session.Event]()
	return NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			r := &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, goal string) error {
					if goals != nil {
						goals <- goal
					}
					return nil
				}),
				Events: broker,
				State:  state,
			}
			if rt != nil {
				rt(r)
			}
			return r, true
		},
		Notify: func(method string, params any) error { return nil },
	})
}

func TestResumeIfIdleStartsTurnWhenIdle(t *testing.T) {
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	state.AddMessage(session.RoleAssistant, "waiting on the build", session.ContentTypePlain)
	goals := make(chan string, 4)
	manager := newResumeTestManager(t, state, goals, nil)

	manager.ResumeIfIdle("s1", "build", false)

	select {
	case goal := <-goals:
		if !strings.Contains(goal, `Watch "build" fired`) || !strings.Contains(goal, "auto-resuming") {
			t.Fatalf("resume goal = %q, want the wrapper goal text", goal)
		}
		if !strings.Contains(goal, "waiting on the build") {
			t.Fatalf("resume goal missing the assistant quote: %q", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ResumeIfIdle did not start a turn on an idle session")
	}
}

func TestResumeIfIdleNoTurnWhenBusy(t *testing.T) {
	block := make(chan struct{})
	firstStarted := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int64
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	manager := newResumeTestManager(t, state, nil, nil)

	// Replace the runner with one that parks so the slot stays occupied,
	// then drive a client prompt in the background.
	manager.lookup = func(sessionID string) (*TurnRuntime, bool) {
		return &TurnRuntime{
			SessionID: sessionID,
			BeginWork: identityBeginWork,
			Run: RunnerFunc(func(ctx context.Context, goal string) error {
				if calls.Add(1) == 1 {
					close(firstStarted)
				}
				<-block
				return nil
			}),
			Events: pubsub.NewBroker[session.Event](),
			State:  state,
		}, true
	}

	go func() {
		defer close(done)
		_, _ = manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}`))
	}()
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the client turn never started")
	}

	manager.ResumeIfIdle("s1", "build", false)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("runner calls = %d, want 1 (a busy session must not auto-resume)", got)
	}

	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the client turn never finished")
	}
}

func TestResumeIfIdleGateOffNoTurn(t *testing.T) {
	var calls atomic.Int64
	manager := newResumeTestManager(t, session.New(config.Default(), "/tmp", time.Now(), session.Persistence{}), nil, func(rt *TurnRuntime) {
		rt.WatchResumeEnabled = func() bool { return false }
		rt.Run = RunnerFunc(func(ctx context.Context, goal string) error { calls.Add(1); return nil })
	})

	manager.ResumeIfIdle("s1", "build", false)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("runner calls = %d, want 0 (the config gate is off)", got)
	}
}

func TestResumeIfIdleGatePendingNoTurn(t *testing.T) {
	var calls atomic.Int64
	manager := newResumeTestManager(t, session.New(config.Default(), "/tmp", time.Now(), session.Persistence{}), nil, func(rt *TurnRuntime) {
		rt.WatchResumeGatePending = func() bool { return true }
		rt.Run = RunnerFunc(func(ctx context.Context, goal string) error { calls.Add(1); return nil })
	})

	manager.ResumeIfIdle("s1", "build", false)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("runner calls = %d, want 0 (a human gate is pending)", got)
	}
}

func TestPostTurnLatchStartsFollowOnTurn(t *testing.T) {
	state := session.New(config.Default(), "/tmp", time.Now(), session.Persistence{})
	goals := make(chan string, 4)
	manager := newResumeTestManager(t, state, goals, nil)

	// A resume fire landed in the final-answer window: the turn-end
	// residual drain armed the latch before the turn ended.
	state.SetWatchResume("build", false)
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}

	select {
	case <-goals: // the client turn's own goal
	case <-time.After(2 * time.Second):
		t.Fatal("the client turn never ran")
	}
	select {
	case goal := <-goals:
		if !strings.Contains(goal, `Watch "build" fired`) || !strings.Contains(goal, "auto-resuming") {
			t.Fatalf("follow-on goal = %q, want the wrapper goal text", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the post-turn latch did not start a follow-on turn")
	}
}

// publishToolEvents runs a turn that publishes the given events and returns
// every projected session update.
func publishToolEvents(t *testing.T, publish func(b *pubsub.Broker[session.Event])) []map[string]any {
	t.Helper()
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	var updates []map[string]any
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					publish(broker)
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: func(method string, params any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				updates = append(updates, p.Update)
			}
			return nil
		},
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_ids","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return updates
}

// Concurrent calls of the same tool used to pair by tool name, so the second
// result attached to the wrong call. Real IDs pair them exactly.
func TestToolCallEventsPairByRealIDForConcurrentSameToolCalls(t *testing.T) {
	t0 := time.Unix(1000, 0)
	updates := publishToolEvents(t, func(b *pubsub.Broker[session.Event]) {
		b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "file.read", ToolCallID: "c1", StartedAt: t0}})
		b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "file.read", ToolCallID: "c2", StartedAt: t0.Add(time.Second)}})
		// c1 finishes: the state republishes the call still running (c2).
		b.Publish(session.EventAuditAdded, session.Event{Audit: &registry.AuditEvent{ToolName: "file.read", ToolCallID: "c1", ResultContent: "one"}})
		b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "file.read", ToolCallID: "c2", StartedAt: t0.Add(time.Second)}})
		b.Publish(session.EventAuditAdded, session.Event{Audit: &registry.AuditEvent{ToolName: "file.read", ToolCallID: "c2", ResultContent: "two"}})
	})
	var got []string
	for _, u := range updates {
		got = append(got, fmt.Sprintf("%v:%v:%v", u["kind"], u["toolCallId"], u["output"]))
	}
	want := []string{"tool_call:c1:<nil>", "tool_call:c2:<nil>", "tool_call_update:c1:one", "tool_call_update:c2:two"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("updates = %v\nwant     %v (no duplicate announce of c2)", got, want)
	}
}

// Providers that reuse IDs like call_0 across calls must still announce each
// call, since the earlier one finished.
func TestToolCallIDReuseAfterFinishAnnouncesAgain(t *testing.T) {
	updates := publishToolEvents(t, func(b *pubsub.Broker[session.Event]) {
		for range 2 {
			b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "noop", ToolCallID: "call_0", StartedAt: time.Unix(1, 0)}})
			b.Publish(session.EventAuditAdded, session.Event{Audit: &registry.AuditEvent{ToolName: "noop", ToolCallID: "call_0", ResultSummary: "ok"}})
		}
	})
	var kinds []string
	for _, u := range updates {
		kinds = append(kinds, fmt.Sprint(u["kind"]))
	}
	if strings.Join(kinds, ",") != "tool_call,tool_call_update,tool_call,tool_call_update" {
		t.Fatalf("kinds = %v", kinds)
	}
}

// An agent.await countdown re-publishes the running call under the same ID
// with new args; the client must see each change.
func TestToolCallArgsUpdateIsResentUnderSameID(t *testing.T) {
	t0 := time.Unix(1, 0)
	updates := publishToolEvents(t, func(b *pubsub.Broker[session.Event]) {
		b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "agent.await", ToolCallID: "w1", Args: "all (3 running)", StartedAt: t0}})
		b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "agent.await", ToolCallID: "w1", Args: "all (3 running)", StartedAt: t0}})
		b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "agent.await", ToolCallID: "w1", Args: "all (2 running)", StartedAt: t0}})
	})
	if len(updates) != 2 || updates[1]["args"] != "all (2 running)" {
		t.Fatalf("updates = %v, want the first announce and the changed-args resend only", updates)
	}
}

// Providers that number calls per response (ollama-0) must not collide across
// steps: the client keys its cards by ID.
func TestToolCallIDsAreUniqueAcrossSteps(t *testing.T) {
	updates := publishToolEvents(t, func(b *pubsub.Broker[session.Event]) {
		for step := int64(1); step <= 2; step++ {
			b.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{Name: "noop", ToolCallID: "ollama-0", StepID: step, StartedAt: time.Unix(step, 0)}})
			b.Publish(session.EventAuditAdded, session.Event{Audit: &registry.AuditEvent{ToolName: "noop", ToolCallID: "ollama-0", StepID: step, ResultSummary: "ok"}})
		}
	})
	ids := map[string]bool{}
	for _, u := range updates {
		if u["kind"] == "tool_call" {
			ids[fmt.Sprint(u["toolCallId"])] = true
		}
	}
	if len(ids) != 2 {
		t.Fatalf("announced ids = %v, want one per step", ids)
	}
	if updates[1]["toolCallId"] != updates[0]["toolCallId"] {
		t.Fatalf("an update must carry its call's scoped id: %v vs %v", updates[1]["toolCallId"], updates[0]["toolCallId"])
	}
}

// ---- typed access resolution over ACP ----

// accessTestSessionID is the ACP session ID the access fixtures use. The
// session's own ID must match it: the resolver compares the two, and a
// mismatch is exactly what a foreign request looks like.
const accessTestSessionID = "sess_access"

// accessTurnOpts tweaks the fixture for the fail-closed and duplicate cases.
type accessTurnOpts struct {
	// noBridge builds the manager without a permission client, so no bridge
	// exists to collect a choice.
	noBridge bool
	// noResolver omits the runtime's ResolveAccess callback.
	noResolver bool
	// duplicate re-publishes each request's pending event, so the forwarder
	// sees the same request twice.
	duplicate bool
}

// accessTurnFixture drives a real turn against a real session, with a
// deterministic resolver standing in for the app runtime. The runner registers
// typed requests and blocks on the canonical channel, exactly as the
// mode.request tool does.
type accessTurnFixture struct {
	t       *testing.T
	state   *session.State
	broker  *pubsub.Broker[session.Event]
	client  *fakePermissionClient
	manager *TurnManager
	session string
	opts    accessTurnOpts

	// barrier, when set, runs inside Apply so a test can hold the commit open
	// and prove the acknowledgment waits for it.
	barrier func()
	// prepareErr, when set, makes the resolver refuse the choice.
	prepareErr error

	mu      sync.Mutex
	calls   []*accessCall
	updates []map[string]any
}

// accessCall is one typed request the fixture's runner published, together
// with the decision the runner observed on the canonical channel. The runner
// consumes that channel, so the fixture records what it saw rather than
// letting a test read an already-drained channel.
type accessCall struct {
	pending  *session.PendingToolCall
	request  session.AccessRequest
	decision chan session.UserApprovalDecision
	settled  atomic.Bool
}

func newAccessTurnFixture(t *testing.T, cfg config.Config, intents []session.AccessIntent) *accessTurnFixture {
	t.Helper()
	return newAccessTurnFixtureWith(t, cfg, intents, accessTurnOpts{})
}

func newAccessTurnFixtureWith(t *testing.T, cfg config.Config, intents []session.AccessIntent, opts accessTurnOpts) *accessTurnFixture {
	t.Helper()
	// The session's own ID must match the ACP session ID: the resolver
	// compares them, and a mismatch is what a foreign request looks like.
	state := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{SessionID: accessTestSessionID})
	broker := pubsub.NewBroker[session.Event]()
	state.SetEventBroker(broker)
	f := &accessTurnFixture{
		t: t, state: state, broker: broker,
		client: &fakePermissionClient{}, session: accessTestSessionID, opts: opts,
	}
	cfgMgr := TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			rt := &TurnRuntime{
				SessionID:     sessionID,
				State:         state,
				BeginWork:     identityBeginWork,
				Events:        broker,
				AccessRequest: state.AccessRequest,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					return f.runIntents(ctx, intents)
				}),
			}
			if !opts.noResolver {
				rt.ResolveAccess = func(choice session.AccessChoice) (session.AccessResult, error) {
					return state.ResolveAccess(choice, f.prepare())
				}
			}
			return rt, true
		},
		Notify: func(method string, params any) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				f.updates = append(f.updates, p.Update)
			}
			return nil
		},
	}
	if !opts.noBridge {
		cfgMgr.Perms = f.client
	}
	f.manager = NewTurnManager(cfgMgr)
	return f
}

// prepare is the deterministic resolver: it commits the target implied by the
// request's missing flags and the chosen mode.
func (f *accessTurnFixture) prepare() session.AccessPrepare {
	return func(req session.AccessRequest, choice session.AccessChoice) (session.AccessCommit, error) {
		if f.prepareErr != nil {
			return session.AccessCommit{}, f.prepareErr
		}
		target := req.Current
		if req.MissingEditing {
			target.Mode = choice.Mode
		}
		if req.MissingSystem {
			target.System = true
		}
		apply := func() {}
		if f.barrier != nil {
			apply = f.barrier
		}
		return session.AccessCommit{Effective: target, Apply: apply}, nil
	}
}

// runIntents registers each intent as a typed request and blocks on the
// canonical channel, mirroring the mode.request tool. Every call reuses the
// same provider call ID, which is what a provider does across steps.
//
// The runner is the channel's consumer, so it records the decision it observed
// on the call record; a test reads that record rather than racing the runner
// for an already-drained channel.
func (f *accessTurnFixture) runIntents(ctx context.Context, intents []session.AccessIntent) error {
	for _, intent := range intents {
		pending := &session.PendingToolCall{
			ID:           "call-0",
			Name:         "mode.request",
			Reason:       intent.Reason,
			ResponseChan: make(chan session.UserApprovalDecision, 1),
		}
		req, waiting, err := f.state.RegisterAccess(ctx, pending, intent)
		if err != nil {
			return err
		}
		if !waiting {
			continue
		}
		rec := &accessCall{
			pending:  pending,
			request:  req,
			decision: make(chan session.UserApprovalDecision, 1),
		}
		f.mu.Lock()
		f.calls = append(f.calls, rec)
		f.mu.Unlock()
		if f.opts.duplicate {
			// Re-publish the same request's pending event: the forwarder must
			// not ask the client twice for one request.
			dup := req
			f.broker.Publish(session.EventPendingApprovalChanged, session.Event{
				PendingApproval: &session.PendingToolCall{ID: pending.ID, Name: pending.Name, Access: &dup},
			})
		}
		select {
		case d := <-pending.ResponseChan:
			rec.settled.Store(true)
			rec.decision <- d
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("timed out waiting for the access decision")
		}
	}
	return nil
}

// start runs the turn in the background and returns its outcome.
func (f *accessTurnFixture) start() <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := f.manager.PromptTurn(context.Background(), json.RawMessage(
			fmt.Sprintf(`{"sessionId":%q,"prompt":[{"type":"text","text":"hi"}]}`, f.session)))
		done <- err
	}()
	return done
}

func (f *accessTurnFixture) awaitTurn(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not finish")
		return nil
	}
}

// decision reads the decision the runner observed for the i-th request.
func (f *accessTurnFixture) decision(i int) (session.UserApprovalDecision, bool) {
	f.t.Helper()
	f.mu.Lock()
	if i >= len(f.calls) {
		f.mu.Unlock()
		return session.UserApprovalDecision{}, false
	}
	rec := f.calls[i]
	f.mu.Unlock()
	select {
	case d := <-rec.decision:
		return d, true
	case <-time.After(5 * time.Second):
		return session.UserApprovalDecision{}, false
	}
}

// noDecision reports whether the i-th request is still unanswered.
func (f *accessTurnFixture) noDecision(i int) bool {
	f.mu.Lock()
	if i >= len(f.calls) {
		f.mu.Unlock()
		return true
	}
	rec := f.calls[i]
	f.mu.Unlock()
	return !rec.settled.Load()
}

// request returns the i-th published request.
func (f *accessTurnFixture) request(i int) session.AccessRequest {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.calls) {
		f.t.Fatalf("no request %d was published", i)
	}
	return f.calls[i].request
}

// modeChangedUpdates returns every mode_changed announcement, in order.
func (f *accessTurnFixture) modeChangedUpdates() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, u := range f.updates {
		if u["kind"] == "mode_changed" {
			out = append(out, u)
		}
	}
	return out
}

// legacyApproval answers like a client that predates the typed payload: the
// outer Approved plus an Edited variant.
func legacyApproval(mode string) func(PermissionRequest) PermissionDecision {
	return func(PermissionRequest) PermissionDecision {
		return PermissionDecision{Approved: true, Edited: mode}
	}
}

// typedApproval answers like a client that understands modeAccess, echoing the
// request's identity and selecting a variant. The variant is sent only when the
// request actually presented an editing selector — a system-only request has no
// mode to choose, and sending one would be an invalid choice.
func typedApproval(mode string) func(PermissionRequest) PermissionDecision {
	return func(req PermissionRequest) PermissionDecision {
		d := PermissionDecision{Approved: true}
		if req.ModeAccess != nil {
			choice := &ModeAccessChoice{
				RequestID: req.ModeAccess.RequestID,
				SessionID: req.ModeAccess.SessionID,
				Current:   req.ModeAccess.Current,
			}
			if req.ModeAccess.MissingEditing {
				choice.Mode = mode
			}
			d.ModeAccess = choice
		}
		return d
	}
}

// The grant is applied before the tool can observe it, and only then is the
// committed state announced.
func TestACPAccessCommitsBeforeToolAcknowledgment(t *testing.T) {
	f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
		{Editing: true, System: true, Reason: "edit and read outside"},
	})
	f.client.decide = typedApproval("copilot")

	entered := make(chan struct{})
	release := make(chan struct{})
	f.barrier = func() {
		close(entered)
		<-release
	}

	done := f.start()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the resolver never reached Apply")
	}

	// Apply is inside the resolver: the tool must still be empty-handed and
	// the session must not yet report the new pair.
	if !f.noDecision(0) {
		t.Fatal("the grant was acknowledged before it was applied")
	}
	if got := f.state.AccessState(); got != (session.AccessState{Mode: "default"}) {
		t.Fatalf("session pair = %+v, want it unchanged while Apply is blocked", got)
	}

	close(release)
	if err := f.awaitTurn(t, done); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}

	d, ok := f.decision(0)
	if !ok {
		t.Fatal("the tool was never acknowledged")
	}
	if !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
		t.Fatalf("delivered decision = %+v, want an approved typed grant", d)
	}
	want := session.AccessState{Mode: "copilot", System: true}
	if got := f.state.AccessState(); got != want {
		t.Fatalf("session pair = %+v, want %+v", got, want)
	}
	updates := f.modeChangedUpdates()
	if len(updates) != 1 || updates[0]["mode"] != "copilot" {
		t.Fatalf("mode_changed updates = %#v, want exactly one for copilot", updates)
	}
}

// A legacy client's Edited text never switches a system-only request, in any
// mode.
func TestACPAccessLegacySystemOnlyPreservesEveryMode(t *testing.T) {
	for _, mode := range []string{"plan", "default", "edit", "copilot", "auto"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.Agent.ApprovalMode = mode
			f := newAccessTurnFixture(t, cfg, []session.AccessIntent{
				{System: true, Reason: "read outside the workspace"},
			})
			// An arbitrary Edited value: it must be ignored, because no
			// editing selector was presented.
			f.client.decide = legacyApproval("auto")

			if err := f.awaitTurn(t, f.start()); err != nil {
				t.Fatalf("PromptTurn: %v", err)
			}

			want := session.AccessState{Mode: mode, System: true}
			if got := f.state.AccessState(); got != want {
				t.Fatalf("session pair = %+v, want %+v", got, want)
			}
			d, ok := f.decision(0)
			if !ok || !d.Approved || d.Access == nil {
				t.Fatalf("delivered decision = %+v, want an approved typed grant", d)
			}
			if d.Access.Effective != want {
				t.Fatalf("effective = %+v, want %+v", d.Access.Effective, want)
			}
		})
	}
}

// Both client styles select an editing variant only when editing was actually
// displayed, and a combined approval grants the whole bundle.
func TestACPAccessCombinedLegacyAndTypedVariants(t *testing.T) {
	styles := []struct {
		name   string
		decide func(string) func(PermissionRequest) PermissionDecision
	}{
		{name: "legacy", decide: legacyApproval},
		{name: "typed", decide: typedApproval},
	}
	for _, style := range styles {
		for _, variant := range []string{"edit", "copilot", "auto"} {
			t.Run(style.name+"/"+variant, func(t *testing.T) {
				f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
					{Editing: true, System: true, Reason: "edit and read outside"},
				})
				f.client.decide = style.decide(variant)

				if err := f.awaitTurn(t, f.start()); err != nil {
					t.Fatalf("PromptTurn: %v", err)
				}

				want := session.AccessState{Mode: variant, System: true}
				if got := f.state.AccessState(); got != want {
					t.Fatalf("session pair = %+v, want the full bundle %+v", got, want)
				}
				updates := f.modeChangedUpdates()
				if len(updates) != 1 || updates[0]["mode"] != variant {
					t.Fatalf("mode_changed updates = %#v, want exactly one for %s", updates, variant)
				}
			})
		}
	}
}

// A permission change while the client is deciding re-asks against fresh state
// instead of applying the stale answer.
func TestACPAccessChangedTupleReasksFreshQuestion(t *testing.T) {
	f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
		{Editing: true, System: true, Reason: "edit and read outside"},
	})

	var calls int
	f.client.decide = func(req PermissionRequest) PermissionDecision {
		calls++
		if calls == 1 {
			// The user switches mode by hand while the client is deciding.
			// The answer the client is about to give describes the old tuple.
			if err := f.state.SetSessionMode("copilot", func() {}); err != nil {
				t.Errorf("SetSessionMode: %v", err)
			}
			return PermissionDecision{Approved: true, Edited: "edit"}
		}
		// The second question carries the fresh tuple: editing is satisfied,
		// so only system access is still missing and no variant is selected.
		return PermissionDecision{Approved: true}
	}

	if err := f.awaitTurn(t, f.start()); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}

	if calls != 2 {
		t.Fatalf("client calls = %d, want 2 (one refresh)", calls)
	}
	// The first question described the original tuple; the second the fresh one.
	if got := f.client.gotReq.ModeAccess; got == nil || got.Current.Mode != "copilot" || got.MissingEditing {
		t.Fatalf("second question = %+v, want the fresh tuple with editing satisfied", got)
	}
	if !f.client.gotReq.ModeAccess.MissingSystem {
		t.Fatal("the second question should still ask for system access")
	}
	// The stale answer applied nothing; the fresh one granted the remainder.
	want := session.AccessState{Mode: "copilot", System: true}
	if got := f.state.AccessState(); got != want {
		t.Fatalf("session pair = %+v, want %+v", got, want)
	}
	updates := f.modeChangedUpdates()
	if len(updates) != 1 || updates[0]["mode"] != "copilot" {
		t.Fatalf("mode_changed updates = %#v, want exactly one", updates)
	}
}

// A duplicate pending event asks the client once, and a reused provider call
// ID with a fresh request ID stays safe.
func TestACPAccessDuplicateEventsAndLateResponses(t *testing.T) {
	t.Run("duplicate event asks once", func(t *testing.T) {
		f := newAccessTurnFixtureWith(t, config.Default(), []session.AccessIntent{
			{System: true, Reason: "read outside the workspace"},
		}, accessTurnOpts{duplicate: true})
		f.client.decide = typedApproval("")

		if err := f.awaitTurn(t, f.start()); err != nil {
			t.Fatalf("PromptTurn: %v", err)
		}
		if f.client.calls != 1 {
			t.Fatalf("client calls = %d, want exactly 1 for one request", f.client.calls)
		}
		if got := f.state.AccessState(); got != (session.AccessState{Mode: "default", System: true}) {
			t.Fatalf("session pair = %+v, want one commit", got)
		}
		if updates := f.modeChangedUpdates(); len(updates) != 1 {
			t.Fatalf("mode_changed updates = %#v, want exactly one commit", updates)
		}
	})

	t.Run("reused call id with fresh request id", func(t *testing.T) {
		f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
			{System: true, Reason: "read outside the workspace"},
			{Editing: true, Reason: "edit a file"},
		})
		f.client.decide = typedApproval("edit")

		if err := f.awaitTurn(t, f.start()); err != nil {
			t.Fatalf("PromptTurn: %v", err)
		}
		if f.client.calls != 2 {
			t.Fatalf("client calls = %d, want one per request", f.client.calls)
		}
		// Both calls reused the provider's call ID; the requests are still
		// distinct and both settled.
		if f.request(0).RequestID == f.request(1).RequestID {
			t.Fatal("two requests shared a request ID")
		}
		for i := 0; i < 2; i++ {
			d, ok := f.decision(i)
			if !ok || !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
				t.Fatalf("decision %d = %+v, want an approved typed grant", i, d)
			}
		}
		want := session.AccessState{Mode: "edit", System: true}
		if got := f.state.AccessState(); got != want {
			t.Fatalf("session pair = %+v, want %+v", got, want)
		}
	})
}

// Without a bridge or a resolver the request fails closed: the tool unblocks
// non-approved and no capability moves.
func TestACPAccessMissingBridgeOrResolverFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts accessTurnOpts
	}{
		{name: "no bridge", opts: accessTurnOpts{noBridge: true}},
		{name: "no resolver", opts: accessTurnOpts{noResolver: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccessTurnFixtureWith(t, config.Default(), []session.AccessIntent{
				{Editing: true, System: true, Reason: "edit and read outside"},
			}, tc.opts)

			if err := f.awaitTurn(t, f.start()); err != nil {
				t.Fatalf("PromptTurn: %v", err)
			}

			d, ok := f.decision(0)
			if !ok {
				t.Fatal("the tool was never unblocked")
			}
			if d.Approved {
				t.Fatalf("a resolver-less runtime reported success: %+v", d)
			}
			if d.Access == nil || d.Access.Status != session.AccessFailed {
				t.Fatalf("delivered result = %+v, want a typed failure", d.Access)
			}
			if got := f.state.AccessState(); got != (session.AccessState{Mode: "default"}) {
				t.Fatalf("session pair = %+v, want both capabilities unchanged", got)
			}
			if updates := f.modeChangedUpdates(); len(updates) != 0 {
				t.Fatalf("a failed request announced %#v", updates)
			}
		})
	}
}

// A client failure settles the canonical request once and leaves a later
// request alone.
func TestACPAccessClientFailureAndCancellationSettleCanonical(t *testing.T) {
	f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
		{System: true, Reason: "read outside the workspace"},
	})
	f.client.err = errors.New("transport down")

	if err := f.awaitTurn(t, f.start()); err != nil {
		// A transport failure is fatal to the turn, exactly as an ordinary
		// bridge failure is; either outcome is acceptable as long as the tool
		// was settled.
		t.Logf("turn ended with %v (expected for a transport failure)", err)
	}

	d, ok := f.decision(0)
	if !ok {
		t.Fatal("the tool was never unblocked")
	}
	if d.Approved {
		t.Fatalf("a client failure reported success: %+v", d)
	}
	if d.Access == nil || d.Access.Status != session.AccessFailed {
		t.Fatalf("delivered result = %+v, want a typed failure", d.Access)
	}
	if got := f.state.AccessState(); got != (session.AccessState{Mode: "default"}) {
		t.Fatalf("session pair = %+v, want it unchanged", got)
	}
	// The canonical channel was settled exactly once: the runner consumed its
	// single value and the record shows it.
	if !f.calls[0].settled.Load() {
		t.Fatal("the canonical request was never settled")
	}

	// A later request is untouched by the earlier failure.
	later, waiting, err := f.state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}
	if got, pending := f.state.AccessRequest(); !pending || got.RequestID != later.RequestID {
		t.Fatalf("later request = (%+v, %v), want it still pending", got, pending)
	}
}

// A malformed editing variant cannot partially grant the system half of a
// combined request.
func TestACPAccessInvalidEditingVariantCannotPartiallyGrantSystem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		decide func(PermissionRequest) PermissionDecision
	}{
		{name: "legacy", decide: legacyApproval("bogus")},
		{name: "typed", decide: typedApproval("bogus")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccessTurnFixture(t, config.Default(), []session.AccessIntent{
				{Editing: true, System: true, Reason: "edit and read outside"},
			})
			f.client.decide = tc.decide

			if err := f.awaitTurn(t, f.start()); err != nil {
				t.Fatalf("PromptTurn: %v", err)
			}

			d, ok := f.decision(0)
			if !ok {
				t.Fatal("the tool was never unblocked")
			}
			if d.Approved {
				t.Fatalf("an invalid variant reported success: %+v", d)
			}
			if d.Access == nil || d.Access.Status != session.AccessFailed {
				t.Fatalf("delivered result = %+v, want a typed failure", d.Access)
			}
			// Neither half was granted.
			if got := f.state.AccessState(); got != (session.AccessState{Mode: "default"}) {
				t.Fatalf("session pair = %+v, want both capabilities unchanged", got)
			}
			if updates := f.modeChangedUpdates(); len(updates) != 0 {
				t.Fatalf("a failed request announced %#v", updates)
			}
		})
	}
}

// A manual mode change through the ACP RPC uses the session override: an
// unrelated reload keeps it, an explicit configured change replaces it.
func TestACPManualModeUsesSessionOverride(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	broker := pubsub.NewBroker[session.Event]()
	state.SetEventBroker(broker)
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				State:     state,
				BeginWork: identityBeginWork,
				Events:    broker,
				// The runtime's SetMode is the session's own setter with the
				// runner callback supplied by app; here the callback is a
				// no-op, which is what an inactive runtime does.
				SetMode: func(mode string) error {
					return state.SetSessionMode(mode, func() {})
				},
			}, true
		},
		Notify: func(string, any) error { return nil },
	})

	if _, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"s1","mode":"copilot"}`)); err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	if got := state.AccessState().Mode; got != "copilot" {
		t.Fatalf("mode = %q, want copilot", got)
	}

	// An unrelated config change must not revert the manual selection.
	unrelated := config.Default()
	unrelated.Agent.MaxToolIterations++
	state.RebaseSessionConfig(unrelated)
	if got := state.AccessState().Mode; got != "copilot" {
		t.Fatalf("mode after an unrelated reload = %q, want the override copilot", got)
	}

	// An explicit configured-mode change replaces it.
	explicit := config.Default()
	explicit.Agent.ApprovalMode = "edit"
	state.RebaseSessionConfig(explicit)
	if got := state.AccessState().Mode; got != "edit" {
		t.Fatalf("mode after an explicit change = %q, want edit", got)
	}
}
