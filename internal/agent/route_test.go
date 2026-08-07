package agent

import (
	"testing"

	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/routing"
)

func TestResolveModelLimitsPrefersPreset(t *testing.T) {
	r := &Runner{}
	window, maxOut := r.resolveModelLimits(routing.ModelPreset{
		Provider: "anthropic", Model: "claude-sonnet-4-5",
		ContextWindow: 111, MaxOutputTokens: 222,
	})
	if window != 111 || maxOut != 222 {
		t.Fatalf("got (%d, %d), want the preset's (111, 222)", window, maxOut)
	}
}

func TestResolveModelLimitsUsesTable(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{
		"anthropic/claude-sonnet-4.5": {ContextWindow: 200000, MaxOutputTokens: 64000},
	})
	r := &Runner{LimitsTable: &tbl}
	window, maxOut := r.resolveModelLimits(routing.ModelPreset{
		Provider: "anthropic", Model: "claude-sonnet-4-5-20250929",
	})
	if window != 200000 || maxOut != 64000 {
		t.Fatalf("got (%d, %d), want (200000, 64000) via the variant match", window, maxOut)
	}
}

func TestResolveModelLimitsFallsBackToCatalog(t *testing.T) {
	tbl := limits.NewTable(map[string]limits.Limit{})
	r := &Runner{LimitsTable: &tbl}
	window, _ := r.resolveModelLimits(routing.ModelPreset{
		Provider: "ollama", Model: "qwen2.5-coder:7b",
	})
	if window != 32768 {
		t.Fatalf("window = %d, want 32768 from the local catalog", window)
	}
}

func TestResolveModelLimitsNilTableDoesNotPanic(t *testing.T) {
	r := &Runner{}
	window, maxOut := r.resolveModelLimits(routing.ModelPreset{
		Provider: "openai", Model: "totally-made-up-xyz",
	})
	if window != 0 || maxOut != 0 {
		t.Fatalf("got (%d, %d), want (0, 0) for an unknown model", window, maxOut)
	}
}
