package session

import "context"

// SetHold pauses (on) or resumes (off) the agent loop. A held agent
// finishes the model call it is in, then waits in WaitUnheld before its
// next tool or model call. Setting the current value is a no-op.
func (s *State) SetHold(on bool) {
	s.mu.Lock()
	if s.hold == on {
		s.mu.Unlock()
		return
	}
	s.hold = on
	if on {
		s.holdCh = make(chan struct{})
	} else if s.holdCh != nil {
		close(s.holdCh)
		s.holdCh = nil
	}
	s.mu.Unlock()
	s.publishEvent(EventHoldChanged, Event{Held: on})
}

// Held reports whether the session is currently held.
func (s *State) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hold
}

// WaitUnheld blocks while the session is held. It returns nil once the
// hold is released and ctx.Err() if ctx is cancelled first.
func (s *State) WaitUnheld(ctx context.Context) error {
	for {
		s.mu.Lock()
		held, ch := s.hold, s.holdCh
		s.mu.Unlock()
		if !held {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
