package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/livebox"
)

// ctrlTKey presses Ctrl+T and returns the model with its flash command.
func ctrlTKey(m Model) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	return updated.(Model), cmd
}

// TestCtrlTCyclesThePlanLayout: Ctrl+T steps the live box's plan layout —
// stacked → split → folded → stacked — and skips split on a frame too narrow
// to carry two columns.
func TestCtrlTCyclesThePlanLayout(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.refreshViewport()

	if m.liveBoxMode != livebox.Stacked {
		t.Fatalf("initial layout = %q, want stacked", m.liveBoxMode)
	}
	for _, want := range []livebox.Mode{livebox.Split, livebox.Folded, livebox.Stacked} {
		var cmd tea.Cmd
		m, cmd = ctrlTKey(m)
		if m.liveBoxMode != want {
			t.Fatalf("layout = %q, want %q", m.liveBoxMode, want)
		}
		if cmd == nil {
			t.Fatal("Ctrl+T must flash the new layout")
		}
		if m.flash != "Plan: "+string(want) {
			t.Fatalf("flash = %q, want %q", m.flash, "Plan: "+string(want))
		}
	}

	// A narrow frame has no room for the split's second column, so the cycle
	// goes straight from stacked to folded.
	narrow := newTestModel(t)
	narrow.resize(80, 24)
	narrow.refreshViewport()

	narrow, _ = ctrlTKey(narrow)
	if narrow.liveBoxMode != livebox.Folded {
		t.Fatalf("layout at 80 columns = %q, want folded", narrow.liveBoxMode)
	}
}
