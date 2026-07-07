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

	if !strings.Contains(result[3].Content, compactedNote) {
		t.Error("old tool result at index 3 should be compacted")
	}
	if !strings.Contains(result[5].Content, compactedNote) {
		t.Error("old tool result at index 5 should be compacted")
	}
	if strings.Contains(result[7].Content, compactedNote) {
		t.Error("recent tool result at index 7 should NOT be compacted")
	}
	if !strings.HasPrefix(result[2].Content, "calling tool") {
		t.Error("assistant messages should not be modified")
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
