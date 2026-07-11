package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
)

func newTestModel(t *testing.T) Model {
	t.Helper()
	m := New(config.Default(), t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)
	return m
}

func press(m Model, keys ...tea.KeyPressMsg) Model {
	for _, k := range keys {
		m, _ = m.Update(k)
	}
	return m
}

func TestTwoLevelFocusAndEscRule(t *testing.T) {
	m := newTestModel(t)
	if m.paneFocused {
		t.Fatal("focus should start on the sidebar")
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.paneFocused {
		t.Fatal("enter should focus the pane")
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.paneFocused {
		t.Fatal("esc in pane at root should return to sidebar")
	}
}

func TestEscAtSidebarCleanEmitsCancelled(t *testing.T) {
	m := newTestModel(t)
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc with clean state should produce a command")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatal("expected CancelledMsg")
	}
}

func TestEscWithDirtyStateNeedsDoublePress(t *testing.T) {
	m := newTestModel(t)
	// dirty the config: enter Privacy pane and toggle the first row
	// (sidebar starts on Agent; j*3 = Privacy per sectionList order)
	m = press(m, kp("j"), kp("j"), kp("j"), tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !m.dirty() {
		t.Fatal("toggle should dirty the working copy")
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEscape}) // back to sidebar
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatal("first esc with dirty state must not cancel")
	}
	if !strings.Contains(m.View(), "unsaved") {
		t.Fatalf("footer should warn about unsaved changes:\n%s", m.View())
	}
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("second esc should cancel")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatal("expected CancelledMsg on second esc")
	}
}

func TestViewHasBorderedPanelsAndFocusMarker(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	if !strings.Contains(v, "╭") || !strings.Contains(v, "╰") {
		t.Fatalf("view should render bordered panels:\n%s", v)
	}
	if !strings.Contains(v, " Settings ") {
		t.Fatalf("sidebar panel should be titled Settings:\n%s", v)
	}
	if !strings.Contains(v, " Agent ") {
		t.Fatalf("detail panel should be titled with the section:\n%s", v)
	}
}

func TestFooterIsContextSensitive(t *testing.T) {
	m := newTestModel(t)
	sidebar := m.View()
	if !strings.Contains(sidebar, "open") || !strings.Contains(sidebar, "search") {
		t.Fatalf("sidebar footer should show open/search hints:\n%s", sidebar)
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // into pane; Agent first rows include a toggle further down
	pane := m.View()
	if !strings.Contains(pane, "sidebar") && !strings.Contains(pane, "back") {
		t.Fatalf("pane footer should hint how to get back:\n%s", pane)
	}
}

func TestDirtyDotInSidebarTitle(t *testing.T) {
	m := newTestModel(t)
	m = press(m, kp("j"), kp("j"), kp("j"), tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Settings ●") {
		t.Fatalf("dirty state should mark the sidebar title:\n%s", plain)
	}
}

func TestNarrowModePagesSections(t *testing.T) {
	m := newTestModel(t)
	m.SetSize(60, 24)
	if !m.sidebarHidden {
		t.Fatal("width 60 should hide the sidebar")
	}
	if !strings.Contains(m.View(), "‹") {
		t.Fatalf("narrow mode should show paging chevrons:\n%s", m.View())
	}
	before := m.cursor
	m = press(m, kp("l"))
	if m.cursor != before+1 {
		t.Fatalf("l should page to next section, cursor=%d", m.cursor)
	}
}

func TestCtrlSSavesAndFlashes(t *testing.T) {
	m := newTestModel(t)
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+s should produce a save command")
	}
	msg := cmd()
	if _, ok := msg.(SavedMsg); !ok {
		t.Fatalf("expected SavedMsg, got %T (footer: %q)", msg, m.Footer())
	}
}

func TestBoolValueStillReadsWorkingCopy(t *testing.T) {
	m := newTestModel(t)
	got := m.BoolValue("Remote providers allowed")
	if got != m.state.cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("BoolValue should read the working copy")
	}
}

func TestFocusedFieldTitleFollowsPaneCursor(t *testing.T) {
	m := newTestModel(t)
	if m.FocusedFieldTitle() != "Agent" {
		t.Fatalf("sidebar focus should report section title, got %q", m.FocusedFieldTitle())
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.FocusedFieldTitle() != "Default profile" {
		t.Fatalf("pane focus should report the cursor row, got %q", m.FocusedFieldTitle())
	}
}

func TestSaveBlockedDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	original := "# original config\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	m := New(config.Default(), dir, cfgPath)
	m.SetSize(100, 32)
	const busyMsg = "Stop the active turn and background jobs before applying settings."
	m.SetSaveBlocked(busyMsg)

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("expected nil command when save is blocked")
	}
	if m.Footer() != busyMsg {
		t.Fatalf("footer = %q, want %q", m.Footer(), busyMsg)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("file was modified:\nwant: %q\ngot:  %q", original, string(content))
	}
}
