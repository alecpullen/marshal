package schema

import "testing"

func TestTokenUsageZeroValue(t *testing.T) {
	var u TokenUsage
	if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 {
		t.Errorf("zero-value TokenUsage should be all-zero: %+v", u)
	}
	if u.ReasoningTokens != 0 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0 {
		t.Errorf("new fields should be zero by default: %+v", u)
	}
}

func TestTokenUsageNewFieldsSettable(t *testing.T) {
	u := TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		ReasoningTokens:  20,
		CacheReadTokens:  30,
		CacheWriteTokens: 10,
	}
	if u.ReasoningTokens != 20 {
		t.Errorf("ReasoningTokens = %d, want 20", u.ReasoningTokens)
	}
	if u.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30", u.CacheReadTokens)
	}
	if u.CacheWriteTokens != 10 {
		t.Errorf("CacheWriteTokens = %d, want 10", u.CacheWriteTokens)
	}
}
