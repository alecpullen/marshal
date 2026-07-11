package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"marshal/internal/app/config"
)

func TestCompositePaneFocusCycles(t *testing.T) {
	st := newState(config.Default())
	form := newScalarPane(func() *huh.Form {
		return newSectionForm(huh.NewConfirm().Key("X").Title("X").Value(new(bool)))
	})
	col := newCollectionPane(st, collectionSpec{
		heading:   "C",
		keyPrompt: "k",
		entries:   func(s *state) []collectionEntry { return nil },
		add:       func(s *state, key string) error { return nil },
	})
	me := newMapStringEditor("M", &st.cfg.Diagnostics.Commands)
	cp := newCompositePane(form, col, me)
	cp.SetWidth(60)
	cp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cp.focusIdx != 1 {
		t.Fatalf("focusIdx = %d, want 1", cp.focusIdx)
	}
	cp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cp.focusIdx != 2 {
		t.Fatalf("focusIdx = %d, want 2", cp.focusIdx)
	}
	cp.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cp.focusIdx != 0 {
		t.Fatalf("focusIdx = %d, want 0 (wrap)", cp.focusIdx)
	}
}
