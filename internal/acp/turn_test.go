package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/pubsub"
)

func TestPromptTurnRunsRunner(t *testing.T) {
	calledWith := ""
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					calledWith = prompt
					return nil
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	got, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":"hello"}`))
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
	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"missing","prompt":"hi"}`))
	if err == nil {
		t.Fatalf("expected error for unknown session")
	}
}

func TestPromptTurnForwardsEventsAsNotifications(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	var mu sync.Mutex
	got := []string{}
	notify := func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, method)
		return nil
	}
	done := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{Content: "hi"}})
					broker.Publish(session.EventActivityChanged, session.Event{Activity: &session.Activity{Kind: session.ActivityThinking}})
					close(done)
					return nil
				}),
				Events: broker,
			}, true
		},
		Notify: notify,
	})
	if _, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":"hi"}`)); err != nil {
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
		count := len(got)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("expected >=2 notifications, got %v", got)
	}
	// Methods are notification names derived from event types.
	wantPrefixes := map[string]bool{"session/message_added": false, "session/activity_changed": false}
	for _, m := range got {
		if _, ok := wantPrefixes[m]; ok {
			wantPrefixes[m] = true
		}
	}
	for m, seen := range wantPrefixes {
		if !seen {
			t.Fatalf("missing notification %q in %v", m, got)
		}
	}
}

func TestPromptTurnReturnsRunnerError(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					return errors.New("boom")
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})
	_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":"hi"}`))
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
				Run: RunnerFunc(func(ctx context.Context, prompt string) error {
					<-ctx.Done()
					return ctx.Err()
				}),
				Events: pubsub.NewBroker[session.Event](),
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	// Register a cancel func manually so we can verify the map wiring.
	manager.activeTurnsMu.Lock()
	manager.activeTurns["sess_test"] = func() {
		close(cancelCalled)
	}
	manager.activeTurnsMu.Unlock()

	if _, err := manager.Cancel(context.Background(), json.RawMessage(`{"sessionId":"sess_test"}`)); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	select {
	case <-cancelCalled:
	case <-time.After(2 * time.Second):
		t.Fatalf("cancel func not invoked")
	}
}

func TestPromptTurnCompletesAfterBrokerCloseAndRunnerRelease(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	runnerDone := make(chan struct{})
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID: sessionID,
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
		_, err := manager.PromptTurn(context.Background(), json.RawMessage(`{"sessionId":"sess_test","prompt":"hi"}`))
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
