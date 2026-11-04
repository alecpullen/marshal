package schema

import (
	"slices"
	"testing"
)

// TestResolveThinkingOptionsPrefersTheOverride pins that an explicit override
// wins and comes back cleaned: blank and default levels and repeats are
// dropped, and the override itself is untouched.
func TestResolveThinkingOptionsPrefersTheOverride(t *testing.T) {
	override := &ThinkingOptions{Levels: []string{"", "default", "low", "low", "high"}}
	models := []ModelInfo{{
		ID:       "gpt-5",
		Thinking: &ThinkingOptions{Levels: []string{"minimal", "medium"}},
	}}

	got := ResolveThinkingOptions("openai", "gpt-5", override, models)
	if got == nil {
		t.Fatal("ResolveThinkingOptions with an override = nil, want a copy")
	}
	if want := []string{"low", "high"}; !slices.Equal(got.Levels, want) {
		t.Errorf("resolved levels = %v, want %v", got.Levels, want)
	}

	got.Levels[0] = "changed"
	want := []string{"", "default", "low", "low", "high"}
	if !slices.Equal(override.Levels, want) {
		t.Errorf("override levels = %v after mutating the copy, want %v", override.Levels, want)
	}
}

// TestResolveThinkingOptionsFallsBack pins the order below an explicit
// override: a discovered model's options, then the Codex fallback, then nil.
func TestResolveThinkingOptionsFallsBack(t *testing.T) {
	models := []ModelInfo{{
		ID:       "gpt-5",
		Thinking: &ThinkingOptions{Levels: []string{"minimal", "medium", "high"}, Mode: "effort"},
	}}

	got := ResolveThinkingOptions("openai", "gpt-5", nil, models)
	if got == nil {
		t.Fatal("ResolveThinkingOptions with a discovered model = nil, want its options")
	}
	if want := []string{"minimal", "medium", "high"}; !slices.Equal(got.Levels, want) {
		t.Errorf("discovered levels = %v, want %v", got.Levels, want)
	}
	if got.Mode != "effort" {
		t.Errorf("discovered mode = %q, want effort", got.Mode)
	}

	codex := ResolveThinkingOptions("openai_codex", "gpt-5", nil, nil)
	if codex == nil {
		t.Fatal("ResolveThinkingOptions(openai_codex) = nil, want the effort levels")
	}
	if want := []string{"low", "medium", "high"}; !slices.Equal(codex.Levels, want) {
		t.Errorf("codex levels = %v, want %v", codex.Levels, want)
	}
	if codex.Mode != "effort" {
		t.Errorf("codex mode = %q, want effort", codex.Mode)
	}

	if other := ResolveThinkingOptions("openai", "gpt-5", nil, nil); other != nil {
		t.Errorf("ResolveThinkingOptions with nothing known = %+v, want nil", other)
	}
}

// TestPreserveThinkingOptionsKeepsCachedControls pins the enrichment-failure
// path: a model with no controls takes the cached entry's, an explicit empty
// control is never replaced, and the input slice is left alone.
func TestPreserveThinkingOptionsKeepsCachedControls(t *testing.T) {
	unknown := ModelInfo{ID: "a"}
	unsupported := ModelInfo{ID: "b", Thinking: &ThinkingOptions{}}
	models := []ModelInfo{unknown, unsupported}
	cached := []ModelInfo{{
		ID:       "a",
		Thinking: &ThinkingOptions{Levels: []string{"low", "high"}, Mode: "effort"},
	}, {
		ID:       "b",
		Thinking: &ThinkingOptions{Levels: []string{"low"}, Mode: "effort"},
	}}

	got := PreserveThinkingOptions(models, cached)
	if got[0].Thinking == nil {
		t.Fatal("model with no controls kept none; want the cached entry's")
	}
	if want := []string{"low", "high"}; !slices.Equal(got[0].Thinking.Levels, want) {
		t.Errorf("preserved levels = %v, want %v", got[0].Thinking.Levels, want)
	}
	if got[1].Thinking == nil || len(got[1].Thinking.Levels) != 0 {
		t.Errorf("explicitly empty controls = %+v, want them kept as empty", got[1].Thinking)
	}
	if models[0].Thinking != nil {
		t.Errorf("input model's controls = %+v, want them left nil", models[0].Thinking)
	}
	if models[1].Thinking == nil || len(models[1].Thinking.Levels) != 0 {
		t.Errorf("input model's empty controls changed: %+v", models[1].Thinking)
	}
}
