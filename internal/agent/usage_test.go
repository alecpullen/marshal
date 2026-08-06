package agent

import (
	"testing"

	"marshal/internal/db"
)

func TestUsageAggregatorAccumulates(t *testing.T) {
	a := NewUsageAggregator()

	// Two gpt-4o turns, one ollama local turn
	a.Observe(TurnMetrics{
		Provider:           "openai",
		Model:              "gpt-4o",
		PromptTokens:       100,
		CompletionTokens:   50,
		ReasoningTokens:    0,
		CacheReadTokens:    10,
		CacheWriteTokens:   5,
		EstimatedCostCents: 200,
	})
	a.Observe(TurnMetrics{
		Provider:           "openai",
		Model:              "gpt-4o",
		PromptTokens:       200,
		CompletionTokens:   100,
		ReasoningTokens:    0,
		CacheReadTokens:    0,
		CacheWriteTokens:   0,
		EstimatedCostCents: 400,
	})
	a.Observe(TurnMetrics{
		Provider:           "ollama",
		Model:              "llama3",
		PromptTokens:       50,
		CompletionTokens:   30,
		ReasoningTokens:    0,
		CacheReadTokens:    0,
		CacheWriteTokens:   0,
		EstimatedCostCents: 0,
	})

	totals, breakdowns := a.Snapshot()

	// Check totals
	if totals.Turns != 3 {
		t.Fatalf("totals.Turns = %d, want 3", totals.Turns)
	}
	if totals.PromptTokens != 350 {
		t.Fatalf("totals.PromptTokens = %d, want 350", totals.PromptTokens)
	}
	if totals.CompletionTokens != 180 {
		t.Fatalf("totals.CompletionTokens = %d, want 180", totals.CompletionTokens)
	}
	if totals.ReasoningTokens != 0 {
		t.Fatalf("totals.ReasoningTokens = %d, want 0", totals.ReasoningTokens)
	}
	if totals.CacheReadTokens != 10 {
		t.Fatalf("totals.CacheReadTokens = %d, want 10", totals.CacheReadTokens)
	}
	if totals.CacheWriteTokens != 5 {
		t.Fatalf("totals.CacheWriteTokens = %d, want 5", totals.CacheWriteTokens)
	}
	if totals.EstimatedCostCents != 600 {
		t.Fatalf("totals.EstimatedCostCents = %d, want 600", totals.EstimatedCostCents)
	}

	// Check breakdowns: should be sorted by cost descending, then provider+model as tie-breaker
	if len(breakdowns) != 2 {
		t.Fatalf("len(breakdowns) = %d, want 2", len(breakdowns))
	}

	// First breakdown: openai/gpt-4o (cost 600)
	b0 := breakdowns[0]
	if b0.Provider != "openai" || b0.Model != "gpt-4o" {
		t.Fatalf("breakdowns[0] = %s/%s, want openai/gpt-4o", b0.Provider, b0.Model)
	}
	if b0.Turns != 2 {
		t.Fatalf("openai/gpt-4o Turns = %d, want 2", b0.Turns)
	}
	if b0.PromptTokens != 300 {
		t.Fatalf("openai/gpt-4o PromptTokens = %d, want 300", b0.PromptTokens)
	}
	if b0.CompletionTokens != 150 {
		t.Fatalf("openai/gpt-4o CompletionTokens = %d, want 150", b0.CompletionTokens)
	}
	if b0.CacheReadTokens != 10 {
		t.Fatalf("openai/gpt-4o CacheReadTokens = %d, want 10", b0.CacheReadTokens)
	}
	if b0.CacheWriteTokens != 5 {
		t.Fatalf("openai/gpt-4o CacheWriteTokens = %d, want 5", b0.CacheWriteTokens)
	}
	if b0.EstimatedCostCents != 600 {
		t.Fatalf("openai/gpt-4o EstimatedCostCents = %d, want 600", b0.EstimatedCostCents)
	}
	if b0.Reported&db.CatPrompt == 0 {
		t.Fatal("openai/gpt-4o CatPrompt not set")
	}
	if b0.Reported&db.CatCompletion == 0 {
		t.Fatal("openai/gpt-4o CatCompletion not set")
	}
	if b0.Reported&db.CatCacheRead == 0 {
		t.Fatal("openai/gpt-4o CatCacheRead not set")
	}
	if b0.Reported&db.CatCacheWrite == 0 {
		t.Fatal("openai/gpt-4o CatCacheWrite not set")
	}

	// Second breakdown: ollama/llama3 (cost 0)
	b1 := breakdowns[1]
	if b1.Provider != "ollama" || b1.Model != "llama3" {
		t.Fatalf("breakdowns[1] = %s/%s, want ollama/llama3", b1.Provider, b1.Model)
	}
	if b1.Turns != 1 {
		t.Fatalf("ollama/llama3 Turns = %d, want 1", b1.Turns)
	}
	if b1.PromptTokens != 50 {
		t.Fatalf("ollama/llama3 PromptTokens = %d, want 50", b1.PromptTokens)
	}
	if b1.CompletionTokens != 30 {
		t.Fatalf("ollama/llama3 CompletionTokens = %d, want 30", b1.CompletionTokens)
	}
	if b1.EstimatedCostCents != 0 {
		t.Fatalf("ollama/llama3 EstimatedCostCents = %d, want 0", b1.EstimatedCostCents)
	}
	if b1.Reported&db.CatPrompt == 0 {
		t.Fatal("ollama/llama3 CatPrompt not set")
	}
	if b1.Reported&db.CatCompletion == 0 {
		t.Fatal("ollama/llama3 CatCompletion not set")
	}
}

func TestUsageAggregatorCapabilityTracking(t *testing.T) {
	a := NewUsageAggregator()

	// A turn with reasoning + cache read, but no cache write
	a.Observe(TurnMetrics{
		Provider:           "openai",
		Model:              "o1",
		PromptTokens:       50,
		CompletionTokens:   30,
		ReasoningTokens:    20,
		CacheReadTokens:    5,
		CacheWriteTokens:   0,
		EstimatedCostCents: 100,
	})

	_, breakdowns := a.Snapshot()
	if len(breakdowns) != 1 {
		t.Fatalf("len(breakdowns) = %d, want 1", len(breakdowns))
	}
	b := breakdowns[0]

	if b.Reported&db.CatPrompt == 0 {
		t.Fatal("CatPrompt not set")
	}
	if b.Reported&db.CatCompletion == 0 {
		t.Fatal("CatCompletion not set")
	}
	if b.Reported&db.CatReasoning == 0 {
		t.Fatal("CatReasoning not set")
	}
	if b.Reported&db.CatCacheRead == 0 {
		t.Fatal("CatCacheRead not set")
	}
	if b.Reported&db.CatCacheWrite != 0 {
		t.Fatal("CatCacheWrite should not be set")
	}
}

func TestUsageAggregatorLocalModelNoCapabilities(t *testing.T) {
	a := NewUsageAggregator()

	// A local model turn with no reasoning or cache
	a.Observe(TurnMetrics{
		Provider:           "ollama",
		Model:              "llama3.2",
		PromptTokens:       10,
		CompletionTokens:   20,
		ReasoningTokens:    0,
		CacheReadTokens:    0,
		CacheWriteTokens:   0,
		EstimatedCostCents: 0,
	})

	_, breakdowns := a.Snapshot()
	if len(breakdowns) != 1 {
		t.Fatalf("len(breakdowns) = %d, want 1", len(breakdowns))
	}
	b := breakdowns[0]

	if b.Reported&db.CatPrompt == 0 {
		t.Fatal("CatPrompt not set")
	}
	if b.Reported&db.CatCompletion == 0 {
		t.Fatal("CatCompletion not set")
	}
	if b.Reported&db.CatReasoning != 0 {
		t.Fatal("CatReasoning should not be set for local model")
	}
	if b.Reported&db.CatCacheRead != 0 {
		t.Fatal("CatCacheRead should not be set for local model")
	}
	if b.Reported&db.CatCacheWrite != 0 {
		t.Fatal("CatCacheWrite should not be set for local model")
	}
}

// TestTurnUsageLineFormatsProviderUsage: the per-turn usage line attached
// to final assistant messages (for the session export's .usage block) must
// compact token counts and stay empty when the provider reported nothing
// (session postmortem 2026-08-06, finding 3).
func TestTurnUsageLineFormatsProviderUsage(t *testing.T) {
	r := &Runner{}
	if got := r.turnUsageLine(); got != "" {
		t.Fatalf("nil stats: turnUsageLine() = %q, want empty", got)
	}

	r.stats = &turnStats{}
	if got := r.turnUsageLine(); got != "" {
		t.Fatalf("zero usage: turnUsageLine() = %q, want empty", got)
	}

	r.stats = &turnStats{m: TurnMetrics{PromptTokens: 12345, CompletionTokens: 340}}
	if got := r.turnUsageLine(); got != "12k prompt + 340 completion tokens" {
		t.Fatalf("turnUsageLine() = %q, want %q", got, "12k prompt + 340 completion tokens")
	}
}
