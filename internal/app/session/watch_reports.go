package session

// Watch reports are machine-generated completion messages for
// background watch children. They are queued separately from the
// human steering queue so that ClearSteering (turn-cancel, Ctrl+X) and
// PopSteering (blank-Enter follow-up) never drop a background child's
// report. The runner drains them at loop-top alongside steering and
// injects them into the model wire as user messages.

// PushWatchReport appends a background watch child's completion report.
// It is drained by the runner at the next loop-top (or the next turn's
// start) and injected into the model context, so a report that arrives
// after the parent turn has ended still reaches the model.
func (s *State) PushWatchReport(msg string) {
	s.mu.Lock()
	s.watchReports = append(s.watchReports, msg)
	s.mu.Unlock()
}

// DrainWatchReports returns and clears the watch report queue
// atomically. The runner calls this at every loop-top, alongside
// DrainSteering, to inject completed background watch child reports into
// the live model context.
func (s *State) DrainWatchReports() []string {
	s.mu.Lock()
	out := s.watchReports
	s.watchReports = nil
	s.mu.Unlock()
	return out
}

// WatchReports returns a copy of the current report queue (does not
// drain). Used by tests and by the TUI to surface pending reports.
func (s *State) WatchReports() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.watchReports...)
}

// ClearWatchReports discards the watch report queue without
// delivering it. The runner calls this at turn end so that a report
// pushed after the final loop-top drain (a child that finished late) is
// not double-delivered in the next turn: the persisted RoleUser message
// (added by the completion goroutine alongside the queue push) is the
// durable copy that buildHistoryMessages replays, so discarding the
// queue here is safe.
func (s *State) ClearWatchReports() {
	s.mu.Lock()
	s.watchReports = nil
	s.mu.Unlock()
}
