package agent

import (
	"encoding/json"
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

func TestEstimateTokensRuneAware(t *testing.T) {
	// Each emoji is 4 bytes but 1 rune. "😀😀" = 8 bytes, 2 runes.
	// Byte-based: 8/4 = 2 tokens. Rune-based: 2/4 = 0 tokens (integer division).
	// Use enough runes to get a non-zero result: 8 emoji = 8 runes = 2 tokens.
	msg := []schema.ChatMessage{
		{Role: schema.RoleUser, Content: "😀😀😀😀😀😀😀😀"},
	}
	got := estimateTokens(msg)
	// Rune-based: 8/4 = 2. Byte-based: 32/4 = 8.
	if got == 8 {
		t.Fatalf("estimateTokens appears byte-based: got %d for 8 runes/32 bytes, want 2", got)
	}
	if got != 2 {
		t.Fatalf("estimateTokens = %d, want 2 (8 runes / 4)", got)
	}
}

func TestEstimateTokensCountsToolCallArgs(t *testing.T) {
	args := strings.Repeat("x", 4000) // 4000 bytes ≈ 1000 tokens at runes/4
	msgs := []schema.ChatMessage{{
		Role:    schema.RoleAssistant,
		Content: "",
		ToolCalls: []schema.ToolCall{
			{ID: "call_1", Name: "file.write", Args: json.RawMessage(args)},
		},
	}}
	got := estimateTokens(msgs)
	want := (4000 + len("call_1") + len("file.write")) / 4
	if got != want {
		t.Fatalf("estimateTokens = %d, want %d (args + name + ID counted)", got, want)
	}
}

func TestEstimateTokensCountsToolCallIDOnResults(t *testing.T) {
	msgs := []schema.ChatMessage{{
		Role:       schema.RoleTool,
		Content:    strings.Repeat("y", 100),
		ToolCallID: "call_1",
	}}
	got := estimateTokens(msgs)
	want := (100 + len("call_1")) / 4
	if got != want {
		t.Fatalf("estimateTokens = %d, want %d (ToolCallID counted)", got, want)
	}
}
