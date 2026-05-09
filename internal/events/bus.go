// Package events implements an in-memory event bus for SwarmEvent publishing
// and subscribing. It is designed for intra-process communication between
// agents, the orchestrator, and the UI.
package events

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/alecpullen/marshal/pkg/protocol"
)

// Bus is an in-memory event bus that supports publish/subscribe patterns.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[string][]*Subscription
	buffer      []protocol.SwarmEvent // Recent events for new subscribers
	bufferSize  int
}

// Subscription represents a subscription to events.
type Subscription struct {
	ID        string
	SessionID string       // Filter by session (empty = all sessions)
	Kinds     []protocol.EventKind // Filter by kinds (nil = all kinds)
	ch        chan protocol.SwarmEvent
	bus       *Bus
	closed    bool
	mu        sync.Mutex
}

// NewBus creates a new event bus with the specified buffer size.
// The buffer retains recent events for new subscribers.
func NewBus(bufferSize int) *Bus {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	return &Bus{
		subscribers: make(map[string][]*Subscription),
		buffer:      make([]protocol.SwarmEvent, 0, bufferSize),
		bufferSize:  bufferSize,
	}
}

// Publish sends an event to all matching subscribers.
func (b *Bus) Publish(event protocol.SwarmEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Add to buffer (circular)
	if len(b.buffer) >= b.bufferSize {
		b.buffer = b.buffer[1:]
	}
	b.buffer = append(b.buffer, event)

	// Notify subscribers
	for _, sub := range b.subscribers[event.SessionID] {
		b.notify(sub, event)
	}
	// Also notify global subscribers (empty SessionID filter)
	for _, sub := range b.subscribers[""] {
		b.notify(sub, event)
	}
}

// notify sends an event to a subscriber if it matches the filters.
func (b *Bus) notify(sub *Subscription, event protocol.SwarmEvent) {
	sub.mu.Lock()
	defer sub.mu.Unlock()

	if sub.closed {
		return
	}

	// Check kind filter
	if len(sub.Kinds) > 0 {
		match := false
		for _, k := range sub.Kinds {
			if k == event.Kind {
				match = true
				break
			}
		}
		if !match {
			return
		}
	}

	// Non-blocking send with drop on backpressure
	select {
	case sub.ch <- event:
	default:
		// Channel full, drop event
	}
}

// Subscribe creates a new subscription to events.
// sessionID: filter by session (empty = all sessions)
// kinds: filter by event kinds (nil = all kinds)
// buffer: channel buffer size (0 = unbuffered)
func (b *Bus) Subscribe(sessionID string, kinds []protocol.EventKind, buffer int) *Subscription {
	sub := &Subscription{
		ID:        protocol.GenerateID(),
		SessionID: sessionID,
		Kinds:     kinds,
		ch:        make(chan protocol.SwarmEvent, buffer),
		bus:       b,
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	key := sessionID
	b.subscribers[key] = append(b.subscribers[key], sub)

	// Replay buffered events
	for _, event := range b.buffer {
		if sessionID == "" || event.SessionID == sessionID {
			b.notify(sub, event)
		}
	}

	return sub
}

// Unsubscribe removes a subscription from the bus.
func (b *Bus) Unsubscribe(sub *Subscription) {
	sub.mu.Lock()
	if sub.closed {
		sub.mu.Unlock()
		return
	}
	sub.closed = true
	close(sub.ch)
	sub.mu.Unlock()

	b.mu.Lock()
	defer b.mu.Unlock()

	key := sub.SessionID
	subs := b.subscribers[key]
	for i, s := range subs {
		if s.ID == sub.ID {
			b.subscribers[key] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
}

// C returns the event channel for the subscription.
func (s *Subscription) C() <-chan protocol.SwarmEvent {
	return s.ch
}

// Close unsubscribes and closes the subscription.
func (s *Subscription) Close() {
	s.bus.Unsubscribe(s)
}

// --- Publisher helpers ---

// Publisher provides a convenient interface for publishing events.
type Publisher struct {
	bus       *Bus
	sessionID string
	agentID   string
}

// SessionID returns the session ID for this publisher.
func (p *Publisher) SessionID() string {
	return p.sessionID
}

// AgentID returns the agent ID for this publisher.
func (p *Publisher) AgentID() string {
	return p.agentID
}

// NewPublisher creates a publisher for a specific session and agent.
func NewPublisher(bus *Bus, sessionID, agentID string) *Publisher {
	return &Publisher{
		bus:       bus,
		sessionID: sessionID,
		agentID:   agentID,
	}
}

// Publish creates and publishes an event with the given kind and payload.
func (p *Publisher) Publish(kind protocol.EventKind, payload interface{}) error {
	event := protocol.NewEvent(kind, p.sessionID)
	event.AgentID = p.agentID
	event, err := event.WithPayload(payload)
	if err != nil {
		return err
	}
	p.bus.Publish(event)
	return nil
}

// PublishRaw publishes a pre-constructed event.
func (p *Publisher) PublishRaw(event protocol.SwarmEvent) {
	if event.SessionID == "" {
		event.SessionID = p.sessionID
	}
	if event.AgentID == "" {
		event.AgentID = p.agentID
	}
	p.bus.Publish(event)
}

// TaskSpawned publishes a task spawned event.
func (p *Publisher) TaskSpawned(taskID, role, goal string, parentIDs []string) error {
	return p.Publish(protocol.EventTaskSpawned, protocol.TaskSpawnedPayload{
		TaskID:    taskID,
		Role:      role,
		Goal:      goal,
		ParentIDs: parentIDs,
	})
}

// TaskProgress publishes a task progress event.
func (p *Publisher) TaskProgress(taskID, status, message string, progress float64) error {
	return p.Publish(protocol.EventTaskProgress, protocol.TaskProgressPayload{
		TaskID:   taskID,
		Status:   status,
		Message:  message,
		Progress: progress,
	})
}

// TaskCompleted publishes a task completed event.
func (p *Publisher) TaskCompleted(taskID string, result interface{}, duration int64, cost protocol.CostInfo) error {
	var resultJSON []byte
	if result != nil {
		var err error
		resultJSON, err = json.Marshal(result)
		if err != nil {
			return err
		}
	}
	return p.Publish(protocol.EventTaskCompleted, protocol.TaskCompletedPayload{
		TaskID:   taskID,
		Result:   resultJSON,
		Duration: duration,
		Cost:     cost,
	})
}

// TaskFailed publishes a task failed event.
func (p *Publisher) TaskFailed(taskID, errorMsg, errorCode string, retryable bool) error {
	return p.Publish(protocol.EventTaskFailed, protocol.TaskFailedPayload{
		TaskID:    taskID,
		Error:     errorMsg,
		ErrorCode: errorCode,
		Retryable: retryable,
	})
}

// ToolCall publishes a tool call event.
func (p *Publisher) ToolCall(callID, toolName string, arguments interface{}) error {
	argsJSON, err := json.Marshal(arguments)
	if err != nil {
		return err
	}
	return p.Publish(protocol.EventToolCall, protocol.ToolCallPayload{
		CallID:    callID,
		ToolName:  toolName,
		Arguments: argsJSON,
	})
}

// ToolResult publishes a tool result event.
func (p *Publisher) ToolResult(callID, status string, result interface{}, errMsg string, newRefs []protocol.ContextRef) error {
	var resultJSON []byte
	if result != nil {
		var err error
		resultJSON, err = json.Marshal(result)
		if err != nil {
			return err
		}
	}
	return p.Publish(protocol.EventToolResult, protocol.ToolResultPayload{
		CallID:  callID,
		Status:  status,
		Result:  resultJSON,
		Error:   errMsg,
		NewRefs: newRefs,
	})
}

// ModelCall publishes a model call event.
func (p *Publisher) ModelCall(callID, model, provider string, messageCount int) error {
	return p.Publish(protocol.EventModelCall, protocol.ModelCallPayload{
		CallID:       callID,
		Model:        model,
		Provider:     provider,
		MessageCount: messageCount,
	})
}

// CostUpdate publishes a cost update event.
func (p *Publisher) CostUpdate(sessionCost, dailyBudget, dailySpent float64) error {
	return p.Publish(protocol.EventCostUpdate, protocol.CostUpdatePayload{
		SessionCost: sessionCost,
		DailyBudget: dailyBudget,
		DailySpent:  dailySpent,
	})
}

// --- Context-aware publisher ---

// WithContext creates a publisher that auto-publishes on context cancellation.
func (p *Publisher) WithContext(ctx context.Context) *ContextPublisher {
	return &ContextPublisher{
		Publisher: p,
		ctx:       ctx,
	}
}

// ContextPublisher wraps a Publisher with context awareness.
type ContextPublisher struct {
	*Publisher
	ctx context.Context
}

// Publish publishes an event or returns context error if cancelled.
func (cp *ContextPublisher) Publish(kind protocol.EventKind, payload interface{}) error {
	select {
	case <-cp.ctx.Done():
		return cp.ctx.Err()
	default:
		return cp.Publisher.Publish(kind, payload)
	}
}
