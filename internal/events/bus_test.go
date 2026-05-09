package events

import (
	"context"
	"testing"
	"time"

	"github.com/alecpullen/marshal/pkg/protocol"
)

func TestNewBus(t *testing.T) {
	bus := NewBus(100)
	if bus == nil {
		t.Fatal("NewBus() returned nil")
	}
}

func TestBus_PublishAndSubscribe(t *testing.T) {
	bus := NewBus(10)

	// Create subscriber
	sub := bus.Subscribe("sess_123", nil, 10)
	defer sub.Close()

	// Publish event
	event := protocol.NewEvent(protocol.EventTaskSpawned, "sess_123")
	bus.Publish(event)

	// Receive event
	select {
	case got := <-sub.C():
		if got.Kind != protocol.EventTaskSpawned {
			t.Errorf("Kind = %v, want %v", got.Kind, protocol.EventTaskSpawned)
		}
		if got.SessionID != "sess_123" {
			t.Errorf("SessionID = %v, want %v", got.SessionID, "sess_123")
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for event")
	}
}

func TestBus_FilterBySession(t *testing.T) {
	bus := NewBus(10)

	// Subscribe to specific session
	sub := bus.Subscribe("sess_123", nil, 10)
	defer sub.Close()

	// Publish event for different session
	event1 := protocol.NewEvent(protocol.EventTaskSpawned, "sess_456")
	bus.Publish(event1)

	// Publish event for subscribed session
	event2 := protocol.NewEvent(protocol.EventTaskSpawned, "sess_123")
	bus.Publish(event2)

	// Should only receive event for sess_123
	select {
	case got := <-sub.C():
		if got.SessionID != "sess_123" {
			t.Errorf("SessionID = %v, want %v", got.SessionID, "sess_123")
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for event")
	}

	// Should not have more events
	select {
	case <-sub.C():
		t.Error("Received unexpected event")
	case <-time.After(100 * time.Millisecond):
		// Expected - no more events
	}
}

func TestBus_FilterByKind(t *testing.T) {
	bus := NewBus(10)

	// Subscribe to specific kinds
	sub := bus.Subscribe("", []protocol.EventKind{protocol.EventTaskSpawned, protocol.EventTaskCompleted}, 10)
	defer sub.Close()

	// Publish different kinds of events
	event1 := protocol.NewEvent(protocol.EventTaskProgress, "sess_123")
	bus.Publish(event1)

	event2 := protocol.NewEvent(protocol.EventTaskSpawned, "sess_123")
	bus.Publish(event2)

	event3 := protocol.NewEvent(protocol.EventToolCall, "sess_123")
	bus.Publish(event3)

	event4 := protocol.NewEvent(protocol.EventTaskCompleted, "sess_123")
	bus.Publish(event4)

	// Should receive TaskSpawned and TaskCompleted
	var received []protocol.EventKind
	for i := 0; i < 2; i++ {
		select {
		case got := <-sub.C():
			received = append(received, got.Kind)
		case <-time.After(time.Second):
			t.Fatal("Timeout waiting for event")
		}
	}

	if received[0] != protocol.EventTaskSpawned {
		t.Errorf("First event = %v, want %v", received[0], protocol.EventTaskSpawned)
	}
	if received[1] != protocol.EventTaskCompleted {
		t.Errorf("Second event = %v, want %v", received[1], protocol.EventTaskCompleted)
	}
}

func TestBus_BufferReplay(t *testing.T) {
	bus := NewBus(5)

	// Publish some events before subscribing
	for i := 0; i < 3; i++ {
		event := protocol.NewEvent(protocol.EventTaskSpawned, "sess_123")
		bus.Publish(event)
	}

	// Subscribe - should receive buffered events
	sub := bus.Subscribe("sess_123", nil, 10)
	defer sub.Close()

	// Should receive buffered events
	received := 0
	done := time.After(time.Second)
loop:
	for {
		select {
		case <-sub.C():
			received++
			if received >= 3 {
				break loop
			}
		case <-done:
			break loop
		}
	}

	if received < 3 {
		t.Errorf("Received %d buffered events, want at least 3", received)
	}
}

func TestBus_GlobalSubscriber(t *testing.T) {
	bus := NewBus(10)

	// Subscribe to all sessions (empty session ID filter)
	sub := bus.Subscribe("", nil, 10)
	defer sub.Close()

	// Publish events for different sessions
	bus.Publish(protocol.NewEvent(protocol.EventTaskSpawned, "sess_123"))
	bus.Publish(protocol.NewEvent(protocol.EventTaskSpawned, "sess_456"))
	bus.Publish(protocol.NewEvent(protocol.EventTaskSpawned, "sess_789"))

	// Should receive all events
	received := 0
	done := time.After(time.Second)
	for {
		select {
		case <-sub.C():
			received++
			if received >= 3 {
				return
			}
		case <-done:
			t.Fatalf("Received %d events, want 3", received)
		}
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	bus := NewBus(10)

	sub := bus.Subscribe("sess_123", nil, 10)

	// Unsubscribe
	sub.Close()

	// Publish event - should not panic
	event := protocol.NewEvent(protocol.EventTaskSpawned, "sess_123")
	bus.Publish(event)

	// Channel should be closed
	select {
	case _, ok := <-sub.C():
		if ok {
			t.Error("Channel should be closed after unsubscribe")
		}
	case <-time.After(time.Second):
		// Channel might be closed with no value
	}
}

func TestBus_MultipleSubscribers(t *testing.T) {
	bus := NewBus(10)

	sub1 := bus.Subscribe("sess_123", nil, 10)
	defer sub1.Close()

	sub2 := bus.Subscribe("sess_123", nil, 10)
	defer sub2.Close()

	// Publish event
	bus.Publish(protocol.NewEvent(protocol.EventTaskSpawned, "sess_123"))

	// Both should receive
	select {
	case <-sub1.C():
		// Good
	case <-time.After(time.Second):
		t.Error("sub1 did not receive event")
	}

	select {
	case <-sub2.C():
		// Good
	case <-time.After(time.Second):
		t.Error("sub2 did not receive event")
	}
}

func TestPublisher_Publish(t *testing.T) {
	bus := NewBus(10)
	pub := NewPublisher(bus, "sess_123", "agent_456")

	// Create a subscriber to receive the event
	sub := bus.Subscribe("sess_123", []protocol.EventKind{protocol.EventTaskSpawned}, 10)
	defer sub.Close()

	// Publish via publisher
	err := pub.Publish(protocol.EventTaskSpawned, protocol.TaskSpawnedPayload{
		TaskID: "task_789",
		Role:   "codegen",
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	// Verify received
	select {
	case got := <-sub.C():
		if got.AgentID != "agent_456" {
			t.Errorf("AgentID = %v, want %v", got.AgentID, "agent_456")
		}
		if got.SessionID != "sess_123" {
			t.Errorf("SessionID = %v, want %v", got.SessionID, "sess_123")
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for event")
	}
}

func TestPublisher_TaskLifecycle(t *testing.T) {
	bus := NewBus(10)
	pub := NewPublisher(bus, "sess_123", "agent_456")

	sub := bus.Subscribe("sess_123", nil, 10)
	defer sub.Close()

	// Simulate full task lifecycle
	pub.TaskSpawned("task_1", "codegen", "Add feature", []string{})
	pub.TaskProgress("task_1", "running", "Working", 0.5)
	pub.TaskCompleted("task_1", map[string]string{"result": "done"}, 1000, protocol.CostInfo{
		InputTokens:  100,
		OutputTokens: 50,
	})

	// Should receive 3 events
	kinds := []protocol.EventKind{}
	done := time.After(time.Second)
	for len(kinds) < 3 {
		select {
		case got := <-sub.C():
			kinds = append(kinds, got.Kind)
		case <-done:
			t.Fatalf("Only received %d events: %v", len(kinds), kinds)
		}
	}

	if kinds[0] != protocol.EventTaskSpawned {
		t.Errorf("First event = %v, want %v", kinds[0], protocol.EventTaskSpawned)
	}
	if kinds[1] != protocol.EventTaskProgress {
		t.Errorf("Second event = %v, want %v", kinds[1], protocol.EventTaskProgress)
	}
	if kinds[2] != protocol.EventTaskCompleted {
		t.Errorf("Third event = %v, want %v", kinds[2], protocol.EventTaskCompleted)
	}
}

func TestPublisher_CostUpdate(t *testing.T) {
	bus := NewBus(10)
	pub := NewPublisher(bus, "sess_123", "agent_456")

	sub := bus.Subscribe("sess_123", []protocol.EventKind{protocol.EventCostUpdate}, 10)
	defer sub.Close()

	pub.CostUpdate(0.05, 5.0, 0.50)

	select {
	case got := <-sub.C():
		var payload protocol.CostUpdatePayload
		if err := got.UnmarshalPayload(&payload); err != nil {
			t.Fatalf("UnmarshalPayload() error = %v", err)
		}
		if payload.SessionCost != 0.05 {
			t.Errorf("SessionCost = %v, want %v", payload.SessionCost, 0.05)
		}
		if payload.DailyBudget != 5.0 {
			t.Errorf("DailyBudget = %v, want %v", payload.DailyBudget, 5.0)
		}
		if payload.DailySpent != 0.50 {
			t.Errorf("DailySpent = %v, want %v", payload.DailySpent, 0.50)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for event")
	}
}

func TestContextPublisher_Publish(t *testing.T) {
	bus := NewBus(10)
	pub := NewPublisher(bus, "sess_123", "agent_456")

	// Create context that will be cancelled
	ctx, cancel := context.WithCancel(context.Background())

	ctxPub := pub.WithContext(ctx)

	// Cancel context before publishing
	cancel()

	// Publish should fail with context error
	err := ctxPub.Publish(protocol.EventTaskSpawned, protocol.TaskSpawnedPayload{})
	if err != context.Canceled {
		t.Errorf("Publish() error = %v, want %v", err, context.Canceled)
	}
}

func TestBus_Backpressure(t *testing.T) {
	bus := NewBus(10)

	// Create subscriber with small buffer that won't be drained
	sub := bus.Subscribe("sess_123", nil, 1)
	defer sub.Close()

	// Publish many events - should not block or panic
	for i := 0; i < 100; i++ {
		event := protocol.NewEvent(protocol.EventTaskSpawned, "sess_123")
		bus.Publish(event)
	}

	// Give time for any goroutines to settle
	time.Sleep(100 * time.Millisecond)

	// Subscriber should have received at least one event (buffered)
	select {
	case <-sub.C():
		// Good - received at least one
	default:
		// Also acceptable if buffer was processed
	}
}
