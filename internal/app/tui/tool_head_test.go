package tui

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestToolRowHeadIsTheUntruncatedCollapsedRow(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v"}`), ResultSummary: "exit 0"}
	head := toolRowHead(ev, "")
	if wide := stripANSI(renderCompletedToolCallIn(ev, false, nil, 400, "")); !strings.Contains(wide, head) {
		t.Fatalf("a wide row %q does not show the head %q", wide, head)
	}
	narrow := strings.TrimRight(stripANSI(renderCompletedToolCallIn(ev, false, nil, 40, "")), " \n")
	if !strings.HasSuffix(narrow, "…") || strings.Contains(narrow, head) {
		t.Fatalf("a narrow row %q should be the head truncated", narrow)
	}
}

func TestToolGroupHeadIsTheUntruncatedCollapsedRow(t *testing.T) {
	evs := []registry.AuditEvent{
		{ToolName: "search.text", Args: []byte(`{"query":"TestPreToolUseBlockDecision"}`)},
		{ToolName: "search.text", Args: []byte(`{"query":"testHookTimeoutMS"}`)},
	}
	head := toolGroupHead(evs)
	if wide := stripANSI(renderToolGroup(evs, false, 400)); !strings.Contains(wide, head) {
		t.Fatalf("a wide group row %q does not show the head %q", wide, head)
	}
	narrow := strings.TrimRight(stripANSI(renderToolGroup(evs, false, 30)), " \n")
	if !strings.HasSuffix(narrow, "…") || strings.Contains(narrow, head) {
		t.Fatalf("a narrow group row %q should be the head truncated", narrow)
	}
}
