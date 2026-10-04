package pricing

import (
	"bytes"
	"log/slog"
	"testing"

	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func TestLookupBuiltInTable(t *testing.T) {
	p := Lookup(routing.ModelPreset{Model: "gpt-4o"}, nil)
	if p.InputPerMTokCents == 0 {
		t.Error("gpt-4o should have a non-zero input price in the built-in table")
	}
}

func TestLookupLocalModelIsZero(t *testing.T) {
	p := Lookup(routing.ModelPreset{Model: "qwen2.5-coder:14b", Provider: "ollama"}, nil)
	if p.InputPerMTokCents != 0 || p.OutputPerMTokCents != 0 {
		t.Errorf("local model should be zero-priced: %+v", p)
	}
}

func TestLookupUnknownModelWithNilLoggerNoWarn(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	preset := routing.ModelPreset{Provider: "test", Model: "unknown-model-xyz"}
	p := Lookup(preset, nil)
	if p.InputPerMTokCents != 0 {
		t.Errorf("unknown model should have zero pricing, got %+v", p)
	}
	// With nil logger, no warning should reach the default handler.
	if buf.Len() > 0 {
		t.Errorf("expected no log output with nil logger, got: %s", buf.String())
	}
}

func TestLookupUnknownModelWithLoggerWarns(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	preset := routing.ModelPreset{Provider: "test", Model: "unknown-model-xyz"}
	p := Lookup(preset, logger)
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
	p := Lookup(cfg, nil)
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

// TestEstimateCostCentsUnit pins the unit: rates are US cents per million
// tokens and the result is whole US cents, so 1M prompt tokens at
// InputPerMTokCents=250 ($2.50/M) cost 250 cents, which is $2.50.
func TestEstimateCostCentsUnit(t *testing.T) {
	p := ModelPricing{InputPerMTokCents: 250}
	got := EstimateCostCents(schema.TokenUsage{PromptTokens: 1_000_000}, p)
	if got != 250 {
		t.Fatalf("EstimateCostCents = %d, want 250 cents", got)
	}
	if usd := float64(got) / 100; usd != 2.5 {
		t.Fatalf("dollars = %v, want 2.5", usd)
	}
}

func TestEstimateCostMicroUSD(t *testing.T) {
	p := ModelPricing{InputPerMTokCents: 15, OutputPerMTokCents: 60} // $0.15/M in, $0.60/M out
	u := schema.TokenUsage{PromptTokens: 20_000, CompletionTokens: 1_000}
	// 20k * $0.15/M = $0.003 = 3000 micro-dollars; 1k * $0.60/M = 600.
	if got := EstimateCostMicroUSD(u, p); got != 3600 {
		t.Fatalf("EstimateCostMicroUSD = %d, want 3600", got)
	}
	// The whole-cent estimate truncates the same turn to 0.
	if got := EstimateCostCents(u, p); got != 0 {
		t.Fatalf("EstimateCostCents = %d, want 0", got)
	}
	// 1M tokens at 250 cents/M = $2.50 = 2,500,000 micro-dollars.
	if got := EstimateCostMicroUSD(schema.TokenUsage{PromptTokens: 1_000_000}, ModelPricing{InputPerMTokCents: 250}); got != 2_500_000 {
		t.Fatalf("1M tokens = %d", got)
	}
}
