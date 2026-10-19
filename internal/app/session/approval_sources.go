package session

import (
	"context"
	"sync"

	"marshal/internal/pubsub"
)

// ApprovalSource tags an approval event forwarded from a child actor. Owner is
// the child State that owns the prompt registry entry; Key is its canonical
// prompt key; Pending is a display-only copy; Actor names the child. The copy
// carries no response channel and no sync.Once, so a subscriber can display it
// but cannot settle it — settlement stays with the owning registry.
type ApprovalSource struct {
	Owner   *State
	Key     PromptKey
	Pending *PendingToolCall
	Actor   string
}

// eventBrokerLocked returns the state's event broker under State.mu.
func (s *State) eventBrokerLocked() *pubsub.Broker[Event] {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventBroker
}

// forwardChildApprovals forwards a child's ordinary approval lifecycle events
// to the root broker, tagged with the child as owner. It never relays
// transcript events and never occupies the parent's pending slot.
func (s *State) forwardChildApprovals(child *State) {
	if child == nil {
		return
	}
	rootBroker := s.eventBrokerLocked()
	childBroker := child.eventBrokerLocked()
	if rootBroker == nil || childBroker == nil {
		return
	}

	var mu sync.Mutex
	seen := map[PromptKey]bool{}
	forward := func(pending *PendingToolCall) {
		if pending == nil || pending.Access != nil {
			return
		}
		mu.Lock()
		if seen[pending.Key] {
			mu.Unlock()
			return
		}
		seen[pending.Key] = true
		mu.Unlock()
		rootBroker.Publish(EventPendingApprovalChanged, Event{
			ApprovalSource: &ApprovalSource{
				Owner:   child,
				Key:     pending.Key,
				Pending: pending,
				Actor:   child.SessionID(),
			},
		})
	}

	// Forward an approval that was already pending when the child registered.
	forward(child.PendingApproval())

	// Detach when either the child or the root closes.
	ctx, cancel := context.WithCancel(child.ctx)
	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	events := childBroker.Subscribe(ctx)
	go func() {
		defer cancel()
		for ev := range events {
			if ev.Type != EventPendingApprovalChanged {
				continue
			}
			forward(ev.Payload.PendingApproval)
		}
	}()
}
