package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestRegistryCoversEverySection(t *testing.T) {
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	hits := buildRegistry(m.specs, m.panes)
	seen := map[int]bool{}
	for _, h := range hits {
		seen[h.sectionIdx] = true
	}
	for i, sp := range m.specs {
		if !seen[i] {
			t.Errorf("section %q registered no searchable fields", sp.title)
		}
	}
}

func TestFuzzyFilterRanksSubstringFirst(t *testing.T) {
	hits := []searchHit{
		{fieldTitle: "Max output bytes"},
		{fieldTitle: "Allow network"},
		{fieldTitle: "All own kettle"}, // subsequence match for "allow ne"
	}
	got := fuzzyFilter(hits, "allow ne")
	if len(got) == 0 || got[0].fieldTitle != "Allow network" {
		t.Fatalf("substring match should rank first, got %v", got)
	}
}

func TestSearchJumpLandsOnField(t *testing.T) {
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if m.overlay != overlaySearch {
		t.Fatal("/ should open the search overlay")
	}
	for _, r := range "allow network" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.overlay != overlayNone {
		t.Fatal("enter should close the overlay")
	}
	if m.specs[m.cursor].id != "shell" {
		t.Fatalf("jump should land on shell section, got %q", m.specs[m.cursor].id)
	}
	if !m.paneFocused {
		t.Fatal("jump should focus the pane")
	}
	if got := m.activePane().top().list.CursorRow().title; got != "Allow network" {
		t.Fatalf("jump should land on the field, got %q", got)
	}
}
