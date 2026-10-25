package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrlTKey presses Ctrl+T and returns the model with its flash command.
func ctrlTKey(m Model) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	return updated.(Model), cmd
}

// TestCtrlTIsUnbound: the plan-layout cycle went with the live box, so Ctrl+T
// reaches the input and does nothing else.
func TestCtrlTIsUnbound(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.refreshViewport()

	m, cmd := ctrlTKey(m)
	if m.flash != "" {
		t.Fatalf("Ctrl+T must not flash: %q", m.flash)
	}
	if cmd != nil {
		t.Fatal("Ctrl+T must not return a command")
	}
	if m.dock.IsOpen() {
		t.Fatalf("Ctrl+T must not open a dock panel, got %T", m.dock.Panel())
	}
}
