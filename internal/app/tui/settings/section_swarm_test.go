package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestSwarmPaneScalarAndMap(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "swarm")
	if got := m.FocusedFieldTitle(); got != "Max fix rounds" {
		t.Fatalf("focused = %q, want Max fix rounds", got)
	}
	m = keyPress(m, "tab") // -> tool iters map
	if got := m.FocusedFieldTitle(); got != "Tool iters" {
		t.Fatalf("tab should reach the tool iters map, got %q", got)
	}
}

func TestSwarmPaneToolItersEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "swarm")
	m = keyPress(m, "tab", "a", "t", "e", "s", "t", "e", "r", "enter", "7", "enter")
	if got := m.state.cfg.Swarm.Budget.ToolIters["tester"]; got != 7 {
		t.Fatalf("tool iters tester = %d, want 7", got)
	}
}
