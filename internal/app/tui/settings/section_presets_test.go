package settings

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func presetsTestConfig() config.Config {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Name: "fast", Provider: "ollama", Model: "qwen3", Temperature: 0.2, ToolCalling: "native", LocalOnly: true},
	}
	return cfg
}

func TestPresetsPaneListsEntries(t *testing.T) {
	m := New(presetsTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "presets")
	if got := m.FocusedFieldTitle(); got != "Model Presets" {
		t.Fatalf("focused = %q, want Model Presets", got)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "fast") || !strings.Contains(view, "ollama/qwen3") {
		t.Errorf("preset list should show name + provider/model:\n%s", view)
	}
}

func TestPresetsPaneAddEntry(t *testing.T) {
	m := New(presetsTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "presets")
	m = keyPress(m, "a", "b", "a", "l", "a", "n", "c", "e", "enter")
	if _, ok := m.state.cfg.Models.Presets["balance"]; !ok {
		t.Fatal("add should create the balance preset")
	}
}

func TestPresetsPaneEditTemperature(t *testing.T) {
	m := New(presetsTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "presets")
	m = keyPress(m, "enter") // open sub-form for "fast"
	// Navigate to the Temperature field. Field order: Provider, Model,
	// Context window, Max output tokens, Temperature (index 4).
	for i := 0; i < 4; i++ {
		m = keyPress(m, "down")
	}
	if got := m.FocusedFieldTitle(); got != "Temperature" {
		t.Fatalf("focused = %q, want Temperature", got)
	}
}
