package connect

import (
	"testing"

	"marshal/internal/llm/catalog"
	"marshal/internal/llm/schema"
)

func TestResolveLimitsPrefersDiscovery(t *testing.T) {
	got := resolveLimits([]schema.ModelInfo{
		{ID: "gpt-4o", ContextWindow: 128000, MaxOutputTokens: 16384},
	}, "gpt-4o")

	if got.ContextWindow != 128000 || got.MaxOutputTokens != 16384 {
		t.Errorf("got %+v, want the discovered figures", got)
	}
	if got.ContextSource != SourceFetched || got.OutputSource != SourceFetched {
		t.Errorf("sources = %q/%q, want both %q", got.ContextSource, got.OutputSource, SourceFetched)
	}
}

func TestResolveLimitsFallsBackToCatalogPerField(t *testing.T) {
	// Discovery knows the context window but not the output cap.
	got := resolveLimits([]schema.ModelInfo{
		{ID: "gpt-4o", ContextWindow: 128000, MaxOutputTokens: 0},
	}, "gpt-4o")

	if got.ContextSource != SourceFetched {
		t.Errorf("ContextSource = %q, want %q", got.ContextSource, SourceFetched)
	}
	catCtx, catOut := catalogFor(t, "gpt-4o")
	_ = catCtx
	if catOut > 0 {
		if got.MaxOutputTokens != catOut || got.OutputSource != SourceCatalog {
			t.Errorf("output = %d/%q, want %d/%q", got.MaxOutputTokens, got.OutputSource, catOut, SourceCatalog)
		}
	} else if got.OutputSource != SourceUnknown {
		t.Errorf("OutputSource = %q, want %q when the catalog has nothing", got.OutputSource, SourceUnknown)
	}
}

func TestResolveLimitsUnknownModel(t *testing.T) {
	got := resolveLimits(nil, "some-self-hosted-thing-v3")

	if got.ContextWindow != 0 || got.MaxOutputTokens != 0 {
		t.Errorf("got %+v, want zeroes for an unknown model", got)
	}
	if got.ContextSource != SourceUnknown || got.OutputSource != SourceUnknown {
		t.Errorf("sources = %q/%q, want both %q", got.ContextSource, got.OutputSource, SourceUnknown)
	}
}

func TestResolveLimitsIgnoresOtherModels(t *testing.T) {
	got := resolveLimits([]schema.ModelInfo{
		{ID: "gpt-4o", ContextWindow: 128000, MaxOutputTokens: 16384},
	}, "llama-3.3-70b")

	if got.ContextSource == SourceFetched {
		t.Error("matched the wrong model's limits")
	}
}

func TestResolveLimitsMarksCatalogSource(t *testing.T) {
	// A model the local catalog knows but discovery did not report.
	lim := resolveLimits(nil, "qwen2.5-coder:7b")
	if lim.ContextWindow != 32768 {
		t.Errorf("ContextWindow = %d, want 32768", lim.ContextWindow)
	}
	if lim.ContextSource != SourceCatalog {
		t.Errorf("ContextSource = %q, want %q", lim.ContextSource, SourceCatalog)
	}
}

func TestSourceVariantIsDistinct(t *testing.T) {
	if SourceVariant == SourceFetched || SourceVariant == SourceCatalog {
		t.Fatal("SourceVariant must be distinguishable from fetched and catalog")
	}
	if SourceVariant == "" {
		t.Fatal("SourceVariant must have a human-readable label")
	}
}

// catalogFor is a thin test helper wrapping catalog.Lookup so tests assert
// against the real catalog rather than hardcoding figures that could drift.
func catalogFor(t *testing.T, id string) (int, int) {
	t.Helper()
	return catalog.Lookup(id)
}
