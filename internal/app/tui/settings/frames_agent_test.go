package settings

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestAgentFrameHasScalarRows(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	f := agentFrame(st)

	titles := map[string]*field{}
	for _, r := range f.List.Rows() {
		titles[r.Title] = r
	}
	for _, want := range []string{"Default profile", "Preset", "Max tool iterations", "Max retries", "Max turn context tokens", "Max tool result chars", "Subtask iterations", "Plan first", "Approval mode"} {
		if titles[want] == nil {
			t.Fatalf("Agent frame missing row %q; rows: %v", want, titles)
		}
	}
	// The legacy provider/model rows are gone.
	if titles["Provider"] != nil || titles["Model"] != nil {
		t.Fatal("Agent frame must not expose legacy provider/model rows")
	}
}

func TestAgentFramePresetRowShowsActivePreset(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets["ollama/llama3.1:8b"] = routing.ModelPreset{
		Name:     "ollama/llama3.1:8b",
		Provider: "ollama",
		Model:    "llama3.1:8b",
	}
	cfg.AgentProfiles["local_balanced"] = routing.AgentProfile{
		Name:  "local_balanced",
		Roles: map[routing.AgentRole]routing.RoleBinding{routing.RoleImplementer: {Preset: "ollama/llama3.1:8b"}},
	}
	st := newState(cfg)
	f := agentFrame(st)

	var presetRow *field
	for _, r := range f.List.Rows() {
		if r.Title == "Preset" {
			presetRow = r
		}
	}
	if presetRow == nil {
		t.Fatal("Agent frame must have a Preset row")
	}
	if got := presetRow.GetStr(); got != "ollama/llama3.1:8b" {
		t.Fatalf("Preset row = %q, want ollama/llama3.1:8b", got)
	}
}

func TestAgentFramePresetRowShowsNoneWithoutPreset(t *testing.T) {
	// Default config has no AgentProfiles, so no preset is active.
	st := newState(config.Default())
	f := agentFrame(st)

	var presetRow *field
	for _, r := range f.List.Rows() {
		if r.Title == "Preset" {
			presetRow = r
		}
	}
	if presetRow == nil {
		t.Fatal("Agent frame must have a Preset row")
	}
	if got := presetRow.GetStr(); got != "(none)" {
		t.Fatalf("Preset row = %q, want (none)", got)
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
