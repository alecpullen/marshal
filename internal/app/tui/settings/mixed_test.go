package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestIndexingPaneTabCyclesToList(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "indexing")
	if got := m.FocusedFieldTitle(); got != "Use treesitter" {
		t.Fatalf("focused = %q", got)
	}
	m = keyPress(m, "tab")
	if got := m.FocusedFieldTitle(); got != "Ignore patterns" {
		t.Fatalf("tab should focus the ignore list, got %q", got)
	}
}

func TestIndexingIgnoreListEditMutatesConfig(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "indexing")
	m = keyPress(m, "tab", "a", "z", "enter")
	ignore := m.state.cfg.Indexing.Ignore
	if ignore[len(ignore)-1] != "z" {
		t.Fatalf("ignore = %v, want trailing z", ignore)
	}
}

func TestMixedPaneEscClosesInlineEditNotOverlay(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "indexing")
	m = keyPress(m, "tab", "a", "z")
	m, c := m.Update(keyMsg("esc"))
	if c != nil {
		t.Fatal("first esc must close the inline edit, not emit CancelledMsg")
	}
	if m.activePane().HasInnerFocus() {
		t.Fatal("inline edit should be closed")
	}
}
