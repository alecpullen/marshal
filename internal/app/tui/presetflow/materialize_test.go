package presetflow

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func mustMaterialize(t *testing.T, cfg *config.Config, providerName, modelID, baseURL string, lim Limits) string {
	t.Helper()
	name, err := Materialize(cfg, providerName, modelID, baseURL, lim)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	return name
}

func TestMaterializeCreatesPresetNamedProviderSlashModel(t *testing.T) {
	cfg := config.Default()
	name := mustMaterialize(t, &cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1",
		Limits{ContextWindow: 32768, MaxOutputTokens: 8192})
	if name != "ollama/qwen3-coder" {
		t.Fatalf("name = %q, want %q", name, "ollama/qwen3-coder")
	}
	preset, ok := cfg.Models.Presets[name]
	if !ok {
		t.Fatalf("preset %q not written", name)
	}
	if preset.Provider != "ollama" || preset.Model != "qwen3-coder" {
		t.Errorf("preset = %+v, want provider=ollama model=qwen3-coder", preset)
	}
	if preset.ContextWindow != 32768 || preset.MaxOutputTokens != 8192 {
		t.Errorf("preset limits = %+v, want 32768/8192", preset)
	}
	if !preset.LocalOnly {
		t.Error("LocalOnly = false, want true for a localhost base URL")
	}
}

func TestMaterializeMapsToolCalling(t *testing.T) {
	trueVal, falseVal := true, false
	cfg := config.Default()

	name := mustMaterialize(t, &cfg, "openrouter", "gpt-4o", "https://openrouter.ai/api/v1",
		Limits{ToolCalling: &trueVal})
	if got := cfg.Models.Presets[name].ToolCalling; got != "native" {
		t.Errorf("ToolCalling = %q, want native", got)
	}

	name2 := mustMaterialize(t, &cfg, "openrouter", "text-only-model", "https://openrouter.ai/api/v1",
		Limits{ToolCalling: &falseVal})
	if got := cfg.Models.Presets[name2].ToolCalling; got != "none" {
		t.Errorf("ToolCalling = %q, want none", got)
	}
}

func TestMaterializeNeverErasesSavedLimitsWithUnknownZeros(t *testing.T) {
	cfg := config.Default()
	mustMaterialize(t, &cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1",
		Limits{ContextWindow: 32768, MaxOutputTokens: 8192})

	// Re-materializing with unknown (zero) limits must not erase the saved
	// figures — e.g. re-picking the same model after discovery lost the
	// bulk limits-table match.
	name := mustMaterialize(t, &cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1", Limits{})
	preset := cfg.Models.Presets[name]
	if preset.ContextWindow != 32768 || preset.MaxOutputTokens != 8192 {
		t.Errorf("preset limits = %+v, want unchanged 32768/8192", preset)
	}
}

func TestMaterializeIsIdempotentOnName(t *testing.T) {
	cfg := config.Default()
	mustMaterialize(t, &cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1", Limits{ContextWindow: 1000})
	mustMaterialize(t, &cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1", Limits{ContextWindow: 2000})
	if len(cfg.Models.Presets) != 1 {
		t.Fatalf("got %d presets, want 1 (same provider+model must reuse the same preset)", len(cfg.Models.Presets))
	}
}

func TestMaterializePreservesMatchingLegacyMetadata(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"local-qwen": {
			Name: "local-qwen", Provider: "ollama", Model: "qwen3",
			ToolCalling: "native", Pricing: "legacy-pricing",
		},
	}

	name := mustMaterialize(t, &cfg, "ollama", "qwen3", "http://localhost:11434", Limits{})
	preset := cfg.Models.Presets[name]
	if preset.ToolCalling != "native" || preset.Pricing != "legacy-pricing" {
		t.Errorf("canonical preset lost legacy metadata: %+v", preset)
	}
}

func TestMaterializeRejectsCanonicalCollision(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen3": {Name: "ollama/qwen3", Provider: "other", Model: "other"},
	}

	if _, err := Materialize(&cfg, "ollama", "qwen3", "http://localhost:11434", Limits{}); err == nil {
		t.Fatal("Materialize should reject a canonical name occupied by another model")
	}
	if got := cfg.Models.Presets["ollama/qwen3"].Provider; got != "other" {
		t.Fatalf("collision should preserve the existing preset, got provider %q", got)
	}
}
