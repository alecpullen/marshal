package settings

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func agentTestConfig() config.Config {
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]string{routing.RoleImplementer: "fast"}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Name: "fast", Provider: "ollama", Model: "qwen3", LocalOnly: true},
	}
	return cfg
}

func TestAgentPaneShowsPresetFields(t *testing.T) {
	m := New(agentTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = keyPress(m, "tab") // enter agent pane
	if got := m.FocusedFieldTitle(); got != "Default profile" {
		t.Errorf("first focused field = %q, want Default profile", got)
	}
}

func TestAgentPaneEditsWriteToWorkingCopy(t *testing.T) {
	m := New(agentTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = keyPress(m, "tab", "enter", "enter") // enter pane, then past Default profile + Preset selects
	if got := m.FocusedFieldTitle(); got != "Provider" {
		t.Fatalf("focused = %q, want Provider", got)
	}
	m = keyPress(m, "x", "enter") // type then blur via Next (validate fires)
	if got := m.state.cfg.Models.Presets["fast"].Provider; got != "ollamax" {
		t.Errorf("preset provider = %q, want ollamax", got)
	}
	if !m.dirty() {
		t.Error("edit must mark model dirty")
	}
}

func TestAgentPaneLocalOnlyToggle(t *testing.T) {
	m := New(agentTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = keyPress(m, "tab", "enter", "enter", "enter", "enter") // Local only
	if got := m.FocusedFieldTitle(); got != "Local only" {
		t.Fatalf("focused = %q, want Local only", got)
	}
	m = keyPress(m, "space", "enter")
	if m.BoolValue("Local only") {
		t.Error("space should have toggled Local only off")
	}
}
