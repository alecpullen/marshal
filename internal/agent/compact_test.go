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
