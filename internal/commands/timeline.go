package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/strutil"
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
		if m.ContentType == session.ContentTypeSubagentReport || m.ContentType == session.ContentTypeWatchReport {
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

// timelineGlyphs are the station and connector marks. Single-cell, from
// Geometric Shapes and Box Drawing, matching the glyph package's coverage
// rule (see internal/app/tui/glyph/glyph.go).
const (
	timelineStation = "◆" // a turn on the current branch
	timelineFork    = "◇" // the head of another branch
	timelineElbow   = "├─"
)

// timelineDoc renders the session's turns as a navigable timeline.
//
// Each row is a station; Enter drills into that turn's actions rather than
// firing one. That is partly the Row model (one Action per row) and partly
// deliberate: restoring the working tree overwrites uncommitted work and
// should not be one keypress from a cursor movement.
func timelineDoc(state *session.State) Result {
	msgs := state.Messages()

	var snaps []db.SnapshotRow
	if database := state.DB(); database != nil {
		if rows, err := database.ListSnapshots(state.SessionID()); err != nil {
			state.Logger().Warn("timeline: list snapshots failed", "error", err)
		} else {
			snaps = rows
		}
	}

	entries := BuildTimeline(msgs, snaps, state.LeafID())
	if len(entries) == 0 {
		return Text("No turns yet — the timeline fills in as you work.")
	}

	rows := make([]Row, 0, len(entries)+len(state.Branches()))
	for _, e := range entries {
		e := e
		text := fmt.Sprintf("%s %s  %s",
			timelineStation, e.At.Local().Format("15:04"),
			strutil.Truncate(firstLineOf(e.Prompt), 48, true))
		if e.IsCurrentLeaf {
			text += "  ← here"
		}
		detail := ""
		if n := len(e.Files); n == 1 {
			detail = "1 file"
		} else if n > 1 {
			detail = fmt.Sprintf("%d files", n)
		}
		rows = append(rows, Row{
			Text:     text,
			Detail:   detail,
			Desc:     "open this turn's actions",
			Children: timelineActions(e),
		})
	}

	// Other branches render as forks below the linear run. The active
	// branch's turns are already listed above; a leaf that is not the
	// current one is a place the conversation could go back to.
	cur := state.LeafID()
	for i, leaf := range state.Branches() {
		if leaf == cur {
			continue
		}
		leaf := leaf
		rows = append(rows, Row{
			Text:        fmt.Sprintf("%s%s branch %d", timelineElbow, timelineFork, i+1),
			Detail:      fmt.Sprintf("leaf %d", leaf),
			Desc:        "switch the conversation to this branch",
			ActionLabel: "↵ switch",
			Action: func(s *session.State) Result {
				s.SwitchBranch(leaf)
				return Text(fmt.Sprintf("Switched to branch (leaf %d).", leaf))
			},
		})
	}

	return Result{Doc: &Doc{
		Title:  "Timeline",
		Rows:   rows,
		Footer: "Enter opens a turn's actions. Restoring files overwrites uncommitted work.",
	}}
}

// timelineActions are the per-turn action rows Enter drills into.
func timelineActions(e TimelineEntry) []Row {
	rows := []Row{{
		Text:        "rewind to before this turn",
		Desc:        "moves the conversation back; your next message starts a new branch",
		ActionLabel: "↵ rewind",
		Action: func(s *session.State) Result {
			newLeaf := s.Rewind(e.MsgID)
			return Text(fmt.Sprintf(
				"Rewound to before %q. Your next message starts a new branch (leaf %d).",
				strutil.Truncate(firstLineOf(e.Prompt), 60, true), newLeaf))
		},
	}}

	// Only offered when there is something to restore to. A turn that
	// predates every snapshot has no restore point, and offering a broken
	// action is worse than offering none.
	if e.SnapshotHash != "" {
		hash := e.SnapshotHash
		rows = append(rows, Row{
			Text:        "restore the working tree to this point",
			Desc:        "overwrites uncommitted changes made since this turn",
			ActionLabel: "↵ restore",
			Action: func(s *session.State) Result {
				sp, _, errMsg := snapshotContext(s)
				if errMsg != "" {
					return Text(errMsg)
				}
				if err := sp.Restore(context.Background(), hash); err != nil {
					return Text(fmt.Sprintf("Failed to restore snapshot: %v", err))
				}
				return Text(fmt.Sprintf("Restored working tree to snapshot %s.", hash))
			},
		})
	}

	if len(e.Files) > 0 {
		for _, f := range e.Files {
			rows = append(rows, Row{Text: "  " + f, Desc: "changed around this turn"})
		}
	}
	return rows
}

// firstLineOf keeps a multi-line prompt to one row.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
