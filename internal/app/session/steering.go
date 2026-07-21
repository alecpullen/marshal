package session

import "marshal/internal/pubsub"

// SteeringEvent is the broker payload published when a message is pushed
// to the steering queue. The session package owns the event type: event
// types are defined next to their owning service, never centrally.
type SteeringEvent struct {
	QueueLen int
	Message  string
}

// PushSteering appends a message typed by the user mid-turn. It is a
// steering message (consumed at the next loop-top) if a turn is running,
// or the next follow-up turn if the turn has already ended. Publishing
// to the broker lets the TUI re-render the queued marker without polling.
func (s *State) PushSteering(msg string) {
	s.mu.Lock()
	s.steeringQueue = append(s.steeringQueue, msg)
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen, Message: msg})
	}
}

// SteeringQueue returns a copy of the current queue (does not drain).
func (s *State) SteeringQueue() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steeringQueue...)
}

// DrainSteering returns and clears the queue atomically. The runner
// calls this at every loop-top to inject queued messages into the live
// model context.
func (s *State) DrainSteering() []string {
	s.mu.Lock()
	out := s.steeringQueue
	s.steeringQueue = nil
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
	return out
}

// PopSteering returns and removes the oldest queued steering message.
// Used by the TUI's follow-up turn path so each Enter pops one item
// at a time; DrainSteering handles the rest when the next turn starts.
func (s *State) PopSteering() (string, bool) {
	s.mu.Lock()
	if len(s.steeringQueue) == 0 {
		s.mu.Unlock()
		return "", false
	}
	oldest := s.steeringQueue[0]
	s.steeringQueue = s.steeringQueue[1:]
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
	return oldest, true
}

// ClearSteering drops all queued messages without returning them. Used
// by the TUI's Ctrl+X clear and the interrupt path (cancelTurn).
func (s *State) ClearSteering() {
	s.mu.Lock()
	s.steeringQueue = nil
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
}

// SetSteeringBroker wires the pub/sub broker so PushSteering publishes.
func (s *State) SetSteeringBroker(b *pubsub.Broker[SteeringEvent]) {
	s.mu.Lock()
	s.steeringBroker = b
	s.mu.Unlock()
}
