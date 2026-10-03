package session

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func persistedState(t *testing.T, dbConn *db.DB, sessionID string) *State {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Default(), "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
}

func newStepTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, err := d.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	const sid = "step-sess"
	if err := d.CreateSession(sid, pid, "t", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return d, sid
}

func plainState() *State {
	return New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
}

func TestBeginEndStepBindsToLatestUserTurn(t *testing.T) {
	s := plainState()
	s.AddMessage(RoleUser, "first", ContentTypePlain)
	a := s.BeginStep(Actor{Role: "reviewer", Label: "reviewer #1"})
	s.EndStep(a)
	// A steering message is stored as RoleUser but is not a turn.
	s.AddMessage(RoleUser, "aside", ContentTypeSteering)
	b := s.BeginStep(Actor{})
	if a == 0 || b != a+1 {
		t.Fatalf("ids = %d, %d; want increasing from 1", a, b)
	}
	sa, ok := s.Step(a)
	if !ok || sa.TurnMsgID == 0 || sa.Actor.Role != "reviewer" || sa.EndedAt.IsZero() {
		t.Fatalf("step a = %+v", sa)
	}
	sb, _ := s.Step(b)
	if sb.TurnMsgID != sa.TurnMsgID {
		t.Errorf("steering message opened a new turn: %d vs %d", sb.TurnMsgID, sa.TurnMsgID)
	}
	if !sb.EndedAt.IsZero() {
		t.Error("step b should still be open")
	}
	s.EndStep(a) // idempotent
	again, _ := s.Step(a)
	if !again.EndedAt.Equal(sa.EndedAt) {
		t.Error("EndStep must be idempotent")
	}
}

func TestStepsFollowTheActiveBranch(t *testing.T) {
	s := plainState()
	s.AddMessage(RoleUser, "turn one", ContentTypePlain)
	one := s.BeginStep(Actor{})
	s.AddMessage(RoleAssistant, "done one", ContentTypePlain)
	s.AddMessage(RoleUser, "turn two", ContentTypePlain)
	turnTwo := s.Messages()[len(s.Messages())-1].ID
	two := s.BeginStep(Actor{})
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: two})
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: one})
	s.LogThinking(ThinkingEntry{Text: "hm", StepID: two, StartedAt: time.Now()})

	if got := len(s.Steps()); got != 2 {
		t.Fatalf("steps = %d, want 2", got)
	}
	s.Rewind(turnTwo)
	steps := s.Steps()
	if len(steps) != 1 || steps[0].ID != one {
		t.Fatalf("after rewind steps = %+v, want only step %d", steps, one)
	}
	var audits, thoughts int
	for _, it := range s.Transcript() {
		switch it.Kind {
		case KindAudit:
			audits++
			if it.Audit.StepID == two {
				t.Error("rewound step's audit is still in the transcript")
			}
		case KindThinking:
			thoughts++
		}
	}
	if audits != 1 || thoughts != 0 {
		t.Errorf("audits=%d thoughts=%d, want 1 and 0 after rewind", audits, thoughts)
	}
}

func TestAddNarrationStampsStep(t *testing.T) {
	s := plainState()
	s.AddMessage(RoleUser, "go", ContentTypePlain)
	id := s.BeginStep(Actor{})
	s.AddNarration(id, "Reading the parser.")
	msgs := s.Messages()
	last := msgs[len(msgs)-1]
	if last.ContentType != ContentTypeNarration || last.StepID != id || last.Role != RoleAssistant {
		t.Fatalf("narration message = %+v", last)
	}
	s.AddMessage(RoleAssistant, "plain", ContentTypePlain)
	msgs = s.Messages()
	if msgs[len(msgs)-1].StepID != 0 {
		t.Error("only narration carries a step id")
	}
}

func TestActiveToolCallsTrackedPerID(t *testing.T) {
	s := plainState()
	t0 := time.Unix(1000, 0)
	s.SetActiveToolCall(ActiveToolCall{Name: "a", ToolCallID: "c1", StartedAt: t0})
	s.SetActiveToolCall(ActiveToolCall{Name: "b", ToolCallID: "c2", StartedAt: t0.Add(time.Second)})
	if got := s.ActiveToolCalls(); len(got) != 2 || got[0].ToolCallID != "c1" || got[1].ToolCallID != "c2" {
		t.Fatalf("ActiveToolCalls = %+v", got)
	}
	if latest, _ := s.ActiveToolCall(); latest.ToolCallID != "c2" {
		t.Fatalf("latest = %+v, want c2", latest)
	}
	s.ClearActiveToolCallID("c2")
	if latest, ok := s.ActiveToolCall(); !ok || latest.ToolCallID != "c1" {
		t.Fatalf("after clearing c2 latest = %+v ok=%v, want c1", latest, ok)
	}
	s.ClearActiveToolCallID("c1")
	if _, ok := s.ActiveToolCall(); ok {
		t.Fatal("nothing should be running")
	}
	s.ClearActiveToolCallID("missing") // no-op

	// An empty ID keeps the legacy single slot.
	s.SetActiveToolCall(ActiveToolCall{Name: "x"})
	s.SetActiveToolCall(ActiveToolCall{Name: "y"})
	if got := s.ActiveToolCalls(); len(got) != 1 || got[0].Name != "y" {
		t.Fatalf("legacy slot = %+v", got)
	}
	s.ClearActiveToolCall()
	if len(s.ActiveToolCalls()) != 0 {
		t.Fatal("ClearActiveToolCall must clear every call")
	}
}

func TestResumeRestoresStepsAndToolRows(t *testing.T) {
	d, sid := newStepTestDB(t)
	first := persistedState(t, d, sid)
	first.AddMessage(RoleUser, "refactor it", ContentTypePlain)
	step := first.BeginStep(Actor{Role: "implementer", Label: "implementer", Model: "m", Provider: "p"})
	first.AddNarration(step, "Reading the parser.")
	first.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: step, ToolCallID: "call_1", ResultSummary: "ok"})
	first.EndStep(step)
	// A pre-steps row: must not come back.
	first.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "legacy.tool"})
	first.AddMessageFinal(RoleAssistant, "done", ContentTypePlain)

	second := persistedState(t, d, sid)
	steps := second.Steps()
	if len(steps) != 1 || steps[0].ID != step || steps[0].Actor.Role != "implementer" || steps[0].Actor.Model != "m" || steps[0].EndedAt.IsZero() {
		t.Fatalf("restored steps = %+v", steps)
	}
	if steps[0].TurnMsgID == 0 {
		t.Error("restored step lost its turn message")
	}
	log := second.AuditLog()
	if len(log) != 1 || log[0].ToolName != "file.read" || log[0].ToolCallID != "call_1" || log[0].StepID != step {
		t.Fatalf("restored audit log = %+v, want only the step-stamped call", log)
	}
	var narration bool
	for _, m := range second.Messages() {
		if m.ContentType == ContentTypeNarration && m.StepID == step {
			narration = true
		}
	}
	if !narration {
		t.Error("narration lost its step id on resume")
	}
	if next := second.BeginStep(Actor{}); next != step+1 {
		t.Errorf("next step after resume = %d, want %d", next, step+1)
	}

	// Restoring must not have re-persisted the row or fed this turn's ledger.
	calls, err := d.GetToolCalls(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Errorf("tool_calls rows = %d after resume, want 2 (no re-persist)", len(calls))
	}
	if len(second.toolAuditThisTurn) != 0 {
		t.Error("restored audits leaked into the turn ledger")
	}
}

func TestResumeOfPreStepSessionRestoresNoToolRows(t *testing.T) {
	d, sid := newStepTestDB(t)
	first := persistedState(t, d, sid)
	first.AddMessage(RoleUser, "hi", ContentTypePlain)
	first.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read"})
	first.AddMessageFinal(RoleAssistant, "done", ContentTypePlain)

	second := persistedState(t, d, sid)
	if len(second.AuditLog()) != 0 || len(second.Steps()) != 0 {
		t.Fatalf("pre-step session restored audits=%d steps=%d", len(second.AuditLog()), len(second.Steps()))
	}
	if len(second.Messages()) != 2 {
		t.Errorf("messages = %d, want 2", len(second.Messages()))
	}
}

func TestResumeHidesStepsOfRewoundTurns(t *testing.T) {
	d, sid := newStepTestDB(t)
	first := persistedState(t, d, sid)
	first.AddMessage(RoleUser, "one", ContentTypePlain)
	s1 := first.BeginStep(Actor{})
	first.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: s1, ToolCallID: "a"})
	first.AddMessageFinal(RoleAssistant, "ok", ContentTypePlain)
	first.AddMessage(RoleUser, "two", ContentTypePlain)
	turnTwo := first.Messages()[len(first.Messages())-1].ID
	s2 := first.BeginStep(Actor{})
	first.LogToolCall(registry.AuditEvent{Timestamp: time.Now(), ToolName: "file.read", StepID: s2, ToolCallID: "b"})
	first.Rewind(turnTwo)

	second := persistedState(t, d, sid)
	steps := second.Steps()
	if len(steps) != 1 || steps[0].ID != s1 {
		t.Fatalf("steps after resume of a rewound session = %+v", steps)
	}
	if log := second.AuditLog(); len(log) != 1 || log[0].ToolCallID != "a" {
		t.Fatalf("audit log = %+v, want only the surviving turn's call", log)
	}
}
