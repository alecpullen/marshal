package session

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

func TestTakeToolAuditForInterruptRendersAndClears(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.LogToolCall(registry.AuditEvent{
		ToolName:      "file.read",
		ResultSummary: "a.go:1-80 (412t)",
		Approval:      registry.ApprovalApproved,
	})
	s.LogToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ResultSummary: "go test ./...",
		Approval:      registry.ApprovalApproved,
	})

	got := s.TakeToolAuditForInterrupt()
	if !strings.Contains(got, "file.read") || !strings.Contains(got, "shell.run") {
		t.Fatalf("TakeToolAuditForInterrupt = %q, want both tools", got)
	}
	if n := len(s.toolAuditThisTurn); n != 0 {
		t.Fatalf("buffer not cleared after TakeToolAuditForInterrupt, len = %d", n)
	}
	// Second call returns "" (already cleared).
	if again := s.TakeToolAuditForInterrupt(); again != "" {
		t.Fatalf("second TakeToolAuditForInterrupt = %q, want empty", again)
	}
}

func TestTakeToolAuditForInterruptEmpty(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	if got := s.TakeToolAuditForInterrupt(); got != "" {
		t.Fatalf("TakeToolAuditForInterrupt on empty buffer = %q, want empty", got)
	}
}

func TestInterruptedTurnNoteOneShot(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	if got := s.TakeInterruptedTurnNote(); got != "" {
		t.Fatalf("TakeInterruptedTurnNote with no note = %q, want empty", got)
	}
	s.SetInterruptedTurnNote("Turn interrupted.")
	if got := s.TakeInterruptedTurnNote(); got != "Turn interrupted." {
		t.Fatalf("TakeInterruptedTurnNote = %q, want the note", got)
	}
	// One-shot: consumed and cleared.
	if got := s.TakeInterruptedTurnNote(); got != "" {
		t.Fatalf("TakeInterruptedTurnNote after consume = %q, want empty", got)
	}
}
