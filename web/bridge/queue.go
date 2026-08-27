package bridge

import (
	"context"
	"sync"
)

// slots bounds how many agents run at once. Agents beyond the limit wait
// rather than being rejected: the operator asked for the work, and CPU
// and memory caps mean an unbounded fleet oversubscribes the host.
//
// A zero limit means unbounded.
type slots struct {
	limit int

	mu      sync.Mutex
	running int
	waiting int
	// free is signalled once per release. It is buffered to the limit so
	// a release never blocks on an absent waiter.
	free chan struct{}
}

func newSlots(limit int) *slots {
	s := &slots{limit: limit}
	if limit > 0 {
		s.free = make(chan struct{}, limit)
	}
	return s
}

// acquire takes a slot, blocking until one is free or ctx is done.
func (s *slots) acquire(ctx context.Context) error {
	if s.limit <= 0 {
		s.mu.Lock()
		s.running++
		s.mu.Unlock()
		return nil
	}

	for {
		s.mu.Lock()
		if s.running < s.limit {
			s.running++
			s.mu.Unlock()
			return nil
		}
		s.waiting++
		s.mu.Unlock()

		select {
		case <-s.free:
			s.mu.Lock()
			s.waiting--
			s.mu.Unlock()
			// Loop rather than taking the slot directly: another waiter
			// may have won the race, and re-checking under the mutex is
			// what keeps running from exceeding limit.
		case <-ctx.Done():
			s.mu.Lock()
			s.waiting--
			s.mu.Unlock()
			return ctx.Err()
		}
	}
}

// release returns a slot and wakes one waiter.
func (s *slots) release() {
	s.mu.Lock()
	if s.running > 0 {
		s.running--
	}
	waiting := s.waiting
	s.mu.Unlock()

	if s.free != nil && waiting > 0 {
		select {
		case s.free <- struct{}{}:
		default:
		}
	}
}

// stats reports the current running and waiting counts, for the fleet UI.
func (s *slots) stats() (running, waiting int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.waiting
}
