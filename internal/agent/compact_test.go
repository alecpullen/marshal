package agent

import (
	"strings"
	"testing"

	"marshal/internal/llm/schema"
)

func TestEstimateTokensCharsOverFour(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: strings.Repeat("a", 400)},
		{Role: schema.RoleUser, Content: strings.Repeat("b", 800)},
	}
	got := estimateTokens(msgs)
	if got != 300 {
		t.Errorf("estimateTokens = %d, want 300", got)
	}
}

func TestCompactMessagesUnderBudgetIsNoOp(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system"},
		{Role: schema.RoleUser, Content: "goal"},
		{Role: schema.RoleUser, Content: "Tool file.read result: short output"},
	}
	result := compactMessages(msgs, 1000, 6)
	if &result[0] == &msgs[0] {
		t.Error("compacted messages should be returned as-is when under budget")
	}
}

func TestCompactMessagesShrinksOldToolResultsOnly(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system"},
		{Role: schema.RoleUser, Content: "goal"},
		{Role: schema.RoleAssistant, Content: "calling tool"},
		{Role: schema.RoleUser, Content: "Tool file.read result: summary\n\n" + strings.Repeat("x", 4000)},
		{Role: schema.RoleAssistant, Content: "next action"},
		{Role: schema.RoleUser, Content: "Tool shell.run result: another\n\n" + strings.Repeat("y", 4000)},
		{Role: schema.RoleAssistant, Content: "reasoning"},
		{Role: schema.RoleUser, Content: "Tool grep.search result: recent"},
	}
	budget := 50
	result := compactMessages(msgs, budget, 2)

	// Old tool results get compacted only after old prose has been emptied.
	// The budget here is small enough that both phases run.
	if !strings.Contains(result[3].Content, compactedNote) {
		t.Error("old tool result at index 3 should be compacted")
	}
	if !strings.Contains(result[5].Content, compactedNote) {
		t.Error("old tool result at index 5 should be compacted")
	}
	if strings.Contains(result[7].Content, compactedNote) {
		t.Error("recent tool result at index 7 should NOT be compacted")
	}
	// Phase 1 drops old assistant prose first, before compacting tool
	// results — tool results are the facts the model needs to answer, so
	// they are preserved as long as possible.
	if result[2].Content != "" {
		t.Errorf("old assistant message at index 2 should be emptied before tool results, got %q", result[2].Content)
	}
	if result[4].Content != "" {
		t.Errorf("old assistant message at index 4 should be emptied before tool results, got %q", result[4].Content)
	}
	// The system prompt and goal are always preserved.
	if result[0].Content != "system" || result[1].Content != "goal" {
		t.Errorf("system/goal messages should not be modified: %+v", result[:2])
	}
}

// TestCompactMessagesDropsOldProseBeforeToolResults verifies the two-phase
// strategy: when over budget, old prose is emptied first, leaving tool
// results intact unless prose alone was insufficient.
func TestCompactMessagesDropsOldProseBeforeToolResults(t *testing.T) {
	// Two big assistant prose blocks + one tool result big enough to trigger
	// compaction, but a budget large enough that dropping prose alone fits.
	toolResult := "Tool file.read result: summary\n\n" + strings.Repeat("x", 4000)
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system"},
		{Role: schema.RoleUser, Content: "goal"},
		{Role: schema.RoleAssistant, Content: strings.Repeat("p", 4000)},
		{Role: schema.RoleUser, Content: toolResult},
		{Role: schema.RoleAssistant, Content: strings.Repeat("q", 4000)},
		{Role: schema.RoleAssistant, Content: "follow-up"},
		{Role: schema.RoleUser, Content: "Tool grep.search result: recent"},
	}
	// keepRecent=2 protects indices 5,6 (follow-up + recent tool result).
	// Compactable range is indices 2..4: two prose blocks and one tool
	// result. Budget is set just above what remains after emptying both
	// prose blocks, so phase 1 (drop prose) alone brings us under budget
	// and the tool result is never touched.
	toolTokens := estimateTokens([]schema.ChatMessage{{Content: toolResult}})
	// Remaining after dropping prose: system(2) + goal(1) + tool + follow-up(3) + recent(8) ~= tool+14
	budget := toolTokens + 20
	result := compactMessages(msgs, budget, 2)

	if strings.Contains(result[3].Content, compactedNote) {
		t.Errorf("tool result should be preserved when dropping prose suffices: %q", result[3].Content)
	}
	if result[2].Content != "" {
		t.Errorf("old assistant prose at index 2 should be emptied, got %q", result[2].Content)
	}
	if result[4].Content != "" {
		t.Errorf("old assistant prose at index 4 should be emptied, got %q", result[4].Content)
	}
}

func TestCompactMessagesDoesNotMutateInput(t *testing.T) {
	original := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system"},
		{Role: schema.RoleUser, Content: "goal"},
		{Role: schema.RoleUser, Content: "Tool file.read result: summary\n\n" + strings.Repeat("z", 4000)},
	}
	saved := original[2].Content
	_ = compactMessages(original, 10, 0)
	if original[2].Content != saved {
		t.Error("compactMessages mutated the input slice")
	}
}

func TestCompactMessagesShrinksRoleToolMessages(t *testing.T) {
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system"},
		{Role: schema.RoleUser, Content: "goal"},
		{Role: schema.RoleAssistant, Content: "calling tool"},
		{Role: schema.RoleTool, ToolCallID: "call_1", Content: "Tool file.read result: summary\n\n" + strings.Repeat("x", 4000)},
		{Role: schema.RoleAssistant, Content: "done"},
	}

	result := compactMessages(msgs, 20, 1)

	if result[3].Role != schema.RoleTool || result[3].ToolCallID != "call_1" {
		t.Fatalf("role/tool_call_id changed: %+v", result[3])
	}
	if !strings.Contains(result[3].Content, compactedNote) {
		t.Fatalf("role:tool message was not compacted: %q", result[3].Content)
	}
}

// TestCompactMessagesPreservesAssistantToolCallPairing verifies that phase 1
// never zeroes the ToolCalls of an assistant message that is followed by
// role:tool results referencing those tool_call_ids. OpenAI-compatible
// providers reject requests where a role:tool message has no matching
// preceding assistant tool_calls entry.
func TestCompactMessagesPreservesAssistantToolCallPairing(t *testing.T) {
	toolResult := "Tool file.read result: summary\n\n" + strings.Repeat("x", 4000)
	msgs := []schema.ChatMessage{
		{Role: schema.RoleSystem, Content: "system"},
		{Role: schema.RoleUser, Content: "goal"},
		{Role: schema.RoleAssistant, Content: strings.Repeat("p", 4000)}, // pure prose, compactable
		{Role: schema.RoleAssistant, Content: "calling", ToolCalls: []schema.ToolCall{{ID: "call_1", Name: "file.read"}}},
		{Role: schema.RoleTool, ToolCallID: "call_1", Content: toolResult},
		{Role: schema.RoleAssistant, Content: "follow-up"},
		{Role: schema.RoleUser, Content: "Tool grep.search result: recent"},
	}
	// keepRecent=2 protects indices 5,6. Compactable range 2..4: pure prose
	// at 2, the ToolCalls assistant at 3, and the tool result at 4. Budget is
	// just above the tool result alone, so emptying the pure-prose block at
	// index 2 brings us under budget and neither the ToolCalls assistant nor
	// the tool result is touched.
	toolTokens := estimateTokens([]schema.ChatMessage{{Content: toolResult}})
	budget := toolTokens + 20
	result := compactMessages(msgs, budget, 2)

	if len(result[3].ToolCalls) == 0 || result[3].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant ToolCalls at index 3 were zeroed, orphaning role:tool call_1: %+v", result[3])
	}
	if result[4].Role != schema.RoleTool || result[4].ToolCallID != "call_1" {
		t.Fatalf("role:tool pairing changed: %+v", result[4])
	}
	if strings.Contains(result[4].Content, compactedNote) {
		t.Fatalf("tool result should be preserved when dropping prose suffices: %q", result[4].Content)
	}
	if result[2].Content != "" {
		t.Errorf("pure-prose assistant at index 2 should be emptied, got %q", result[2].Content)
	}
}
