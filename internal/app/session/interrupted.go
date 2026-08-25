package session

import "strings"

// TakeToolAuditForInterrupt renders the current turn's tool-audit buffer as
// a compact comma-separated tool list (tool name + short label per entry)
// and CLEARS the buffer. It is called when a turn is interrupted (Esc) so
// the dead turn's entries are folded into the interrupt marker instead of
// being flushed under the NEXT turn's final assistant message. Returns ""
// when the buffer is empty.
func (s *State) TakeToolAuditForInterrupt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.toolAuditThisTurn) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.toolAuditThisTurn))
	for _, e := range s.toolAuditThisTurn {
		tool := strings.TrimSpace(e.Tool)
		if tool == "" {
			continue
		}
		summary := strings.TrimSpace(e.Summary)
		if summary != "" && summary != tool {
			parts = append(parts, tool+" "+summary)
		} else {
			parts = append(parts, tool)
		}
	}
	s.toolAuditThisTurn = nil
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// SetInterruptedTurnNote stores a one-shot pending note describing where an
// interrupted turn stopped. It is set at cancel time and consumed (and
// cleared) at the next turn's start, where it is surfaced in the user's
// transcript. The model gets its full orientation from the persisted
// RoleUser interrupt marker instead.
func (s *State) SetInterruptedTurnNote(note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interruptedTurnNote = note
}

// TakeInterruptedTurnNote returns and clears the pending interrupted-turn
// note. Returns "" when none is pending.
func (s *State) TakeInterruptedTurnNote() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	note := s.interruptedTurnNote
	s.interruptedTurnNote = ""
	return note
}
