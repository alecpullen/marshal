package commands

import (
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/db"
)

func tAt(min int) time.Time {
	return time.Date(2026, 8, 25, 14, min, 0, 0, time.UTC)
}

func userMsg(id int64, min int, text string) session.Message {
	return session.Message{ID: id, Role: session.RoleUser, Content: text, CreatedAt: tAt(min)}
}

func snap(hash string, min int, files ...string) db.SnapshotRow {
	return db.SnapshotRow{Hash: hash, CreatedAt: tAt(min), Files: files}
}

func TestBuildTimelineOneEntryPerUserTurn(t *testing.T) {
	msgs := []session.Message{
		userMsg(1, 2, "first"),
		{ID: 2, Role: session.RoleAssistant, Content: "reply", CreatedAt: tAt(3)},
		userMsg(3, 5, "second"),
	}
	got := BuildTimeline(msgs, nil, 3)
	if len(got) != 2 {
		t.Fatalf("want one entry per user turn, got %d: %+v", len(got), got)
	}
	if got[0].Prompt != "first" || got[1].Prompt != "second" {
		t.Fatalf("wrong prompts: %+v", got)
	}
}

// THE mapping test. A turn takes the latest snapshot at or before it —
// positional counting is wrong because turn_index does not track branch
// position after a rewind.
func TestBuildTimelineMapsTurnToPrecedingSnapshot(t *testing.T) {
	msgs := []session.Message{userMsg(1, 5, "a"), userMsg(2, 15, "b")}
	snaps := []db.SnapshotRow{
		snap("early", 1, "x.go"),
		snap("mid", 4, "y.go"),
		snap("late", 12, "z.go"),
	}
	got := BuildTimeline(msgs, snaps, 2)
	if got[0].SnapshotHash != "mid" {
		t.Fatalf("turn at 14:05 should take the 14:04 snapshot, got %q", got[0].SnapshotHash)
	}
	if got[1].SnapshotHash != "late" {
		t.Fatalf("turn at 14:15 should take the 14:12 snapshot, got %q", got[1].SnapshotHash)
	}
	if len(got[0].Files) != 1 || got[0].Files[0] != "y.go" {
		t.Fatalf("entry should carry its snapshot's files, got %+v", got[0].Files)
	}
}

// A turn before any snapshot has nothing to restore to, and must say so
// rather than silently taking the first snapshot after it.
func TestBuildTimelineTurnBeforeAnySnapshotHasNoHash(t *testing.T) {
	got := BuildTimeline([]session.Message{userMsg(1, 1, "a")}, []db.SnapshotRow{snap("later", 9)}, 1)
	if got[0].SnapshotHash != "" {
		t.Fatalf("want no snapshot, got %q", got[0].SnapshotHash)
	}
}

// A snapshot taken in the same second as the turn counts as preceding it:
// the snapshot is captured before the tool call the turn triggered.
func TestBuildTimelineSnapshotAtSameInstantCounts(t *testing.T) {
	got := BuildTimeline([]session.Message{userMsg(1, 5, "a")}, []db.SnapshotRow{snap("same", 5)}, 1)
	if got[0].SnapshotHash != "same" {
		t.Fatalf("a snapshot at the same instant should count, got %q", got[0].SnapshotHash)
	}
}

func TestBuildTimelineMarksCurrentLeaf(t *testing.T) {
	got := BuildTimeline([]session.Message{userMsg(1, 1, "a"), userMsg(7, 2, "b")}, nil, 7)
	if got[0].IsCurrentLeaf {
		t.Error("first turn is not the leaf")
	}
	if !got[1].IsCurrentLeaf {
		t.Error("the turn matching leafID must be marked")
	}
}

func TestBuildTimelineEmptyIsNil(t *testing.T) {
	if got := BuildTimeline(nil, nil, 0); got != nil {
		t.Fatalf("want nil, got %+v", got)
	}
}

// Non-user messages, including the machine-generated subagent reports
// stored under RoleUser, must not become stations.
func TestBuildTimelineIgnoresSubagentReports(t *testing.T) {
	msgs := []session.Message{
		userMsg(1, 1, "real turn"),
		{ID: 2, Role: session.RoleUser, Content: "[subagent 1 finished]",
			ContentType: session.ContentTypeSubagentReport, CreatedAt: tAt(2)},
	}
	got := BuildTimeline(msgs, nil, 2)
	if len(got) != 1 {
		t.Fatalf("a subagent report is not a user turn, got %+v", got)
	}
}
