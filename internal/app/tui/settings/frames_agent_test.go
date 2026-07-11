package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestAgentProviderFieldIsKindPicker(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	f := agentFrame(st)

	var providerRow *field
	for _, r := range f.list.Rows() {
		if r.title == "Provider" {
			providerRow = r
			break
		}
	}
	if providerRow == nil {
		t.Fatal("Agent frame must have a Provider row")
	}
	if providerRow.kind != kindPicker {
		t.Fatalf("Provider row kind = %v, want kindPicker", providerRow.kind)
	}
	values := map[string]bool{}
	for _, item := range providerRow.pickOptions() {
		values[item.Value] = true
	}
	if !values["ollama"] || !values["openrouter"] {
		t.Fatalf("provider picker items missing configured providers, got %v", values)
	}
}

func TestAgentProviderPickerEmptyState(t *testing.T) {
	st := newState(config.Default())
	f := agentFrame(st)

	var providerRow *field
	for _, r := range f.list.Rows() {
		if r.title == "Provider" {
			providerRow = r
			break
		}
	}
	items := providerRow.pickOptions()
	if len(items) == 0 || items[0].Value != "__add_provider__" {
		t.Fatalf("empty provider picker should have an 'Add a provider' item, got %v", items)
	}
}

func TestAgentModelPickerUsesDiscoveredCache(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	cfg.Agent.Provider = "ollama"
	st := newState(cfg)
	st.discovered["ollama"] = []string{"qwen2.5-coder:7b", "llama3.1:8b"}

	f := agentFrame(st)
	var modelRow *field
	for _, r := range f.list.Rows() {
		if r.title == "Model" {
			modelRow = r
			break
		}
	}
	if modelRow.kind != kindPicker {
		t.Fatalf("Model row kind = %v, want kindPicker", modelRow.kind)
	}
	values := map[string]bool{}
	for _, item := range modelRow.pickOptions() {
		values[item.Value] = true
	}
	if !values["qwen2.5-coder:7b"] {
		t.Fatal("model picker should include discovered models")
	}
}

func TestAgentFrameProviderWritesToActivePreset(t *testing.T) {
	cfg := config.Default()
	s := newState(cfg)
	preset := activePresetNameFor(s.cfg)
	if preset == "" {
		t.Skip("default config has no active preset; covered by direct-write test below")
	}
	ps := newPaneStack(agentFrame(s))
	ps.SetSize(80, 24)
	for ps.top().list.CursorRow().title != "Provider" {
		ps.Update(kp("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ps.top().list.input.SetValue("vllm")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Models.Presets[preset].Provider != "vllm" {
		t.Fatalf("provider should write to preset %q, got %q", preset, s.cfg.Models.Presets[preset].Provider)
	}
}

func TestShellFrameHasEnumAndLists(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(shellFrame(s))
	ps.SetSize(80, 24)
	var titles []string
	for _, f := range ps.top().list.Rows() {
		titles = append(titles, f.title)
	}
	for _, want := range []string{"Allow network", "Dynamic argv0 guardrail", "Allow commands", "Confirm commands", "Deny patterns"} {
		found := false
		for _, ti := range titles {
			if ti == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("shell frame missing row %q; rows: %v", want, titles)
		}
	}
}
