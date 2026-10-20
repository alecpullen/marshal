package app

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

func saveProbeCache(t *testing.T, dir, provider string, pc config.ProviderConfig, models []schema.ModelInfo) {
	t.Helper()
	c := modelcache.Load(dir)
	c.Providers[provider] = modelcache.Entry{
		ConfigHash: modelcache.HashProvider(pc),
		Models:     models,
		FetchedAt:  time.Now(),
	}
	if err := modelcache.Save(dir, c); err != nil {
		t.Fatalf("save model cache: %v", err)
	}
}

// TestModelProbeUsesProviderReportedLimits keeps the clamp working where it
// is authoritative: a provider whose own model metadata carries the window.
func TestModelProbeUsesProviderReportedLimits(t *testing.T) {
	dir := t.TempDir()
	pc := config.ProviderConfig{Type: "anthropic", BaseURL: "https://api.anthropic.test"}
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"anthropic": pc}}
	saveProbeCache(t, dir, "anthropic", pc, []schema.ModelInfo{{
		ID:               "claude-x",
		ContextWindow:    200000,
		MaxOutputTokens:  64000,
		ProviderReported: true,
	}})

	got := modelProbeFunc(cfg, dir)("anthropic", "claude-x")
	if !got.OK || got.Window != 200000 || got.MaxOutput != 64000 {
		t.Fatalf("probe = %+v, want the provider-reported 200000/64000", got)
	}
}

// TestModelProbeIgnoresTableDerivedLimits is the manual-test regression: an
// openai_compatible /models response carries no limits, so discovered.json's
// ContextWindow came from limits.Table. Treating it as a provider statement
// clamped a user's explicit 1M preset to 64k, which the UI displayed and the
// runner then compacted against.
func TestModelProbeIgnoresTableDerivedLimits(t *testing.T) {
	dir := t.TempDir()
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: "https://ollama-cloud.test/v1"}
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"ollama-cloud": pc}}
	saveProbeCache(t, dir, "ollama-cloud", pc, []schema.ModelInfo{{
		ID:              "glm-5.3",
		ContextWindow:   65536,
		MaxOutputTokens: 8192,
	}})

	got := modelProbeFunc(cfg, dir)("ollama-cloud", "glm-5.3")
	if got.OK {
		t.Fatalf("probe = %+v, want OK=false: a table-derived window must not clamp a preset", got)
	}
}
