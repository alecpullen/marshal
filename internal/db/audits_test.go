package db

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func TestSaveAndGetToolCalls(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "session-audit"
	if err := db.CreateSession(sessionID, projectID, "audit test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	args, err := json.Marshal(map[string]string{"command": "go test"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	exitCode := 0
	event := registry.AuditEvent{
		Timestamp:       time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
		AgentRole:       "implementer",
		Model:           "test-model",
		ToolName:        "shell.exec",
		Args:            args,
		Risk:            registry.RiskCommand,
		Approval:        registry.ApprovalApproved,
		ResultSummary:   "tests passed",
		FilesChanged:    []string{"foo.go"},
		CommandExitCode: &exitCode,
		Error:           "",
	}

	if err := db.SaveToolCall(sessionID, event); err != nil {
		t.Fatalf("SaveToolCall failed: %v", err)
	}

	calls, err := db.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("GetToolCalls failed: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	got := calls[0]
	if got.ToolName != "shell.exec" || got.Model != "test-model" || got.AgentRole != "implementer" {
		t.Errorf("tool call metadata mismatch: %+v", got)
	}
	if got.Approval != registry.ApprovalApproved {
		t.Errorf("expected approval approved, got %s", got.Approval)
	}
	if string(got.Args) != string(args) {
		t.Errorf("args mismatch: %s vs %s", string(got.Args), string(args))
	}
	if got.CommandExitCode == nil || *got.CommandExitCode != 0 {
		t.Errorf("expected exit code 0, got %v", got.CommandExitCode)
	}
	if got.Error != event.Error {
		t.Errorf("expected error %q, got %q", event.Error, got.Error)
	}

	t.Run("with error", func(t *testing.T) {
		errArgs, err := json.Marshal(map[string]string{"command": "go fail"})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		errEvent := registry.AuditEvent{
			Timestamp:     time.Date(2026, 7, 2, 12, 1, 0, 0, time.UTC),
			AgentRole:     "implementer",
			Model:         "test-model",
			ToolName:      "shell.exec",
			Args:          errArgs,
			Risk:          registry.RiskCommand,
			Approval:      registry.ApprovalApproved,
			ResultSummary: "command failed",
			FilesChanged:  []string{},
			Error:         "exit status 1",
		}

		if err := db.SaveToolCall(sessionID, errEvent); err != nil {
			t.Fatalf("SaveToolCall failed: %v", err)
		}

		calls, err := db.GetToolCalls(sessionID)
		if err != nil {
			t.Fatalf("GetToolCalls failed: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("expected 2 tool calls, got %d", len(calls))
		}

		got := calls[1]
		if got.ToolName != "shell.exec" {
			t.Errorf("tool name mismatch: got %q", got.ToolName)
		}
		if got.Error != errEvent.Error {
			t.Errorf("expected error %q, got %q", errEvent.Error, got.Error)
		}
	})
}
