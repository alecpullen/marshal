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

func TestToolTarget(t *testing.T) {
	cases := []struct {
		name  string
		event registry.AuditEvent
		want  string
	}{
		{
			name:  "file.read returns the path",
			event: registry.AuditEvent{ToolName: "file.read", Args: json.RawMessage(`{"path":"budget.go"}`)},
			want:  "budget.go",
		},
		{
			name:  "shell.run returns the command",
			event: registry.AuditEvent{ToolName: "shell.run", Args: json.RawMessage(`{"command":"go test ./..."}`)},
			want:  "go test ./...",
		},
		{
			name:  "search tool returns the query",
			event: registry.AuditEvent{ToolName: "repo.search", Args: json.RawMessage(`{"query":"auth flow"}`)},
			want:  "auth flow",
		},
		{
			name: "FilesChanged wins over args",
			event: registry.AuditEvent{
				ToolName:     "file.write_patch",
				FilesChanged: []string{"a.go"},
				Args:         json.RawMessage(`{"path":"b.go"}`),
			},
			want: "a.go",
		},
		{
			name:  "no recognizable subject returns empty",
			event: registry.AuditEvent{ToolName: "todos", Args: json.RawMessage(`{"items":[]}`)},
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolTarget(tc.event); got != tc.want {
				t.Errorf("toolTarget() = %q, want %q", got, tc.want)
			}
		})
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
