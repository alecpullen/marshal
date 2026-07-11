package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestDiagnosticsPaneMapEdit(t *testing.T) {
	// Slow huh-driven test (~6s) because the diagnostics pane rebuilds
	// the command map form during keypress processing.
	if testing.Short() {
		t.Skip("slow huh-driven test")
	}

	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "diagnostics")
	if got := m.FocusedFieldTitle(); got != "Commands" {
		t.Fatalf("focused = %q, want Commands", got)
	}
	m = keyPress(m, "a", "p", "y", "enter", "r", "u", "f", "f", " ", "c", "h", "e", "c", "k", "enter")
	if got := m.state.cfg.Diagnostics.Commands["py"]; got != "ruff check" {
		t.Fatalf("diagnostics py = %q, want ruff check", got)
	}
}
