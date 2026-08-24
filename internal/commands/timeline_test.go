package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
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

func TestTimelineDocHasARowPerTurn(t *testing.T) {
	state := newTestState()
	state.AddMessage(session.RoleUser, "first turn", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "reply", session.ContentTypeMarkdown)
	state.AddMessage(session.RoleUser, "second turn", session.ContentTypePlain)

	reg := New()
	toolReg := registry.New()
	RegisterAll(reg, toolReg)
	cmd, _ := reg.Lookup("timeline")
	res := cmd.Handler(state, nil)
	if res.Doc == nil {
		t.Fatalf("expected a Doc, got text: %q", res.Text)
	}
	// Two user turns = two station rows (no branches to add fork rows).
	if len(res.Doc.Rows) != 2 {
		t.Fatalf("want 2 rows (one per turn), got %d: %+v", len(res.Doc.Rows), res.Doc.Rows)
	}
	if !strings.Contains(res.Doc.Rows[0].Text, "first turn") {
		t.Fatalf("first row should contain the first turn's prompt, got %q", res.Doc.Rows[0].Text)
	}
	if !strings.Contains(res.Doc.Rows[1].Text, "second turn") {
		t.Fatalf("second row should contain the second turn's prompt, got %q", res.Doc.Rows[1].Text)
	}
}

func TestTimelineRowMarksCurrentPosition(t *testing.T) {
	state := newTestState()
	state.AddMessage(session.RoleUser, "turn a", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "reply", session.ContentTypeMarkdown)
	state.AddMessage(session.RoleUser, "turn b", session.ContentTypePlain)

	reg := New()
	toolReg := registry.New()
	RegisterAll(reg, toolReg)
	cmd, _ := reg.Lookup("timeline")
	res := cmd.Handler(state, nil)
	// The last user turn is the current leaf — its row should be marked.
	lastRow := res.Doc.Rows[len(res.Doc.Rows)-1]
	if !strings.Contains(lastRow.Text, "← here") {
		t.Fatalf("current leaf row should be marked, got %q", lastRow.Text)
	}
	// Earlier rows should not be marked.
	for _, r := range res.Doc.Rows[:len(res.Doc.Rows)-1] {
		if strings.Contains(r.Text, "← here") {
			t.Fatalf("non-leaf row should not be marked, got %q", r.Text)
		}
	}
}

// Each turn's Enter opens its actions rather than firing one directly:
// restoring the working tree is destructive and must not be one keypress
// away from a cursor move.
func TestTimelineRowDrillsIntoActions(t *testing.T) {
	state := newTestState()
	state.AddMessage(session.RoleUser, "a turn", session.ContentTypePlain)

	reg := New()
	toolReg := registry.New()
	RegisterAll(reg, toolReg)
	cmd, _ := reg.Lookup("timeline")
	res := cmd.Handler(state, nil)
	if len(res.Doc.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(res.Doc.Rows))
	}
	row := res.Doc.Rows[0]
	if len(row.Children) == 0 {
		t.Fatal("row should drill into children (actions), not fire directly")
	}
	// The first child should be the rewind action.
	if !strings.Contains(row.Children[0].Text, "rewind") {
		t.Fatalf("first child should be rewind, got %q", row.Children[0].Text)
	}
	// The row itself should NOT have an Action (it has Children instead).
	if row.Action != nil {
		t.Fatal("a row with Children must not also have an Action")
	}
}

// The restore action must be absent, not broken, when a turn predates any
// snapshot.
func TestTimelineNoRestoreActionWithoutSnapshot(t *testing.T) {
	state := newTestState()
	state.AddMessage(session.RoleUser, "a turn", session.ContentTypePlain)

	reg := New()
	toolReg := registry.New()
	RegisterAll(reg, toolReg)
	cmd, _ := reg.Lookup("timeline")
	res := cmd.Handler(state, nil)
	row := res.Doc.Rows[0]
	for _, child := range row.Children {
		if strings.Contains(child.Text, "restore") {
			t.Fatalf("restore action should be absent when there is no snapshot, got %q", child.Text)
		}
	}
}

func TestTimelineEmptySessionExplains(t *testing.T) {
	state := newTestState()
	reg := New()
	toolReg := registry.New()
	RegisterAll(reg, toolReg)
	cmd, _ := reg.Lookup("timeline")
	res := cmd.Handler(state, nil)
	if res.Doc != nil {
		t.Fatalf("empty session should return Text, not a Doc, got %+v", res)
	}
	if !strings.Contains(res.Text, "No turns") {
		t.Fatalf("empty session should explain, got %q", res.Text)
	}
}

// /rewind restored files to before the CURRENT turn rather than before the
// TARGET turn, because turnIndex is a session counter that Rewind does not
// move. Rewinding to an early turn therefore restored almost nothing while
// reporting success.
func TestRewindRestoresToTheTargetTurnNotTheCurrentOne(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/test/repo", "test-repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	sessionID := "test-session-" + t.Name()
	if err := database.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Capture the reference clock before adding messages. Snapshots are
	// stored with second precision (RFC3339) while messages keep their
	// in-memory nanosecond times, so both snapshots must be clearly
	// outside the message window or the pairing becomes ambiguous when
	// they all land in the same wall-clock second.
	now := time.Now().UTC()

	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{
		DB: database, SessionID: sessionID,
	})
	state.AddMessage(session.RoleUser, "turn1", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "a1", session.ContentTypeMarkdown)
	state.AddMessage(session.RoleUser, "turn2", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "a2", session.ContentTypeMarkdown)

	// Save two snapshots at different turn indices. The "early" snapshot
	// is the one that should be restored when rewinding to turn 1; the
	// "late" one is strictly after every message so it can never win the
	// timestamp pairing for turn 1.
	if _, err := database.SaveSnapshot(sessionID, 1, "hash-early", []string{"a.go"}, now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if _, err := database.SaveSnapshot(sessionID, 3, "hash-late", []string{"b.go"}, now.Add(1*time.Hour)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Inject a snapshotter that records the hash it was asked to restore.
	rec := &recordingSnapshotter{}
	state.SetSnapshotter(rec)

	reg := New()
	toolReg := registry.New()
	RegisterAll(reg, toolReg)
	cmd, _ := reg.Lookup("rewind")
	res := cmd.Handler(state, []string{"1"})
	if !strings.Contains(res.Text, "Rewound") {
		t.Fatalf("expected rewind success, got: %q", res.Text)
	}
	if rec.restoredHash != "hash-early" {
		t.Fatalf("expected restore of hash-early (the target turn's snapshot), got %q", rec.restoredHash)
	}
}

type recordingSnapshotter struct{ restoredHash string }

func (r *recordingSnapshotter) Track(ctx context.Context) (string, error) { return "", nil }
func (r *recordingSnapshotter) Diff(ctx context.Context, hash string) (string, error) {
	return "", nil
}
func (r *recordingSnapshotter) Restore(ctx context.Context, hash string) error {
	r.restoredHash = hash
	return nil
}
