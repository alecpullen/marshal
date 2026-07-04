package settings

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTabNavigatesBetweenFields(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)

	view := m.View()
	if !strings.Contains(view, "> Default profile:") {
		t.Fatalf("first field should be focused on open:\n%s", view)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "> Preset:") {
		t.Fatalf("Tab should move focus to second field:\n%s", view)
	}
}

func TestTypingUpdatesStringField(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)

	// Tab to Provider field (third field: Default profile, Preset, Provider)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "> Provider:") {
		t.Fatalf("expected Provider field to be focused:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "Provider: ollamax") {
		t.Fatalf("typing 'x' should append to Provider value, got:\n%s", view)
	}
}

func TestArrowChangesSelectField(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)

	// Default profile field is focused initially
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "local_balanced") {
		t.Fatalf("select field should still show option, got:\n%s", view)
	}
}

func TestUpDownNavigatesBetweenFields(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "> Preset:") {
		t.Fatalf("Down should move focus to second field:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "> Default profile:") {
		t.Fatalf("Up should move focus back to first field:\n%s", view)
	}
}

func TestDownOnLastFieldWrapsToFirst(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)

	for i := 0; i < 5; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
	}

	view := m.View()
	if !strings.Contains(view, "> [ ] Remote providers allowed") {
		t.Fatalf("expected last field focused, got:\n%s", view)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "> Default profile:") {
		t.Fatalf("Down on last field should wrap to first:\n%s", view)
	}
}
