package presetflow

import (
	"testing"

	"marshal/internal/app/config"
)

func TestMaterializeCreatesPresetNamedProviderSlashModel(t *testing.T) {
	cfg := config.Default()
	name := Materialize(&cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1",
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

	name := Materialize(&cfg, "openrouter", "gpt-4o", "https://openrouter.ai/api/v1",
		Limits{ToolCalling: &trueVal})
	if got := cfg.Models.Presets[name].ToolCalling; got != "native" {
		t.Errorf("ToolCalling = %q, want native", got)
	}

	name2 := Materialize(&cfg, "openrouter", "text-only-model", "https://openrouter.ai/api/v1",
		Limits{ToolCalling: &falseVal})
	if got := cfg.Models.Presets[name2].ToolCalling; got != "none" {
		t.Errorf("ToolCalling = %q, want none", got)
	}
}

func TestMaterializeNeverErasesSavedLimitsWithUnknownZeros(t *testing.T) {
	cfg := config.Default()
	Materialize(&cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1",
		Limits{ContextWindow: 32768, MaxOutputTokens: 8192})

	// Re-materializing with unknown (zero) limits must not erase the saved
	// figures — e.g. re-picking the same model after discovery lost the
	// bulk limits-table match.
	name := Materialize(&cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1", Limits{})
	preset := cfg.Models.Presets[name]
	if preset.ContextWindow != 32768 || preset.MaxOutputTokens != 8192 {
		t.Errorf("preset limits = %+v, want unchanged 32768/8192", preset)
	}
}

func TestMaterializeIsIdempotentOnName(t *testing.T) {
	cfg := config.Default()
	Materialize(&cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1", Limits{ContextWindow: 1000})
	Materialize(&cfg, "ollama", "qwen3-coder", "http://localhost:11434/v1", Limits{ContextWindow: 2000})
	if len(cfg.Models.Presets) != 1 {
		t.Fatalf("got %d presets, want 1 (same provider+model must reuse the same preset)", len(cfg.Models.Presets))
	}
}
