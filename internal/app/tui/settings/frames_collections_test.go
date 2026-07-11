package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestProvidersAddAndEditType(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(providersFrame(s))
	ps.SetSize(80, 24)
	// providers root IS the collection frame; add an entry
	ps.Update(kp("a"))
	for _, r := range "local" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	pc, ok := s.cfg.Providers["local"]
	if !ok {
		t.Fatalf("add should create provider, got %v", s.cfg.Providers)
	}
	if pc.Type != "openai_compatible" {
		t.Fatalf("new provider should default to openai_compatible, got %q", pc.Type)
	}
	// drill into it and edit Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if ps.depth() != 2 {
		t.Fatalf("enter should drill into the provider, depth=%d", ps.depth())
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // edit first row (Type)
	ps.top().list.input.SetValue("anthropic")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Providers["local"].Type != "anthropic" {
		t.Fatalf("type edit should apply immediately, got %q", s.cfg.Providers["local"].Type)
	}
}

func TestHooksAddWithoutPromptAndDelete(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(hooksFrame(s))
	ps.SetSize(80, 24)
	ps.Update(kp("a")) // no key prompt: adds immediately
	if len(s.cfg.Hooks.Entries) != 1 || s.cfg.Hooks.Entries[0].Event != "pre_tool" {
		t.Fatalf("a should append a pre_tool hook, got %v", s.cfg.Hooks.Entries)
	}
	ps.Update(kp("d"))
	if len(s.cfg.Hooks.Entries) != 0 {
		t.Fatalf("d should delete the hook, got %v", s.cfg.Hooks.Entries)
	}
}
