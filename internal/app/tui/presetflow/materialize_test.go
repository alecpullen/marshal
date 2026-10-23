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

func TestMaterializeNeverShrinksSavedLimitsWithSmallerDiscovery(t *testing.T) {
	cfg := config.Default()
	mustMaterialize(t, &cfg, "ollama-cloud", "glm-5.3", "https://ollama.example/v1",
		Limits{ContextWindow: 1000000, MaxOutputTokens: 128000})

	// Re-picking the model when discovery reports a smaller default (a
	// stale cache, a provider reporting its base limits) must not clobber
	// the hand-set figures.
	name := mustMaterialize(t, &cfg, "ollama-cloud", "glm-5.3", "https://ollama.example/v1",
		Limits{ContextWindow: 200000, MaxOutputTokens: 64000})
	preset := cfg.Models.Presets[name]
	if preset.ContextWindow != 1000000 || preset.MaxOutputTokens != 128000 {
		t.Errorf("preset limits = %+v, want unchanged 1000000/128000", preset)
	}

	// A larger discovered value is an upgrade and is adopted.
	name = mustMaterialize(t, &cfg, "ollama-cloud", "glm-5.3", "https://ollama.example/v1",
		Limits{ContextWindow: 2000000, MaxOutputTokens: 256000})
	preset = cfg.Models.Presets[name]
	if preset.ContextWindow != 2000000 || preset.MaxOutputTokens != 256000 {
		t.Errorf("preset limits = %+v, want upgraded 2000000/256000", preset)
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
		"ollama/qwen3": {
			Name: "ollama/qwen3", Provider: "ollama", Model: "qwen3",
			ToolCalling: "native", Pricing: "legacy-pricing",
		},
	}

	name := mustMaterialize(t, &cfg, "ollama", "qwen3", "http://localhost:11434", Limits{})
	preset := cfg.Models.Presets[name]
	if preset.ToolCalling != "native" || preset.Pricing != "legacy-pricing" {
		t.Errorf("canonical preset lost legacy metadata: %+v", preset)
	}
}

func TestMaterializeRecordsIdentityAndLocality(t *testing.T) {
	cfg := config.Default()
	mustMaterialize(t, &cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1", Limits{})
	preset := cfg.Models.Presets["ollama/qwen3-coder"]
	if preset.Provider != "ollama" || preset.Model != "qwen3-coder" {
		t.Fatalf("preset identity wrong: %+v", preset)
	}
	if !preset.LocalOnly {
		t.Fatal("localhost preset must be LocalOnly")
	}
}

// TestMaterializeSeedsDocumentedDefaults pins the seeding contract: a
// first-time preset with no reported limits saves the documented pair rather
// than zeros, so the user can edit a concrete figure. Remote and local
// endpoints get different pairs, and the literals are asserted rather than
// the constants they came from so a changed default fails here.
func TestMaterializeSeedsDocumentedDefaults(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		wantCW int
		wantMO int
	}{
		{"remote", "https://api.example.com/v1", 1_000_000, 128_000},
		{"local", "http://localhost:11434/v1", 8_192, 4_096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			name := mustMaterialize(t, &cfg, "p", "m", tc.base, Limits{})
			preset := cfg.Models.Presets[name]
			if preset.ContextWindow != tc.wantCW || preset.MaxOutputTokens != tc.wantMO {
				t.Fatalf("seeded limits = %d/%d, want %d/%d",
					preset.ContextWindow, preset.MaxOutputTokens, tc.wantCW, tc.wantMO)
			}
		})
	}
}

// TestMaterializeSeedsOnlyTheMissingField keeps an explicitly reported figure
// while filling the other from the default.
func TestMaterializeSeedsOnlyTheMissingField(t *testing.T) {
	cfg := config.Default()
	name := mustMaterialize(t, &cfg, "p", "m", "https://api.example.com/v1",
		Limits{ContextWindow: 200_000})
	preset := cfg.Models.Presets[name]
	if preset.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want the reported 200000", preset.ContextWindow)
	}
	if preset.MaxOutputTokens != 128_000 {
		t.Errorf("MaxOutputTokens = %d, want the seeded 128000", preset.MaxOutputTokens)
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
