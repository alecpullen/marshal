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
