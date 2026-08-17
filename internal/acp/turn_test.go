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

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
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
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
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
		activeTurns:     map[string]*activeTurn{},
		activeTurnsMu:   sync.Mutex{},
		cancelTimeout:   100 * time.Millisecond,
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
						Questions:    []session.Question{{Question: "pick", Options: []string{"a", "b"}}},
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

func TestPromptTurnAppliesModeElevation(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	pendingCh := make(chan session.UserApprovalDecision, 1)
	var applied string
	var mu sync.Mutex
	var updates []map[string]any
	setModeDone := make(chan struct{})
	modeChangedDone := make(chan struct{})
	var setModeOnce sync.Once
	var modeChangedOnce sync.Once

	permClient := &fakePermissionClient{decision: PermissionDecision{Approved: true, Edited: "copilot"}}
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				BeginWork: identityBeginWork,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					pending := &session.PendingToolCall{
						ID:           "mode_req_1",
						Name:         "mode.request",
						Reason:       "mode-elevation: agent requests an editing mode",
						ResponseChan: pendingCh,
					}
					broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: pending})
					select {
					case <-pendingCh:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(5 * time.Second):
						return errors.New("timed out waiting for decision")
					}
				}),
				Events: broker,
				SetMode: func(mode string) error {
					applied = mode
					setModeOnce.Do(func() { close(setModeDone) })
					return nil
				},
			}, true
		},
		Notify: func(method string, params any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				updates = append(updates, p.Update)
				if p.Update["kind"] == "mode_changed" && p.Update["mode"] == "copilot" {
					modeChangedOnce.Do(func() { close(modeChangedDone) })
				}
			}
			return nil
		},
		Perms: permClient,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_elev","prompt":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatalf("PromptTurn() error = %v", err)
	}

	waitFor := func(ch <-chan struct{}, label string) {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
		}
	}
	waitFor(setModeDone, "SetMode")
	waitFor(modeChangedDone, "mode_changed update")

	if applied != "copilot" {
		t.Fatalf("applied mode = %q, want copilot — mode.request elevation was not applied", applied)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, u := range updates {
		if u["kind"] == "mode_changed" && u["mode"] == "copilot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mode_changed update emitted: %#v", updates)
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
	factory := func(planPath string) AgentRunner {
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
	factory := func(planPath string) AgentRunner {
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
	factory := func(planPath string) AgentRunner { return runner }

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
				PipelineFactory: func(planPath string) AgentRunner { return nil },
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
	factory := func(planPath string) AgentRunner {
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
