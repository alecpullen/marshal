package db

import (
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func stepTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	const sid = "sess-steps"
	if err := d.CreateSession(sid, pid, "t", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return d, sid
}

func TestStepsRoundTrip(t *testing.T) {
	d, sid := stepTestDB(t)
	start := time.Date(2026, 10, 3, 12, 0, 0, 123456789, time.UTC)
	if err := d.SaveStep(sid, StepRow{Seq: 1, TurnMessageID: 7, ActorRole: "reviewer", ActorLabel: "reviewer #1", Model: "m", Provider: "p", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveStep(sid, StepRow{Seq: 2, StartedAt: start.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	end := start.Add(3 * time.Second)
	if err := d.EndStep(sid, 1, end); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetSteps(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("steps = %+v", got)
	}
	s1 := got[0]
	if s1.TurnMessageID != 7 || s1.ActorRole != "reviewer" || s1.ActorLabel != "reviewer #1" || s1.Model != "m" || s1.Provider != "p" {
		t.Errorf("step 1 = %+v", s1)
	}
	if !s1.StartedAt.Equal(start) || !s1.EndedAt.Equal(end) {
		t.Errorf("step 1 times = %v..%v, want %v..%v", s1.StartedAt, s1.EndedAt, start, end)
	}
	if got[1].TurnMessageID != 0 || !got[1].EndedAt.IsZero() {
		t.Errorf("open step with no turn = %+v", got[1])
	}
}

func TestMessageStepSeqRoundTrip(t *testing.T) {
	d, sid := stepTestDB(t)
	now := time.Now().UTC()
	narr, err := d.SaveMessageStep(sid, "assistant", "Reading the parser.", "narration", now, "", 0, false, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := d.SaveMessage(sid, "user", "hi", "", now, "", 0, false, narr)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := d.MessagesOnBranch(sid, plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].StepSeq != 4 || msgs[1].StepSeq != 0 {
		t.Fatalf("StepSeq = %+v", msgs)
	}
}

func TestToolCallStepAndCallIDRoundTrip(t *testing.T) {
	d, sid := stepTestDB(t)
	ts := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, e := range []registry.AuditEvent{
		{Timestamp: ts, ToolName: "file.read", StepID: 3, ToolCallID: "call_abc"},
		{Timestamp: ts.Add(time.Second), ToolName: "file.read"}, // legacy shape
	} {
		if err := d.SaveToolCall(sid, e); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := d.GetToolCalls(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].StepID != 3 || calls[0].ToolCallID != "call_abc" {
		t.Fatalf("calls[0] = %+v", calls[0])
	}
	if calls[1].StepID != 0 || calls[1].ToolCallID != "" {
		t.Fatalf("legacy row must read back with no step: %+v", calls[1])
	}
}

// A database created before steps existed gains the table and columns on the
// next Migrate, and keeps working.
func TestMigrateUpgradesPreStepSchema(t *testing.T) {
	d, sid := stepTestDB(t)
	for _, stmt := range []string{
		`DROP TABLE steps`,
		`ALTER TABLE messages DROP COLUMN step_seq`,
		`ALTER TABLE tool_calls DROP COLUMN step_seq`,
		`ALTER TABLE tool_calls DROP COLUMN tool_call_id`,
		`ALTER TABLE turn_metrics DROP COLUMN intent_nudges`,
	} {
		if _, err := d.sqlDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate on old schema: %v", err)
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate must be idempotent: %v", err)
	}
	if err := d.SaveStep(sid, StepRow{Seq: 1, StartedAt: time.Now()}); err != nil {
		t.Fatalf("steps table missing after upgrade: %v", err)
	}
	if _, err := d.SaveMessageStep(sid, "assistant", "x", "", time.Now(), "", 0, false, 0, 1); err != nil {
		t.Fatalf("messages.step_seq missing after upgrade: %v", err)
	}
	if err := d.SaveToolCall(sid, registry.AuditEvent{Timestamp: time.Now(), ToolName: "t", StepID: 1, ToolCallID: "c"}); err != nil {
		t.Fatalf("tool_calls columns missing after upgrade: %v", err)
	}
	if _, err := d.InsertTurnMetrics(TurnMetricsRow{ProjectID: 1, SessionID: sid, StartedAt: time.Now(), IntentNudges: 1}); err != nil {
		t.Fatalf("turn_metrics.intent_nudges missing after upgrade: %v", err)
	}
}
