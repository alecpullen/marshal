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
