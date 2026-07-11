package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestHooksPaneAddAndEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "hooks")
	m = keyPress(m, "a", "enter") // hooks add does not prompt for a name — it appends a default entry
	if len(m.state.cfg.Hooks.Entries) != 1 {
		t.Fatalf("add should append a hook entry, got %d", len(m.state.cfg.Hooks.Entries))
	}
	m = keyPress(m, "enter") // edit the new entry
	if got := m.FocusedFieldTitle(); got != "Event" {
		t.Fatalf("focused = %q, want Event", got)
	}
}

func TestHooksPaneDelete(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.Entries = []config.HookConfig{{Event: "pre_tool", Command: "echo hi"}}
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "hooks")
	m = keyPress(m, "d")
	if len(m.state.cfg.Hooks.Entries) != 0 {
		t.Fatal("d should delete the hook entry")
	}
}
