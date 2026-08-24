package commands

import (
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
)

// TimelineEntry is one station on the timeline: a user turn, plus the
// snapshot the working tree was at when that turn began.
type TimelineEntry struct {
	MsgID         int64
	At            time.Time
	Prompt        string
	SnapshotHash  string // "" when the turn predates any snapshot
	Files         []string
	IsCurrentLeaf bool
}

// BuildTimeline pairs each user turn on the active branch with the latest
// snapshot taken at or before it.
//
// The pairing is by timestamp rather than by turn index. turn_index is a
// monotonic session counter (session.State.IncrementTurnIndex) that does
// not decrease across a rewind, so after one rewind the Nth user message on
// a branch is no longer turn N — positional or index-based pairing silently
// maps a turn to the wrong restore point.
//
// snaps must be ordered oldest-first, as db.ListSnapshots returns them.
func BuildTimeline(msgs []session.Message, snaps []db.SnapshotRow, leafID int64) []TimelineEntry {
	var out []TimelineEntry
	si := 0
	var cur *db.SnapshotRow
	for _, m := range msgs {
		if m.Role != session.RoleUser {
			continue
		}
		// A subagent completion report is stored under RoleUser so history
		// replays it, but it is not a turn the user took.
		if m.ContentType == session.ContentTypeSubagentReport {
			continue
		}
		// Advance through the snapshot series to the last one at or before
		// this turn. Both slices are time-ordered, so this is a single pass.
		for si < len(snaps) && !snaps[si].CreatedAt.After(m.CreatedAt) {
			s := snaps[si]
			cur = &s
			si++
		}
		e := TimelineEntry{
			MsgID:         m.ID,
			At:            m.CreatedAt,
			Prompt:        m.Content,
			IsCurrentLeaf: m.ID == leafID,
		}
		if cur != nil {
			e.SnapshotHash = cur.Hash
			e.Files = cur.Files
		}
		out = append(out, e)
	}
	return out
}
