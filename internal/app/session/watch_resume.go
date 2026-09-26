package session

// The watch resume latch is a one-slot, check-and-clear flag, not a queue:
// exactly one producer (the runner's turn-end residual drain) and exactly
// one consumer per runtime idle boundary (the TUI's handleAgentFinished;
// ACP's post-turn latch check). It exists for the fire-in-the-final-answer
// window — a resume fire that lands after the final loop-top drain cannot
// be seen by the model, and the turn is about to end, so the only place
// left to record "wake when idle" is the session. Two resume watches whose
// residuals land in one turn-end produce one wake (replace-on-write); both
// reports are persisted and replay into the resumed turn, so nothing is
// lost. Cleared with the session; no DB involvement (in-memory binding,
// approved Q5).

// SetWatchResume arms the auto-resume latch. One slot, replace-on-write:
// a later arm overwrites the earlier one. Safe under s.mu.
func (s *State) SetWatchResume(name string, repeat bool) {
	s.mu.Lock()
	s.watchResumeName = name
	s.watchResumeRepeat = repeat
	s.mu.Unlock()
}

// TakeWatchResume returns the armed resume intent and clears the latch
// atomically. ok is false when the latch is not armed. Safe under s.mu.
func (s *State) TakeWatchResume() (name string, repeat bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchResumeName == "" {
		return "", false, false
	}
	name, repeat = s.watchResumeName, s.watchResumeRepeat
	s.watchResumeName = ""
	s.watchResumeRepeat = false
	return name, repeat, true
}
