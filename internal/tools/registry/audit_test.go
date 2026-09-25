package registry

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestNewAuditEventCopiesToolCallResultAndError(t *testing.T) {
	now := time.Unix(123, 0)
	exitCode := 2
	tool := testTool("shell.run")
	tool.Risk = RiskCommand
	call := ToolCall{
		ID:   "call-1",
		Name: "shell.run",
		Args: json.RawMessage(`{"cmd":"go test ./..."}`),
	}
	result := ToolResult{
		Summary:         "tests failed",
		Content:         "FAIL",
		FilesChanged:    []string{"internal/example.go"},
		CommandExitCode: &exitCode,
	}

	event := NewAuditEvent(now, tool, call, result, ApprovalApproved, errors.New("exit status 2"))

	if !event.Timestamp.Equal(now) {
		t.Fatalf("Timestamp = %v, want %v", event.Timestamp, now)
	}
	if event.ToolName != "shell.run" {
		t.Fatalf("ToolName = %q, want shell.run", event.ToolName)
	}
	if string(event.Args) != `{"cmd":"go test ./..."}` {
		t.Fatalf("Args = %s", event.Args)
	}
	if event.Risk != RiskCommand {
		t.Fatalf("Risk = %q, want command", event.Risk)
	}
	if event.Approval != ApprovalApproved {
		t.Fatalf("Approval = %q, want approved", event.Approval)
	}
	if event.ResultSummary != "tests failed" {
		t.Fatalf("ResultSummary = %q, want tests failed", event.ResultSummary)
	}
	if len(event.FilesChanged) != 1 || event.FilesChanged[0] != "internal/example.go" {
		t.Fatalf("FilesChanged = %#v", event.FilesChanged)
	}
	if event.CommandExitCode == nil || *event.CommandExitCode != 2 {
		t.Fatalf("CommandExitCode = %#v, want 2", event.CommandExitCode)
	}
	if event.Error != "exit status 2" {
		t.Fatalf("Error = %q, want exit status 2", event.Error)
	}
	if event.AgentRole != "" {
		t.Fatalf("AgentRole = %q, want empty", event.AgentRole)
	}
	if event.Model != "" {
		t.Fatalf("Model = %q, want empty", event.Model)
	}
}

func TestNewAuditEventRoundTripsOriginalArgs(t *testing.T) {
	now := time.Unix(456, 0)
	tool := testTool("shell.run")
	tool.Risk = RiskCommand
	call := ToolCall{
		ID:   "call-rewrite",
		Name: "shell.run",
		Args: json.RawMessage(`{"command":"git --no-pager log"}`),
	}
	result := ToolResult{Summary: "ran"}
	event := NewAuditEvent(now, tool, call, result, ApprovalApproved, nil)
	event.OriginalArgs = json.RawMessage(`{"command":"git status"}`)
	event.Rewritten = true

	if string(event.OriginalArgs) != `{"command":"git status"}` {
		t.Fatalf("OriginalArgs = %s", event.OriginalArgs)
	}
	if !event.Rewritten {
		t.Fatal("Rewritten should be true")
	}
	if string(event.Args) != `{"command":"git --no-pager log"}` {
		t.Fatalf("Args = %s", event.Args)
	}

	// Verify that a non-rewritten event has zero values.
	event2 := NewAuditEvent(now, tool, call, result, ApprovalApproved, nil)
	if event2.OriginalArgs != nil {
		t.Fatalf("OriginalArgs = %s, want nil", event2.OriginalArgs)
	}
	if event2.Rewritten {
		t.Fatal("Rewritten should be false")
	}
}

func TestNewAuditEventCopiesCommandExitCode(t *testing.T) {
	now := time.Unix(123, 0)
	exitCode := 2
	result := ToolResult{
		Summary:         "tests failed",
		CommandExitCode: &exitCode,
	}

	event := NewAuditEvent(now, testTool("shell.run"), ToolCall{Name: "shell.run"}, result, ApprovalNotRequired, nil)
	exitCode = 9

	if event.CommandExitCode == nil || *event.CommandExitCode != 2 {
		t.Fatalf("CommandExitCode = %#v, want independent copy with value 2", event.CommandExitCode)
	}
}

func TestNewAuditEventCopiesResultContent(t *testing.T) {
	now := time.Now()
	result := ToolResult{
		Summary: " Applied patches to: a.go",
		Content: "--- a.go\n+++ a.go\n@@ -1 +1 @@\n-old\n+new\n",
	}
	call := ToolCall{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"..."}`)}
	event := NewAuditEvent(now, Tool{Name: "file.write_patch"}, call, result, ApprovalApproved, nil)
	if event.ResultContent != result.Content {
		t.Fatalf("ResultContent = %q, want %q", event.ResultContent, result.Content)
	}
}

func TestNewAuditEventCopiesFilesChangedSlice(t *testing.T) {
	now := time.Unix(123, 0)
	result := ToolResult{
		Summary:      "changed files",
		FilesChanged: []string{"a.go"},
	}

	event := NewAuditEvent(now, testTool("file.write_patch"), ToolCall{Name: "file.write_patch"}, result, ApprovalNotRequired, nil)
	result.FilesChanged[0] = "mutated.go"

	if len(event.FilesChanged) != 1 || event.FilesChanged[0] != "a.go" {
		t.Fatalf("FilesChanged = %#v, want independent copy", event.FilesChanged)
	}
	if event.Error != "" {
		t.Fatalf("Error = %q, want empty", event.Error)
	}
}

func TestNewAuditEventCopiesNotice(t *testing.T) {
	now := time.Unix(123, 0)
	tests := []struct {
		name   string
		notice *ToolNotice
	}{
		{
			name: "nil notice stays nil",
		},
		{
			name: "notice with data",
			notice: &ToolNotice{
				Kind: NoticeOversizeFallback,
				Text: "showed head of oversized file",
				Data: map[string]any{"path": "big.go", "bytes": float64(9000001)},
			},
		},
		{
			name:   "notice without data",
			notice: &ToolNotice{Kind: NoticeCappedResults, Text: "capped at 50"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToolResult{Summary: "read big.go", Notice: tt.notice}

			event := NewAuditEvent(now, testTool("file.read"), ToolCall{Name: "file.read"}, result, ApprovalNotRequired, nil)

			if tt.notice == nil {
				if event.Notice != nil {
					t.Fatalf("Notice = %#v, want nil", event.Notice)
				}
				return
			}
			if event.Notice == nil {
				t.Fatal("Notice = nil, want copy")
			}
			if event.Notice.Kind != tt.notice.Kind {
				t.Fatalf("Notice.Kind = %q, want %q", event.Notice.Kind, tt.notice.Kind)
			}
			if event.Notice.Text != tt.notice.Text {
				t.Fatalf("Notice.Text = %q, want %q", event.Notice.Text, tt.notice.Text)
			}
			if len(event.Notice.Data) != len(tt.notice.Data) {
				t.Fatalf("Notice.Data = %#v, want %#v", event.Notice.Data, tt.notice.Data)
			}
			for k, v := range tt.notice.Data {
				if event.Notice.Data[k] != v {
					t.Fatalf("Notice.Data[%q] = %#v, want %#v", k, event.Notice.Data[k], v)
				}
			}
		})
	}
}

func TestNewAuditEventNoticeIsIndependentCopy(t *testing.T) {
	now := time.Unix(123, 0)
	notice := &ToolNotice{
		Kind: NoticeZeroMatchCoach,
		Text: "no matches for pattern",
		Data: map[string]any{"query": "foo"},
	}
	result := ToolResult{Summary: "searched", Notice: notice}

	event := NewAuditEvent(now, testTool("repo.search"), ToolCall{Name: "repo.search"}, result, ApprovalNotRequired, nil)

	// Mutate the source notice and its Data map after the event is built.
	notice.Kind = "mutated"
	notice.Text = "mutated"
	notice.Data["query"] = "mutated"
	notice.Data["added"] = true

	if event.Notice == nil {
		t.Fatal("Notice = nil, want copy")
	}
	if event.Notice.Kind != NoticeZeroMatchCoach {
		t.Fatalf("Notice.Kind = %q, want %q", event.Notice.Kind, NoticeZeroMatchCoach)
	}
	if event.Notice.Text != "no matches for pattern" {
		t.Fatalf("Notice.Text = %q, want unchanged", event.Notice.Text)
	}
	if len(event.Notice.Data) != 1 || event.Notice.Data["query"] != "foo" {
		t.Fatalf("Notice.Data = %#v, want independent copy", event.Notice.Data)
	}
}
