package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func auditItem(tool string, mutate func(*registry.AuditEvent)) session.TranscriptItem {
	ev := registry.AuditEvent{Timestamp: time.Unix(100, 0), ToolName: tool, ResultSummary: "ok"}
	if mutate != nil {
		mutate(&ev)
	}
	return session.TranscriptItem{Kind: session.KindAudit, Timestamp: ev.Timestamp, Audit: &ev}
}

func messageItem() session.TranscriptItem {
	return session.TranscriptItem{
		Kind:      session.KindMessage,
		Timestamp: time.Unix(100, 0),
		Message:   &session.Message{Role: session.RoleUser, Content: "hi", ContentType: session.ContentTypePlain},
	}
}

func TestRunningToolRendersBelowGroupThenFolds(t *testing.T) {
	readEvent := func(path string, ts time.Time) registry.AuditEvent {
		args, err := json.Marshal(map[string]string{"path": path})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		return registry.AuditEvent{
			Timestamp:     ts,
			ToolName:      "file.read",
			Args:          args,
			ResultSummary: "ok",
		}
	}

	m := newTestModel(t)
	m.resize(100, 24)
	base := m.now()
	m.state.LogToolCall(readEvent("budget.go", base))
	m.state.LogToolCall(readEvent("runner.go", base.Add(time.Second)))
	m.state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "file.read",
		Args:      "chat.go",
		StartedAt: base.Add(2 * time.Second),
	})
	m.busy, m.turnStartedAt = true, base
	m.refreshViewport()

	running := stripANSI(m.viewport.View())
	if !strings.Contains(running, "budget.go, runner.go") || !strings.Contains(running, "×2") {
		t.Errorf("while running, want a ×2 group of completed calls:\n%s", running)
	}
	if !strings.Contains(running, "chat.go") {
		t.Errorf("while running, want the in-flight target on its own row:\n%s", running)
	}
	if strings.Contains(running, "×3") {
		t.Errorf("in-flight call must not fold into the group while running:\n%s", running)
	}

	m.state.ClearActiveToolCall()
	m.state.LogToolCall(readEvent("chat.go", base.Add(3*time.Second)))
	m.refreshViewport()

	done := stripANSI(m.viewport.View())
	if !strings.Contains(done, "budget.go, runner.go, chat.go") || !strings.Contains(done, "×3") {
		t.Errorf("after completion, want the call folded into a ×3 group:\n%s", done)
	}
}
