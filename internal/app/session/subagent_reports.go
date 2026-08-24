package session

// Subagent reports are machine-generated completion messages for
// background agent.run children. They are queued separately from the
// human steering queue so that ClearSteering (turn-cancel, Ctrl+X) and
// PopSteering (blank-Enter follow-up) never drop a background child's
// report. The runner drains them at loop-top alongside steering and
// injects them into the model wire as user messages.

// PushSubagentReport appends a background subagent's completion report.
// It is drained by the runner at the next loop-top (or the next turn's
// start) and injected into the model context, so a report that arrives
// after the parent turn has ended still reaches the model.
func (s *State) PushSubagentReport(msg string) {
	s.mu.Lock()
	s.subagentReports = append(s.subagentReports, msg)
	s.mu.Unlock()
}

// DrainSubagentReports returns and clears the subagent report queue
// atomically. The runner calls this at every loop-top, alongside
// DrainSteering, to inject completed background child reports into the
// live model context.
func (s *State) DrainSubagentReports() []string {
	s.mu.Lock()
	out := s.subagentReports
	s.subagentReports = nil
	s.mu.Unlock()
	return out
}

// SubagentReports returns a copy of the current report queue (does not
// drain). Used by tests and by the TUI to surface pending reports.
func (s *State) SubagentReports() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.subagentReports...)
}
