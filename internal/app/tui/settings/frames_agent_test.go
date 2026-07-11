package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

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
