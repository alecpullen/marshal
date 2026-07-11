package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
	// Make a change so the diff overlay has something to save.
	m.state.cfg.Privacy.RemoteProvidersAllowed = true
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	// ctrl+s opens the diff overlay; press Enter to confirm the save.
	if cmd != nil {
		t.Fatal("ctrl+s should open diff overlay (nil cmd), not save directly")
	}
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("ctrl+s then Enter should produce a save command")
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

func TestDrillIntoNewestProviderFindsCreatedProvider(t *testing.T) {
	// Config with pre-existing providers ("lmstudio" and "ollama").
	// Adding "groq" creates a provider whose name sorts before both.
	// The old code took rows[len(rows)-1] (alphabetically-last = "ollama");
	// the fix must find the row for the newly created provider.
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"lmstudio": {Type: "openai_compatible", BaseURL: "http://localhost:1234/v1"},
		"ollama":   {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	m := New(cfg, t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)

	// Navigate to Providers section (index 1, after Agent).
	m = press(m, kp("j"), tea.KeyPressMsg{Code: tea.KeyEnter})

	// Open the wizard as the normal path would: openPicker stores onPick.
	req := providersWizard(m.state)()
	m.pickerOnPick = req.onPick
	m.pickerFieldID = req.fieldID

	// Simulate picking the "groq" template → creates provider, sets
	// wizardCreatedProvider, then drills into it.
	got, _ := m.handlePickerPicked("groq")

	// wizardCreatedProvider should have been consumed (cleared).
	if got.state.wizardCreatedProvider != "" {
		t.Fatalf("wizardCreatedProvider should be cleared after drill, got %q",
			got.state.wizardCreatedProvider)
	}

	// Should have drilled into the "groq" provider detail.
	pane := got.activePane()
	if pane.depth() != 2 {
		t.Fatalf("expected depth 2 after drilling, got %d", pane.depth())
	}
	if !strings.HasPrefix(pane.top().title, "groq") {
		t.Fatalf("drilled into provider %q, want title starting with 'groq'",
			pane.top().title)
	}

	// Verify "groq" actually exists in the config.
	if _, ok := got.state.cfg.Providers["groq"]; !ok {
		t.Fatal("wizard should have created providers.groq")
	}
}

func TestDrillIntoNewestProviderWithCollision(t *testing.T) {
	// "ollama" already exists, so wizard picking "ollama" should create
	// "ollama-2" and drill into it (not the alphabetically-last "openrouter").
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"lmstudio":   {Type: "openai_compatible"},
		"ollama":     {Type: "openai_compatible"},
		"openrouter": {Type: "openai_compatible"},
	}
	m := New(cfg, t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)

	m = press(m, kp("j"), tea.KeyPressMsg{Code: tea.KeyEnter})

	req := providersWizard(m.state)()
	m.pickerOnPick = req.onPick
	m.pickerFieldID = req.fieldID

	got, _ := m.handlePickerPicked("ollama")

	// "ollama-2" should be the created provider.
	if _, ok := got.state.cfg.Providers["ollama-2"]; !ok {
		t.Fatal("wizard picking ollama with existing ollama should create ollama-2")
	}

	pane := got.activePane()
	if pane.depth() != 2 {
		t.Fatalf("expected depth 2 after drilling, got %d", pane.depth())
	}
	if pane.top().title != "ollama-2" {
		t.Fatalf("drilled into provider %q, want 'ollama-2'", pane.top().title)
	}
}

func TestTruncateErrPreservesUTF8AndAddsEllipsis(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"short err", "short err"},
		{"", ""},
		// Exactly 40 chars: no truncation
		{"1234567890123456789012345678901234567890", "1234567890123456789012345678901234567890"},
		// 41 chars: truncate to 37 + ellipsis
		{"12345678901234567890123456789012345678901", "1234567890123456789012345678901234567\u2026"},
		// Multi-byte chars (é is 2 bytes)
		{"ééééééééééééééééééééé", "ééééééééééééééééééééé"},
		// Multi-byte that would split: 38 runes, 41+ bytes
		{"éééééééééééééééééééééé", "éééééééééééééééééééééé"},
		// 41-rune multi-byte string (each é is 2 bytes) that should be truncated
		{"éééééééééééééééééééééééééééééééééééééééééé", "ééééééééééééééééééééééééééééééééééééé\u2026"},
		// Mixed single and multi-byte, 42 runes → truncate to 37 runes + ellipsis
		{"abcdeééééééééééééééééééééééééééééééééééééé", "abcdeéééééééééééééééééééééééééééééééé\u2026"},
	}
	for _, tc := range tests {
		got := truncateErr(tc.input)
		if got != tc.want {
			t.Errorf("truncateErr(%q) = %q, want %q", tc.input, got, tc.want)
		}
		// Verify valid UTF-8
		if !utf8.ValidString(got) {
			t.Errorf("truncateErr(%q) produced invalid UTF-8: %q", tc.input, got)
		}
	}
}

func TestCtrlSOpensDiffOverlay(t *testing.T) {
	m := New(config.Default(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	if m.overlay != overlayDiff {
		t.Fatalf("overlay = %v, want overlayDiff", m.overlay)
	}
}

func TestDiffOverlayShowsNoChangesWhenClean(t *testing.T) {
	m := New(config.Default(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	view := m.View()
	if !strings.Contains(view, "no changes") {
		t.Fatalf("clean diff view should say 'no changes':\n%s", view)
	}
}

func TestDiffOverlayShowsChangeWhenDirty(t *testing.T) {
	cfg := config.Default()
	m := New(cfg, "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m.state.cfg.Privacy.RemoteProvidersAllowed = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	view := m.View()
	if !strings.Contains(view, "RemoteProvidersAllowed") {
		t.Fatalf("dirty diff view should mention the changed field:\n%s", view)
	}
}

func TestDiffOverlayEscReturnsToEditing(t *testing.T) {
	m := New(config.Default(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 30)
	m.paneFocused = true

	m, _ = m.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	m, _ = m.Update(tea.KeyPressMsg{Text: "esc"})
	if m.overlay != overlayNone {
		t.Fatalf("overlay = %v, want overlayNone after Esc", m.overlay)
	}
}

func TestDrillIntoNewestProviderClearedOnNoWizard(t *testing.T) {
	// When drillIntoNewestProvider is called without a wizard-created
	// provider, it should be a no-op (wizardCreatedProvider is empty).
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible"},
	}
	m := New(cfg, t.TempDir(), t.TempDir()+"/config.toml")
	m.SetSize(100, 32)

	m = press(m, kp("j"), tea.KeyPressMsg{Code: tea.KeyEnter})

	// Don't set up any wizard state; just call drillIntoNewestProvider.
	// It should be a no-op because wizardCreatedProvider is empty.
	m.drillIntoNewestProvider()

	pane := m.activePane()
	if pane.depth() != 1 {
		t.Fatalf("expected depth 1 (no drill), got %d", pane.depth())
	}
}
