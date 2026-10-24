package presetsetup

import (
	"math"
	"testing"

	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/schema"
)

// catalogWith builds a one-provider, one-model catalog for tests.
func catalogWith(providerID, modelID string, model modelcatalog.Model) *modelcatalog.Catalog {
	return &modelcatalog.Catalog{
		Providers: map[string]modelcatalog.Provider{
			providerID: {
				ID:     providerID,
				Models: map[string]modelcatalog.Model{modelID: model},
			},
		},
	}
}

func TestResolvePrefersProviderReported(t *testing.T) {
	cat := catalogWith("google", "gemini-2.5-pro", modelcatalog.Model{
		ID:    "gemini-2.5-pro",
		Limit: modelcatalog.Limit{Context: 1048576, Output: 65536},
	})
	m := Resolve("gemini", "", "gemini-2.5-pro", schema.ModelInfo{ContextWindow: 200000}, cat)
	if m.ContextWindow != 200000 {
		t.Fatalf("ContextWindow = %d, want 200000", m.ContextWindow)
	}
	if m.ContextSource != SourceProvider {
		t.Fatalf("ContextSource = %q, want %q", m.ContextSource, SourceProvider)
	}
}

func TestResolveFallsBackToCatalog(t *testing.T) {
	cat := catalogWith("google", "gemini-2.5-pro", modelcatalog.Model{
		ID:    "gemini-2.5-pro",
		Limit: modelcatalog.Limit{Context: 1048576, Output: 65536},
	})
	m := Resolve("gemini", "", "gemini-2.5-pro", schema.ModelInfo{}, cat)
	if m.ContextWindow != 1048576 || m.ContextSource != SourceModelsDev {
		t.Fatalf("context = %d/%q, want 1048576/%q", m.ContextWindow, m.ContextSource, SourceModelsDev)
	}
	if m.MaxOutputTokens != 65536 || m.OutputSource != SourceModelsDev {
		t.Fatalf("output = %d/%q, want 65536/%q", m.MaxOutputTokens, m.OutputSource, SourceModelsDev)
	}
}

func TestResolveFallsBackToDefaults(t *testing.T) {
	cat := catalogWith("google", "other-model", modelcatalog.Model{ID: "other-model"})
	m := Resolve("gemini", "https://generativelanguage.googleapis.com", "gemini-2.5-pro", schema.ModelInfo{}, cat)
	if m.ContextSource != SourceDefault || m.OutputSource != SourceDefault {
		t.Fatalf("sources = %q/%q, want %q/%q", m.ContextSource, m.OutputSource, SourceDefault, SourceDefault)
	}
	if m.ContextWindow != 1_000_000 || m.MaxOutputTokens != 128_000 {
		t.Fatalf("limits = %d/%d, want 1000000/128000", m.ContextWindow, m.MaxOutputTokens)
	}
}

func TestResolveUsesLocalDefaults(t *testing.T) {
	m := Resolve("ollama", "http://localhost:11434/v1", "llama3", schema.ModelInfo{}, nil)
	if m.ContextWindow != 8_192 || m.MaxOutputTokens != 4_096 {
		t.Fatalf("limits = %d/%d, want 8192/4096", m.ContextWindow, m.MaxOutputTokens)
	}
}

func TestResolveUnboundProviderSkipsCatalog(t *testing.T) {
	cat := catalogWith("my-endpoint", "my-model", modelcatalog.Model{
		ID:    "my-model",
		Limit: modelcatalog.Limit{Context: 999, Output: 111},
	})
	m := Resolve("my-endpoint", "https://api.example.com/v1", "my-model", schema.ModelInfo{}, cat)
	if m.ContextSource != SourceDefault {
		t.Fatalf("ContextSource = %q, want %q", m.ContextSource, SourceDefault)
	}
	if m.ContextWindow != 1_000_000 {
		t.Fatalf("ContextWindow = %d, want 1000000", m.ContextWindow)
	}
}

func TestResolveKeepsUnknownToolSupport(t *testing.T) {
	cat := catalogWith("google", "gemini-2.5-pro", modelcatalog.Model{
		ID:    "gemini-2.5-pro",
		Limit: modelcatalog.Limit{Context: 1048576, Output: 65536},
	})
	m := Resolve("gemini", "", "gemini-2.5-pro", schema.ModelInfo{}, cat)
	if m.ToolCalling != nil {
		t.Fatalf("ToolCalling = %v, want nil", *m.ToolCalling)
	}
	if m.ToolSource != SourceUnknown {
		t.Fatalf("ToolSource = %q, want %q", m.ToolSource, SourceUnknown)
	}
}

func TestResolveTakesFalseToolSupport(t *testing.T) {
	no := false
	cat := catalogWith("google", "gemini-2.5-pro", modelcatalog.Model{
		ID:       "gemini-2.5-pro",
		ToolCall: &no,
	})
	m := Resolve("gemini", "", "gemini-2.5-pro", schema.ModelInfo{}, cat)
	if m.ToolCalling == nil {
		t.Fatal("ToolCalling = nil, want non-nil false")
	}
	if *m.ToolCalling {
		t.Fatal("ToolCalling = true, want false")
	}
	if m.ToolSource != SourceModelsDev {
		t.Fatalf("ToolSource = %q, want %q", m.ToolSource, SourceModelsDev)
	}
}

func TestCentsFromUSDPerMTokRounds(t *testing.T) {
	cases := []struct {
		usd  float64
		want int64
	}{
		{1.25, 125},
		{0.0004, 0},
		{2.5, 250},
	}
	for _, tc := range cases {
		got, ok := CentsFromUSDPerMTok(tc.usd)
		if !ok {
			t.Fatalf("CentsFromUSDPerMTok(%v) not ok", tc.usd)
		}
		if got != tc.want {
			t.Fatalf("CentsFromUSDPerMTok(%v) = %d, want %d", tc.usd, got, tc.want)
		}
	}
}

func TestCentsFromUSDPerMTokRejectsInvalid(t *testing.T) {
	for _, usd := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, ok := CentsFromUSDPerMTok(usd); ok {
			t.Fatalf("CentsFromUSDPerMTok(%v) ok, want false", usd)
		}
	}
}

func TestResolveConvertsPricing(t *testing.T) {
	cat := catalogWith("google", "gemini-2.5-pro", modelcatalog.Model{
		ID: "gemini-2.5-pro",
		Cost: modelcatalog.Cost{
			Input:     1.4,
			Output:    4.4,
			CacheRead: 0.26,
		},
	})
	m := Resolve("gemini", "", "gemini-2.5-pro", schema.ModelInfo{}, cat)
	if m.Pricing == nil {
		t.Fatal("Pricing = nil, want non-nil")
	}
	if m.Pricing.InputPerMTokCents != 140 {
		t.Fatalf("InputPerMTokCents = %d, want 140", m.Pricing.InputPerMTokCents)
	}
	if m.Pricing.OutputPerMTokCents != 440 {
		t.Fatalf("OutputPerMTokCents = %d, want 440", m.Pricing.OutputPerMTokCents)
	}
	if m.Pricing.CacheReadPerMTokCents != 26 {
		t.Fatalf("CacheReadPerMTokCents = %d, want 26", m.Pricing.CacheReadPerMTokCents)
	}
}

func TestResolveLeavesPricingUnsetWhenAbsent(t *testing.T) {
	cat := catalogWith("google", "gemini-2.5-pro", modelcatalog.Model{
		ID:    "gemini-2.5-pro",
		Limit: modelcatalog.Limit{Context: 1048576, Output: 65536},
	})
	m := Resolve("gemini", "", "gemini-2.5-pro", schema.ModelInfo{}, cat)
	if m.Pricing != nil {
		t.Fatalf("Pricing = %+v, want nil", m.Pricing)
	}
}
