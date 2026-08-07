package modeloptions

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func testConfig() config.Config {
	return config.Config{
		Models: config.ModelsConfig{
			Presets: map[string]routing.ModelPreset{
				"active": {
					Name:            "active",
					Provider:        "ollama",
					Model:           "qwen2.5-coder:7b",
					ContextWindow:   32000,
					MaxOutputTokens: 2048,
				},
				"other": {
					Name:            "other",
					Provider:        "ollama",
					Model:           "llama3",
					ContextWindow:   8192,
					MaxOutputTokens: 1024,
				},
			},
		},
	}
}

func TestPanelShowsActivePresetFields(t *testing.T) {
	cfg := testConfig()
	p := New(cfg, "active")
	view := p.View(80, 12)
	if !strings.Contains(view, "active") {
		t.Fatalf("view missing preset name:\n%s", view)
	}
	if !strings.Contains(view, "Context window") {
		t.Fatalf("view missing context window row:\n%s", view)
	}
	if !strings.Contains(view, "Max output tokens") {
		t.Fatalf("view missing max output row:\n%s", view)
	}
}

func TestPanelEmitsCopiedCandidateOnCommit(t *testing.T) {
	cfg := testConfig()
	original := cfg.Models.Presets["active"].ContextWindow
	p := New(cfg, "active")

	// Navigate to context row (cursor starts at row 0).
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !p.fields.Editing() {
		t.Fatal("expected editing mode")
	}
	// Clear pre-populated value and type 64000 while editing.
	p.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	for _, r := range "64000" {
		p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command from committing the edit")
	}
	msg := cmd().(ChangedMsg)
	if msg.PresetName != "active" {
		t.Fatalf("PresetName = %q, want active", msg.PresetName)
	}
	if msg.Config.Models.Presets["active"].ContextWindow != 64000 {
		t.Fatalf("committed context window = %d, want 64000", msg.Config.Models.Presets["active"].ContextWindow)
	}
	if cfg.Models.Presets["active"].ContextWindow != original {
		t.Fatalf("original config was mutated: got %d, want %d", cfg.Models.Presets["active"].ContextWindow, original)
	}
	if msg.Config.Models.Presets["other"].ContextWindow != 8192 {
		t.Fatalf("other preset was mutated: got %d, want 8192", msg.Config.Models.Presets["other"].ContextWindow)
	}
}

func TestPanelZeroMeansAutomatic(t *testing.T) {
	cfg := testConfig()
	p := New(cfg, "active")

	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	for _, r := range "0" {
		p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.cfg.Models.Presets["active"].ContextWindow != 0 {
		t.Fatalf("stored value = %d, want 0", p.cfg.Models.Presets["active"].ContextWindow)
	}
	if !strings.Contains(p.View(80, 12), "automatic") {
		t.Fatalf("rendered value should show automatic:\n%s", p.View(80, 12))
	}
}

func TestPanelRejectsNegativeValues(t *testing.T) {
	cfg := testConfig()
	p := New(cfg, "active")

	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	for _, r := range "-1" {
		p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.fields.ErrMsg == "" {
		t.Fatal("expected field-list error for negative value")
	}
	if p.cfg.Models.Presets["active"].ContextWindow != 32000 {
		t.Fatalf("stored value changed on rejected input: got %d, want 32000", p.cfg.Models.Presets["active"].ContextWindow)
	}
	if cmd != nil {
		if _, ok := cmd().(ChangedMsg); ok {
			t.Fatal("negative value should not emit ChangedMsg")
		}
	}
	if p.fields.Editing() {
		// The field list cancels the edit after an error, which is fine.
	}
}

func TestPanelEscClosesAtRoot(t *testing.T) {
	cfg := testConfig()
	p := New(cfg, "active")
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a command from Esc at root")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("expected ClosedMsg, got %T", cmd())
	}
}
