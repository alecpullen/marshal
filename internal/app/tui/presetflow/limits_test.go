package presetflow

import (
	"testing"

	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func TestResolvePrefersDiscovered(t *testing.T) {
	discovered := []schema.ModelInfo{{ID: "qwen2.5-coder:7b", ContextWindow: 65536, MaxOutputTokens: 4096}}
	lim := Resolve(discovered, "qwen2.5-coder:7b", false)
	if lim.ContextWindow != 65536 || lim.ContextSource != SourceReported {
		t.Errorf("ContextWindow=%d Source=%q, want 65536/reported", lim.ContextWindow, lim.ContextSource)
	}
	if lim.MaxOutputTokens != 4096 || lim.OutputSource != SourceReported {
		t.Errorf("MaxOutputTokens=%d Source=%q, want 4096/reported", lim.MaxOutputTokens, lim.OutputSource)
	}
}

func TestResolveFallsBackToDefaultsPerField(t *testing.T) {
	// Discovered reports a context window but no max output — each field
	// must resolve independently, and the missing one takes the remote
	// 128K default rather than a table lookup.
	discovered := []schema.ModelInfo{{ID: "qwen2.5-coder:7b", ContextWindow: 65536}}
	lim := Resolve(discovered, "qwen2.5-coder:7b", false)
	if lim.ContextWindow != 65536 || lim.ContextSource != SourceReported {
		t.Errorf("ContextWindow=%d Source=%q, want 65536/reported", lim.ContextWindow, lim.ContextSource)
	}
	if lim.MaxOutputTokens != routing.DefaultRemoteMaxOutputTokens || lim.OutputSource != SourceDefault {
		t.Errorf("MaxOutputTokens=%d Source=%q, want %d/default",
			lim.MaxOutputTokens, lim.OutputSource, routing.DefaultRemoteMaxOutputTokens)
	}
}

func TestResolveUnknownModelUsesRemoteDefaults(t *testing.T) {
	lim := Resolve(nil, "some-unheard-of-model", false)
	if lim.ContextWindow != routing.DefaultRemoteContextWindow || lim.ContextSource != SourceDefault {
		t.Errorf("ContextWindow=%d Source=%q, want %d/default",
			lim.ContextWindow, lim.ContextSource, routing.DefaultRemoteContextWindow)
	}
	if lim.MaxOutputTokens != routing.DefaultRemoteMaxOutputTokens || lim.OutputSource != SourceDefault {
		t.Errorf("MaxOutputTokens=%d Source=%q, want %d/default",
			lim.MaxOutputTokens, lim.OutputSource, routing.DefaultRemoteMaxOutputTokens)
	}
	if lim.ToolCalling != nil || lim.ToolSource != SourceUnknown {
		t.Errorf("ToolCalling=%v Source=%q, want nil/unknown", lim.ToolCalling, lim.ToolSource)
	}
}

// Local endpoints keep the conservative pair: Ollama's /api/show probe then
// supplies the pulled model's real window.
func TestResolveLocalModelUsesLocalDefaults(t *testing.T) {
	lim := Resolve(nil, "some-local-model", true)
	if lim.ContextWindow != routing.DefaultLocalContextWindow || lim.ContextSource != SourceDefault {
		t.Errorf("ContextWindow=%d Source=%q, want %d/default",
			lim.ContextWindow, lim.ContextSource, routing.DefaultLocalContextWindow)
	}
	if lim.MaxOutputTokens != routing.DefaultLocalMaxOutputTokens || lim.OutputSource != SourceDefault {
		t.Errorf("MaxOutputTokens=%d Source=%q, want %d/default",
			lim.MaxOutputTokens, lim.OutputSource, routing.DefaultLocalMaxOutputTokens)
	}
}

func TestResolveCarriesDiscoveredToolCalling(t *testing.T) {
	toolCalling := true
	discovered := []schema.ModelInfo{{ID: "gpt-4o", ToolCalling: &toolCalling}}
	lim := Resolve(discovered, "gpt-4o", false)
	if lim.ToolCalling == nil || !*lim.ToolCalling || lim.ToolSource != SourceReported {
		t.Errorf("ToolCalling=%v Source=%q, want true/reported", lim.ToolCalling, lim.ToolSource)
	}
}

func TestWithPresetFillsOnlyUnknownFields(t *testing.T) {
	lim := Limits{ContextWindow: 0, ContextSource: SourceUnknown, MaxOutputTokens: 4096, OutputSource: SourceReported}
	got := lim.WithPreset(32768, 2048)
	if got.ContextWindow != 32768 || got.ContextSource != SourcePreset {
		t.Errorf("ContextWindow=%d Source=%q, want 32768/preset", got.ContextWindow, got.ContextSource)
	}
	if got.MaxOutputTokens != 4096 || got.OutputSource != SourceReported {
		t.Errorf("MaxOutputTokens=%d Source=%q, want unchanged 4096/reported", got.MaxOutputTokens, got.OutputSource)
	}
}

func TestWithPresetOverridesDefault(t *testing.T) {
	// A saved preset figure beats the default pair so a re-picked model
	// does not show a guessed number.
	lim := Limits{
		ContextWindow:   routing.DefaultRemoteContextWindow,
		ContextSource:   SourceDefault,
		MaxOutputTokens: routing.DefaultRemoteMaxOutputTokens,
		OutputSource:    SourceDefault,
	}
	got := lim.WithPreset(200000, 8192)
	if got.ContextWindow != 200000 || got.ContextSource != SourcePreset {
		t.Errorf("ContextWindow=%d Source=%q, want 200000/preset", got.ContextWindow, got.ContextSource)
	}
	if got.MaxOutputTokens != 8192 || got.OutputSource != SourcePreset {
		t.Errorf("MaxOutputTokens=%d Source=%q, want 8192/preset", got.MaxOutputTokens, got.OutputSource)
	}
}

func TestWithPresetDoesNotOverrideReported(t *testing.T) {
	lim := Limits{ContextWindow: 256000, ContextSource: SourceReported, MaxOutputTokens: 4096, OutputSource: SourceReported}
	got := lim.WithPreset(200000, 8192)
	if got.ContextWindow != 256000 || got.ContextSource != SourceReported {
		t.Errorf("ContextWindow=%d Source=%q, want unchanged 256000/reported", got.ContextWindow, got.ContextSource)
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

func TestWithProbedOverridesDefaultContext(t *testing.T) {
	lim := Limits{ContextWindow: 32768, ContextSource: SourceDefault}
	got := lim.WithProbed(nil, 65536)
	if got.ContextWindow != 65536 || got.ContextSource != SourceProbed {
		t.Errorf("ContextWindow=%d Source=%q, want 65536/probed", got.ContextWindow, got.ContextSource)
	}
}

func TestWithProbedOverridesReportedContext(t *testing.T) {
	lim := Limits{ContextWindow: 32768, ContextSource: SourceReported}
	got := lim.WithProbed(nil, 65536)
	if got.ContextWindow != 65536 || got.ContextSource != SourceProbed {
		t.Errorf("ContextWindow=%d Source=%q, want 65536/probed", got.ContextWindow, got.ContextSource)
	}
}

// A probed context window is the model file's training ceiling, not the
// user's configured budget. A saved-preset figure is a deliberate choice
// and must survive re-probing /connect — otherwise /connect silently
// resets the user's saved limits every time they re-pick a model.
func TestWithProbedPreservesPresetContext(t *testing.T) {
	lim := Limits{ContextWindow: 200000, ContextSource: SourcePreset}
	got := lim.WithProbed(nil, 40960)
	if got.ContextWindow != 200000 || got.ContextSource != SourcePreset {
		t.Errorf("ContextWindow=%d Source=%q, want unchanged 200000/preset", got.ContextWindow, got.ContextSource)
	}
}

// A value the user just typed into the confirm-limits screen (SourceEdited)
// is even more authoritative than a saved preset: the probe resolving
// asynchronously must not clobber an in-flight edit.
func TestWithProbedPreservesEditedContext(t *testing.T) {
	lim := Limits{ContextWindow: 131072, ContextSource: SourceEdited}
	got := lim.WithProbed(nil, 40960)
	if got.ContextWindow != 131072 || got.ContextSource != SourceEdited {
		t.Errorf("ContextWindow=%d Source=%q, want unchanged 131072/edited", got.ContextWindow, got.ContextSource)
	}
}

// ToolCalling has no user-configurable meaning independent of the probe,
// so the probe stays authoritative even when the figure came from a saved
// preset or an edit; only the context window is preserved.
func TestWithProbedStillOverridesToolCallingOnPresetSource(t *testing.T) {
	toolCalling := false
	lim := Limits{ContextWindow: 200000, ContextSource: SourcePreset}
	got := lim.WithProbed(&toolCalling, 40960)
	if got.ToolCalling == nil || *got.ToolCalling || got.ToolSource != SourceProbed {
		t.Errorf("ToolCalling=%v Source=%q, want false/probed", got.ToolCalling, got.ToolSource)
	}
	if got.ContextWindow != 200000 || got.ContextSource != SourcePreset {
		t.Errorf("ContextWindow=%d Source=%q, want unchanged 200000/preset", got.ContextWindow, got.ContextSource)
	}
}
