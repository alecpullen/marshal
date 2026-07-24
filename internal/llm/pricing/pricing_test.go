package pricing

import (
	"testing"

	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func TestLookupBuiltInTable(t *testing.T) {
	p := Lookup(routing.ModelPreset{Model: "gpt-4o"})
	if p.InputPerMTokCents == 0 {
		t.Error("gpt-4o should have a non-zero input price in the built-in table")
	}
}

func TestLookupLocalModelIsZero(t *testing.T) {
	p := Lookup(routing.ModelPreset{Model: "qwen2.5-coder:14b", Provider: "ollama"})
	if p.InputPerMTokCents != 0 || p.OutputPerMTokCents != 0 {
		t.Errorf("local model should be zero-priced: %+v", p)
	}
}

func TestLookupConfigOverrideWins(t *testing.T) {
	cfg := routing.ModelPreset{
		Model: "gpt-4o",
		Pricing: &ModelPricing{
			InputPerMTokCents:  999,
			OutputPerMTokCents: 999,
		},
	}
	p := Lookup(cfg)
	if p.InputPerMTokCents != 999 {
		t.Errorf("config override should win: got InputPerMTokCents=%d, want 999", p.InputPerMTokCents)
	}
}

func TestEstimateCostCents(t *testing.T) {
	p := ModelPricing{
		InputPerMTokCents:      250,  // $2.50/M
		OutputPerMTokCents:     1000, // $10.00/M
		ReasoningPerMTokCents:  1000,
		CacheReadPerMTokCents:  125, // $1.25/M
		CacheWritePerMTokCents: 300, // $3.00/M
	}
	u := schema.TokenUsage{
		PromptTokens:     1_000_000, // 1M input -> 250 cents
		CompletionTokens: 500_000,   // 0.5M output -> 500 cents
		ReasoningTokens:  100_000,   // 0.1M reasoning -> 100 cents
		CacheReadTokens:  200_000,   // 0.2M cache read -> 25 cents
		CacheWriteTokens: 100_000,   // 0.1M cache write -> 30 cents
	}
	got := EstimateCostCents(u, p)
	want := int64(250 + 500 + 100 + 25 + 30) // 905 cents = $9.05
	if got != want {
		t.Errorf("EstimateCostCents = %d, want %d", got, want)
	}
}

func TestEstimateCostCentsZeroPricing(t *testing.T) {
	p := ModelPricing{} // all zero
	u := schema.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	if got := EstimateCostCents(u, p); got != 0 {
		t.Errorf("zero pricing should give zero cost: got %d", got)
	}
}

func TestEstimateCostCentsSubCentTruncates(t *testing.T) {
	p := ModelPricing{InputPerMTokCents: 250} // $2.50/M
	u := schema.TokenUsage{PromptTokens: 100} // 100 tokens -> 0.025 cents -> truncates to 0
	if got := EstimateCostCents(u, p); got != 0 {
		t.Errorf("sub-cent should truncate to 0: got %d", got)
	}
}
