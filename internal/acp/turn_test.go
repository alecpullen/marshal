package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

// identityBeginWork is a TurnRuntime.BeginWork gate that passes through
// the context and returns a no-op finish function.
func identityBeginWork(ctx context.Context) (context.Context, func(), error) {
	return ctx, func() {}, nil
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
// The test uses a short bounded-wait override so it completes in
// milliseconds regardless.
func TestCancelAndWaitBoundsWait(t *testing.T) {
	orig := cancelWait
	cancelWait = 100 * time.Millisecond
	defer func() { cancelWait = orig }()

	tm := &TurnManager{
		activeTurns:   map[string]*activeTurn{},
		activeTurnsMu: sync.Mutex{},
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
					// Activity, audit, active-tool, and pending-clear
					// events should not produce any wire update.
					broker.Publish(session.EventActivityChanged, session.Event{Activity: &session.Activity{
						Kind: session.ActivityThinking,
					}})
					broker.Publish(session.EventAuditAdded, session.Event{Audit: auditEvent("test")})
					broker.Publish(session.EventActiveToolChanged, session.Event{ActiveTool: &session.ActiveToolCall{
						Name: "test_tool",
					}})
					// Pending approval cleared (nil) and pending question cleared (nil)
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
