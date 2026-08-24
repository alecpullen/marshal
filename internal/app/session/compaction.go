package session

import (
	"fmt"

	"marshal/internal/strutil"
)

// CompactionInfo describes one compaction: the wire before and after, and
// the generation the session rolled into.
type CompactionInfo struct {
	MessagesBefore int
	MessagesAfter  int
	TokensBefore   int
	TokensAfter    int
	Generation     int
}

// Summary renders the compaction as a single line for the transcript.
// Formatting lives here, not in the TUI, so the terminal and ACP surfaces
// report identical facts.
func (c CompactionInfo) Summary() string {
	return fmt.Sprintf("compacted %d messages to %d · %s → %s · generation %d",
		c.MessagesBefore, c.MessagesAfter,
		strutil.CompactTokens(c.TokensBefore),
		strutil.CompactTokens(c.TokensAfter),
		c.Generation)
}

// RecordCompaction posts a one-line compaction marker to the transcript.
// Compaction is otherwise invisible: it happens mid-turn, discards most of
// the wire, and previously left no user-facing trace at all, which made a
// long session look like the model had spontaneously forgotten its work.
func (s *State) RecordCompaction(info CompactionInfo) {
	s.AddMessage(RoleSystem, info.Summary(), ContentTypeCompaction)
}
