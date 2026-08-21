package pricing

import (
	"bytes"
	"log/slog"
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

func TestLookupUnknownModelLogsWarning(t *testing.T) {
	// Capture slog output.
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	preset := routing.ModelPreset{Provider: "test", Model: "unknown-model-xyz"}
	p := Lookup(preset)
	if p.InputPerMTokCents != 0 {
		t.Errorf("unknown model should have zero pricing, got %+v", p)
	}
	if !bytes.Contains(buf.Bytes(), []byte("unknown-model-xyz")) {
		t.Errorf("expected warning log mentioning model name, got: %s", buf.String())
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
		PromptTokens:     1_000_000,
		CompletionTokens: 500_000,
		ReasoningTokens:  100_000,
		CacheReadTokens:  200_000,
		CacheWriteTokens: 100_000,
	}
	got := EstimateCostCents(u, p)
	// non-cached prompt = 700_000 * 250 / 1_000_000 = 175
	// non-reasoning completion = 400_000 * 1000 / 1_000_000 = 400
	// reasoning = 100_000 * 1000 / 1_000_000 = 100
	// cache read = 200_000 * 125 / 1_000_000 = 25
	// cache write = 100_000 * 300 / 1_000_000 = 30
	// total = 730
	want := int64(730)
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
