package presetflow

import (
	"testing"

	"marshal/internal/llm/schema"
)

func TestResolvePrefersDiscoveredOverCatalog(t *testing.T) {
	discovered := []schema.ModelInfo{{ID: "qwen2.5-coder:7b", ContextWindow: 65536, MaxOutputTokens: 4096}}
	lim := Resolve(discovered, "qwen2.5-coder:7b")
	if lim.ContextWindow != 65536 || lim.ContextSource != SourceFetched {
		t.Errorf("ContextWindow=%d Source=%q, want 65536/fetched", lim.ContextWindow, lim.ContextSource)
	}
	if lim.MaxOutputTokens != 4096 || lim.OutputSource != SourceFetched {
		t.Errorf("MaxOutputTokens=%d Source=%q, want 4096/fetched", lim.MaxOutputTokens, lim.OutputSource)
	}
}

func TestResolveFallsBackToCatalogPerField(t *testing.T) {
	// qwen2.5-coder:7b is in the built-in catalog (32768/8192). Discovered
	// reports a context window but not a max output — each field must
	// resolve independently.
	discovered := []schema.ModelInfo{{ID: "qwen2.5-coder:7b", ContextWindow: 65536}}
	lim := Resolve(discovered, "qwen2.5-coder:7b")
	if lim.ContextWindow != 65536 || lim.ContextSource != SourceFetched {
		t.Errorf("ContextWindow=%d Source=%q, want 65536/fetched", lim.ContextWindow, lim.ContextSource)
	}
	if lim.MaxOutputTokens != 8192 || lim.OutputSource != SourceCatalog {
		t.Errorf("MaxOutputTokens=%d Source=%q, want 8192/catalog", lim.MaxOutputTokens, lim.OutputSource)
	}
}

func TestResolveUnknownModelFallsBackToCatalogDefaults(t *testing.T) {
	lim := Resolve(nil, "some-unheard-of-model")
	if lim.ContextWindow != 8192 || lim.ContextSource != SourceCatalog {
		t.Errorf("ContextWindow=%d Source=%q, want 8192/catalog", lim.ContextWindow, lim.ContextSource)
	}
	if lim.MaxOutputTokens != 4096 || lim.OutputSource != SourceCatalog {
		t.Errorf("MaxOutputTokens=%d Source=%q, want 4096/catalog", lim.MaxOutputTokens, lim.OutputSource)
	}
	if lim.ToolCalling != nil || lim.ToolSource != SourceUnknown {
		t.Errorf("ToolCalling=%v Source=%q, want nil/unknown", lim.ToolCalling, lim.ToolSource)
	}
}

func TestResolveCarriesDiscoveredToolCalling(t *testing.T) {
	toolCalling := true
	discovered := []schema.ModelInfo{{ID: "gpt-4o", ToolCalling: &toolCalling}}
	lim := Resolve(discovered, "gpt-4o")
	if lim.ToolCalling == nil || !*lim.ToolCalling || lim.ToolSource != SourceFetched {
		t.Errorf("ToolCalling=%v Source=%q, want true/fetched", lim.ToolCalling, lim.ToolSource)
	}
}

func TestWithPresetFillsOnlyUnknownFields(t *testing.T) {
	lim := Limits{ContextWindow: 0, ContextSource: SourceUnknown, MaxOutputTokens: 4096, OutputSource: SourceFetched}
	got := lim.WithPreset(32768, 2048)
	if got.ContextWindow != 32768 || got.ContextSource != SourcePreset {
		t.Errorf("ContextWindow=%d Source=%q, want 32768/preset", got.ContextWindow, got.ContextSource)
	}
	if got.MaxOutputTokens != 4096 || got.OutputSource != SourceFetched {
		t.Errorf("MaxOutputTokens=%d Source=%q, want unchanged 4096/fetched", got.MaxOutputTokens, got.OutputSource)
	}
}

func TestWithPresetOverridesCatalogDefault(t *testing.T) {
	// A saved preset figure beats the catalog's conservative guess so a
	// re-picked model does not show a guessed default.
	lim := Limits{ContextWindow: 8192, ContextSource: SourceCatalog, MaxOutputTokens: 4096, OutputSource: SourceCatalog}
	got := lim.WithPreset(200000, 8192)
	if got.ContextWindow != 200000 || got.ContextSource != SourcePreset {
		t.Errorf("ContextWindow=%d Source=%q, want 200000/preset", got.ContextWindow, got.ContextSource)
	}
	if got.MaxOutputTokens != 8192 || got.OutputSource != SourcePreset {
		t.Errorf("MaxOutputTokens=%d Source=%q, want 8192/preset", got.MaxOutputTokens, got.OutputSource)
	}
}

func TestWithPresetDoesNotOverrideFetched(t *testing.T) {
	lim := Limits{ContextWindow: 256000, ContextSource: SourceFetched, MaxOutputTokens: 4096, OutputSource: SourceFetched}
	got := lim.WithPreset(200000, 8192)
	if got.ContextWindow != 256000 || got.ContextSource != SourceFetched {
		t.Errorf("ContextWindow=%d Source=%q, want unchanged 256000/fetched", got.ContextWindow, got.ContextSource)
	}
}

func TestWithProbedOverridesToolCallingAndFillsContext(t *testing.T) {
	toolCalling := true
	lim := Limits{ContextWindow: 0, ContextSource: SourceUnknown}
	got := lim.WithProbed(&toolCalling, 40960)
	if got.ToolCalling == nil || !*got.ToolCalling || got.ToolSource != SourceProbed {
		t.Errorf("ToolCalling=%v Source=%q, want true/probed", got.ToolCalling, got.ToolSource)
	}
	if got.ContextWindow != 40960 || got.ContextSource != SourceProbed {
		t.Errorf("ContextWindow=%d Source=%q, want 40960/probed", got.ContextWindow, got.ContextSource)
	}
}

func TestWithProbedOverridesKnownContext(t *testing.T) {
	lim := Limits{ContextWindow: 32768, ContextSource: SourceCatalog}
	got := lim.WithProbed(nil, 65536)
	if got.ContextWindow != 65536 || got.ContextSource != SourceProbed {
		t.Errorf("ContextWindow=%d Source=%q, want 65536/probed", got.ContextWindow, got.ContextSource)
	}
}
