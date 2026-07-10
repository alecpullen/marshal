package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func keyPress(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.Msg
		switch k {
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		updated, cmd := m.Update(msg)
		m = updated
		// Drain the command chain so huh's NextField / focus-shift cmds
		// are observed. Mirrors what a real bubbletea runtime does on the
		// program's tick. Bound the drain to avoid hanging on async cmds.
		for i := 0; i < 4 && cmd != nil; i++ {
			var produced tea.Msg
			produced, cmd = drainCmd(cmd)
			if produced == nil {
				break
			}
			updated, cmd = m.Update(produced)
			m = updated
		}
	}
	return m
}

func drainCmd(cmd tea.Cmd) (tea.Msg, tea.Cmd) {
	msg := cmd()
	if msg == nil {
		return nil, nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var combined tea.Cmd
		for _, sub := range batch {
			combined = tea.Batch(combined, sub)
		}
		return nil, combined
	}
	return msg, nil
}

func TestSidebarListsAllSections(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	view := stripANSI(m.View())
	for _, title := range []string{"Agent", "Providers", "Model Presets", "Privacy", "Shell", "Sandbox", "Indexing", "Web", "Swarm", "MCP", "Snapshots", "Hooks", "Permissions", "Diagnostics", "Commands"} {
		if !strings.Contains(view, title) {
			t.Errorf("sidebar missing section %q", title)
		}
	}
}

func TestSidebarCursorMovesAndClamps(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m = keyPress(m, "up")
	if m.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m.cursor)
	}
	m = keyPress(m, "j", "j")
	if m.cursor != 2 {
		t.Errorf("cursor after jj = %d, want 2", m.cursor)
	}
	m = keyPress(m, "G")
	if m.cursor != len(m.sections)-1 {
		t.Errorf("G should jump to last section, got %d", m.cursor)
	}
	m = keyPress(m, "g")
	if m.cursor != 0 {
		t.Errorf("g should jump to first section, got %d", m.cursor)
	}
}

func TestTabEntersAndLeavesPane(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m = keyPress(m, "tab")
	if !m.paneFocused {
		t.Fatal("tab should focus the pane")
	}
	m = keyPress(m, "shift+tab")
	if m.paneFocused {
		t.Fatal("shift+tab should return to sidebar")
	}
}

func TestEscAtTopLevelCancels(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatal("esc at top level should emit CancelledMsg")
	}
}

func TestDirtyReflectsWorkingCopyChanges(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	if m.dirty() {
		t.Fatal("fresh model must not be dirty")
	}
	m.state.cfg.Privacy.RemoteProvidersAllowed = true
	if !m.dirty() {
		t.Fatal("mutating the working copy must set dirty")
	}
}

func TestCloneConfigIsDeep(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{"a": {BaseURL: "x"}}
	clone := cloneConfig(cfg)
	clone.Providers["a"] = config.ProviderConfig{BaseURL: "changed"}
	clone.Tools.Shell.Allow.Commands[0] = "changed"
	clone.Indexing.Ignore[0] = "changed"
	if cfg.Providers["a"].BaseURL != "x" || cfg.Tools.Shell.Allow.Commands[0] == "changed" || cfg.Indexing.Ignore[0] == "changed" {
		t.Fatal("cloneConfig must deep-copy maps and slices")
	}
}
