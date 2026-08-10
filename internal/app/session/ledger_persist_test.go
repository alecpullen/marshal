package session

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

// TestLogToolCallPersistsLedgerOnAssistantFinal ensures that an
// assistant-final message flushes the in-turn tool audit buffer, while
// a non-final message (e.g. a planning message) does NOT. Persistence
// is exercised end-to-end in a separate db_test; here we just verify
// the buffer plumbing so the network effect of the flush hook is
// locked down.
func TestLogToolCallCapturesAgentRunContent(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.LogToolCall(registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "review",
		ResultContent: "Looks good.",
		Approval:      registry.ApprovalApproved,
	})
	if len(s.toolAuditThisTurn) != 1 {
		t.Fatalf("expected one buffered entry, got %d", len(s.toolAuditThisTurn))
	}
	if s.toolAuditThisTurn[0].Content != "Looks good." {
		t.Fatalf("content = %q, want Looks good.", s.toolAuditThisTurn[0].Content)
	}
}

func TestLogToolCallCapsAgentRunContent(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	long := strings.Repeat("x", subagentReportCap+100)
	s.LogToolCall(registry.AuditEvent{
		ToolName:      "agent.run",
		ResultSummary: "review",
		ResultContent: long,
		Approval:      registry.ApprovalApproved,
	})
	if len(s.toolAuditThisTurn) != 1 {
		t.Fatalf("expected one buffered entry, got %d", len(s.toolAuditThisTurn))
	}
	if len(s.toolAuditThisTurn[0].Content) > subagentReportCap {
		t.Fatalf("content length %d exceeds cap %d", len(s.toolAuditThisTurn[0].Content), subagentReportCap)
	}
	if !strings.HasPrefix(s.toolAuditThisTurn[0].Content, "xxxx") {
		t.Fatal("truncation removed the wrong end")
	}
}

func TestLogToolCallPersistsLedgerOnAssistantFinal(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          100,
		MaxTotalTokens:      100000,
		MaxEntryTokens:      100000,
		ProjectionMaxTokens: 1000,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})

	// Two tool calls must accumulate into the buffer.
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
	if n := len(s.toolAuditThisTurn); n != 2 {
		t.Fatalf("after tool calls, toolAuditThisTurn len = %d, want 2", n)
	}

	// A non-final assistant message must NOT flush yet.
	s.AddMessage(RoleAssistant, "draft text", ContentTypeMarkdown)
	if n := len(s.toolAuditThisTurn); n != 2 {
		t.Fatalf("after non-final message, toolAuditThisTurn len = %d, want 2", n)
	}

	// The final assistant message flushes.
	s.AddMessageFinal(RoleAssistant, "final answer", ContentTypeMarkdown)
	if n := len(s.toolAuditThisTurn); n != 0 {
		t.Fatalf("after final message, toolAuditThisTurn len = %d, want 0", n)
	}

	// Two consecutive turns, each gets its own ledger buffer.
	s.LogToolCall(registry.AuditEvent{
		ToolName:      "file.read",
		ResultSummary: "x.go",
		Approval:      registry.ApprovalApproved,
	})
	if n := len(s.toolAuditThisTurn); n != 1 {
		t.Fatalf("next-turn buffer = %d, want 1", n)
	}
}

// TestLedgerSummaryFallbackInToolAudit covers the ResultSummary-derived
// ledger summary: an empty summary falls back to the tool name so the
// ledger never drops an entry silently.
func TestLedgerSummaryFallbackInToolAudit(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          100,
		MaxTotalTokens:      100000,
		MaxEntryTokens:      100000,
		ProjectionMaxTokens: 1000,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})

	s.LogToolCall(registry.AuditEvent{ToolName: "shell.run", Approval: registry.ApprovalApproved})
	if len(s.toolAuditThisTurn) != 1 {
		t.Fatalf("expected one buffered entry, got %d", len(s.toolAuditThisTurn))
	}
	if got := s.toolAuditThisTurn[0].Summary; got != "shell.run" {
		t.Fatalf("fallback summary = %q, want %q", got, "shell.run")
	}
}
