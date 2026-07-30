package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

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

func TestGroupTranscript(t *testing.T) {
	read := func() session.TranscriptItem { return auditItem("file.read", nil) }
	cases := []struct {
		name       string
		items      []session.TranscriptItem
		wantGroups []int // expected Group length per entry; 0 means ungrouped
	}{
		{
			name:       "consecutive same-tool events merge",
			items:      []session.TranscriptItem{read(), read(), read()},
			wantGroups: []int{3},
		},
		{
			name:       "different tool name breaks the run",
			items:      []session.TranscriptItem{read(), auditItem("shell.run", nil), read()},
			wantGroups: []int{0, 0, 0},
		},
		{
			name: "diff tool never merges",
			items: []session.TranscriptItem{
				auditItem("file.write_patch", nil),
				auditItem("file.write_patch", nil),
			},
			wantGroups: []int{0, 0},
		},
		{
			name: "event with error never merges",
			items: []session.TranscriptItem{
				read(),
				auditItem("file.read", func(e *registry.AuditEvent) { e.Error = "boom" }),
			},
			wantGroups: []int{0, 0},
		},
		{
			name: "event with hooks never merges",
			items: []session.TranscriptItem{
				read(),
				auditItem("file.read", func(e *registry.AuditEvent) {
					e.Hooks = []registry.HookMetadata{{Decision: "block"}}
				}),
			},
			wantGroups: []int{0, 0},
		},
		{
			name:       "message between same-tool events breaks the run",
			items:      []session.TranscriptItem{read(), messageItem(), read()},
			wantGroups: []int{0, 0, 0},
		},
		{
			name:       "single event yields an ungrouped entry",
			items:      []session.TranscriptItem{read()},
			wantGroups: []int{0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := groupTranscript(tc.items)
			if len(entries) != len(tc.wantGroups) {
				t.Fatalf("groupTranscript returned %d entries, want %d", len(entries), len(tc.wantGroups))
			}
			for i, want := range tc.wantGroups {
				if got := len(entries[i].Group); got != want {
					t.Errorf("entry %d Group len = %d, want %d", i, got, want)
				}
				if want == 0 && entries[i].Item == nil {
					t.Errorf("entry %d should be ungrouped with Item set", i)
				}
				if want >= 2 && entries[i].Item != nil {
					t.Errorf("entry %d is a collapsed run; Item should be nil", i)
				}
			}
		})
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

func groupEvents() []registry.AuditEvent {
	return []registry.AuditEvent{
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"budget.go"}`), ResultSummary: "42 lines"},
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"runner.go"}`), ResultSummary: "88 lines"},
		{ToolName: "file.read", Args: json.RawMessage(`{"path":"execute.go"}`), ResultSummary: "12 lines"},
	}
}

func TestRenderToolGroupCollapsedOneLine(t *testing.T) {
	out := stripANSI(renderToolGroup(groupEvents(), false, 80))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("collapsed run should render one line, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "×3") {
		t.Fatalf("collapsed line missing count:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], "budget.go, runner.go, execute.go") {
		t.Fatalf("collapsed line missing joined targets:\n%s", lines[0])
	}
}

func TestRenderToolGroupExpandedOneLinePerCall(t *testing.T) {
	out := stripANSI(renderToolGroup(groupEvents(), true, 80))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expanded run should render header + 3 lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "×3") {
		t.Fatalf("expanded header missing count:\n%s", lines[0])
	}
	for i, want := range []string{"budget.go", "runner.go", "execute.go"} {
		if !strings.Contains(lines[i+1], want) {
			t.Fatalf("expanded line %d missing %q:\n%s", i+1, want, lines[i+1])
		}
	}
}

// TestRunningToolRendersBelowGroupThenFolds pins the shape spec §4 declares
// correct: a running call renders on its own row beneath the group it belongs
// to — it needs the room, since shell.run streams output there — and folds
// into that group once it completes.
func TestToolGroupFitsWidthWithWideRunes(t *testing.T) {
	events := []registry.AuditEvent{
		{ToolName: "file.read", ResultSummary: "ok", Args: json.RawMessage(`{"path":"内部/非常に長いパス/ファイル一.go"}`)},
		{ToolName: "file.read", ResultSummary: "ok", Args: json.RawMessage(`{"path":"内部/別のとても長いパス/ファイル二.go"}`)},
	}
	out := renderToolGroup(events, false, 40)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d cells wide, budget 40: %q", w, line)
		}
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
	if !strings.Contains(running, DisplayToolName("file.read")+" ×2") {
		t.Errorf("while running, want a ×2 group of completed calls:\n%s", running)
	}
	if !strings.Contains(running, "chat.go") {
		t.Errorf("while running, want the in-flight target on its own row:\n%s", running)
	}
	if strings.Contains(running, DisplayToolName("file.read")+" ×3") {
		t.Errorf("in-flight call must not fold into the group while running:\n%s", running)
	}

	m.state.ClearActiveToolCall()
	m.state.LogToolCall(readEvent("chat.go", base.Add(3*time.Second)))
	m.refreshViewport()

	done := stripANSI(m.viewport.View())
	if !strings.Contains(done, DisplayToolName("file.read")+" ×3") {
		t.Errorf("after completion, want the call folded into a ×3 group:\n%s", done)
	}
}

func TestToolGroupUsesCategoryGlyph(t *testing.T) {
	events := []registry.AuditEvent{
		{ToolName: "file.read", ResultSummary: "ok"},
		{ToolName: "file.read", ResultSummary: "ok"},
	}
	out := stripANSI(renderToolGroup(events, false, 80))
	if !strings.Contains(out, "≡") {
		t.Fatalf("file.read group should use the ≡ gutter: %q", out)
	}
}
