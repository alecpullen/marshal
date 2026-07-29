package tui

import (
	"encoding/json"
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
