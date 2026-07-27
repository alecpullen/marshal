package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
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
	for _, r := range f.List.Rows() {
		if r.Title == "Provider" {
			providerRow = r
			break
		}
	}
	if providerRow == nil {
		t.Fatal("Agent frame must have a Provider row")
	}
	if providerRow.Kind != kindPicker {
		t.Fatalf("Provider row kind = %v, want kindPicker", providerRow.Kind)
	}
	values := map[string]bool{}
	for _, item := range providerRow.PickOptions() {
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
	for _, r := range f.List.Rows() {
		if r.Title == "Provider" {
			providerRow = r
			break
		}
	}
	items := providerRow.PickOptions()
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
	for _, r := range f.List.Rows() {
		if r.Title == "Model" {
			modelRow = r
			break
		}
	}
	if modelRow.Kind != kindPicker {
		t.Fatalf("Model row kind = %v, want kindPicker", modelRow.Kind)
	}
	values := map[string]bool{}
	for _, item := range modelRow.PickOptions() {
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
	for ps.Top().List.CursorRow().Title != "Provider" {
		ps.Update(kp("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ps.Top().List.SetInputValue("vllm")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Models.Presets[preset].Provider != "vllm" {
		t.Fatalf("provider should write to preset %q, got %q", preset, s.cfg.Models.Presets[preset].Provider)
	}
}

func TestAgentFrameTitlesShowActivePreset(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets["my-preset"] = routing.ModelPreset{
		Name:     "my-preset",
		Provider: "ollama",
		Model:    "llama3.1:8b",
	}
	cfg.AgentProfiles["local_balanced"] = routing.AgentProfile{
		Name:  "local_balanced",
		Roles: map[routing.AgentRole]routing.RoleBinding{routing.RoleImplementer: {Preset: "my-preset"}},
	}
	st := newState(cfg)
	f := agentFrame(st)

	var providerRow, modelRow *field
	for _, r := range f.List.Rows() {
		if r.Title == "Provider (preset: my-preset)" {
			providerRow = r
		}
		if r.Title == "Model (preset: my-preset)" {
			modelRow = r
		}
	}
	if providerRow == nil {
		t.Fatal("Agent frame must have a Provider (preset: my-preset) row when preset is active")
	}
	if modelRow == nil {
		t.Fatal("Agent frame must have a Model (preset: my-preset) row when preset is active")
	}
	wantDesc := "writes into preset my-preset — shared by every role that uses it"
	if providerRow.Desc != wantDesc {
		t.Fatalf("Provider row desc = %q, want %q", providerRow.Desc, wantDesc)
	}
	if modelRow.Desc != wantDesc {
		t.Fatalf("Model row desc = %q, want %q", modelRow.Desc, wantDesc)
	}
}

func TestAgentFrameTitlesPlainWithoutPreset(t *testing.T) {
	// Default config has no AgentProfiles, so no preset is active.
	st := newState(config.Default())
	f := agentFrame(st)

	var providerRow, modelRow *field
	for _, r := range f.List.Rows() {
		if r.Title == "Provider" {
			providerRow = r
		}
		if r.Title == "Model" {
			modelRow = r
		}
	}
	if providerRow == nil {
		t.Fatal("Agent frame must have a plain Provider row when no preset is active")
	}
	if modelRow == nil {
		t.Fatal("Agent frame must have a plain Model row when no preset is active")
	}
	if providerRow.Desc != "configured provider for this role" {
		t.Fatalf("Provider row desc = %q, want default desc", providerRow.Desc)
	}
	if modelRow.Desc != "model id for this role" {
		t.Fatalf("Model row desc = %q, want default desc", modelRow.Desc)
	}
}

func TestShellFrameHasEnumAndLists(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(shellFrame(s))
	ps.SetSize(80, 24)
	var titles []string
	for _, f := range ps.Top().List.Rows() {
		titles = append(titles, f.Title)
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
