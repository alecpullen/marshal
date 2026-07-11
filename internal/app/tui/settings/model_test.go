package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func newTestConfig() config.Config {
	cfg := config.Default()
	cfg.Profile.Default = "local_balanced"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b", LocalOnly: true},
	}
	return cfg
}

func TestNewModelHasFields(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(100, 40)
	view := stripANSI(m.View())
	for _, title := range []string{"Agent", "Providers"} {
		if !strings.Contains(view, title) {
			t.Errorf("view should contain sidebar entry %q", title)
		}
	}
}

func TestCancelReturnsCancelledMsg(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 24)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected command")
	}
	msg := cmd()
	if _, ok := msg.(CancelledMsg); !ok {
		t.Fatalf("expected CancelledMsg, got %T", msg)
	}
}

func TestDirtyEscRequiresConfirmation(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 24)
	// Make the working copy dirty.
	m.state.cfg.Privacy.RemoteProvidersAllowed = true
	if !m.dirty() {
		t.Fatal("setup: model must be dirty")
	}
	// First Esc: should NOT cancel; should set pendingCancel.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil {
		t.Fatalf("first Esc on dirty model must not emit a command, got %v", cmd())
	}
	if !m.pendingCancel {
		t.Fatal("first Esc on dirty model must set pendingCancel")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "unsaved changes") {
		t.Error("pending-cancel footer should mention unsaved changes")
	}
	// Second Esc: should emit CancelledMsg.
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("second Esc must emit CancelledMsg")
	}
	if _, ok := cmd().(CancelledMsg); !ok {
		t.Fatalf("second Esc should emit CancelledMsg, got %T", cmd())
	}
	// After cancel, pendingCancel is cleared.
	if m.pendingCancel {
		t.Error("pendingCancel must clear after confirmed cancel")
	}
}

func TestDirtyCtrlSClearsPendingCancel(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 24)
	m.state.cfg.Privacy.RemoteProvidersAllowed = true
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.pendingCancel {
		t.Fatal("setup: pendingCancel should be set")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: rune('s'), Mod: tea.ModCtrl})
	if m.pendingCancel {
		t.Error("Ctrl+S must clear pendingCancel")
	}
}

func TestSidebarHiddenAtNarrowWidth(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 40)
	if !m.sidebarHidden {
		t.Fatal("sidebar should be hidden at width 50 (< 70)")
	}
	view := stripANSI(m.View())
	// Sidebar items are rendered with a leading space (" Model Presets").
	// When the sidebar is hidden, no sidebar items should appear.
	if strings.Contains(view, " Model Presets") {
		t.Error("narrow view should not contain sidebar item")
	}
	// Verify the pane header still shows the active section title.
	if !strings.Contains(view, "Agent") {
		t.Error("narrow view should still show active pane header")
	}
}

func TestNarrowViewDoesNotOverflow(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 30)
	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	for _, line := range lines {
		if w := len([]rune(line)); w > m.width {
			t.Errorf("line width = %d, exceeds terminal width %d: %q", w, m.width, line)
		}
	}
}

func TestSidebarVisibleAboveBreakpoint(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(100, 40)
	if m.sidebarHidden {
		t.Fatal("sidebar should be visible at width 100")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Agent") {
		t.Error("wide view should contain sidebar title 'Agent'")
	}
}

func TestLeftRightCycleSectionsWhenSidebarHidden(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 40)
	m.cursor = 2 // start at "Model Presets"

	// right should advance cursor
	m = keyPress(m, "right")
	if m.cursor != 3 {
		t.Errorf("right with sidebar hidden should advance cursor to 3, got %d", m.cursor)
	}

	// left should go back
	m = keyPress(m, "left")
	if m.cursor != 2 {
		t.Errorf("left with sidebar hidden should move cursor back to 2, got %d", m.cursor)
	}

	// l (lowercase L) should also advance
	m = keyPress(m, "l")
	if m.cursor != 3 {
		t.Errorf("l with sidebar hidden should advance cursor to 3, got %d", m.cursor)
	}

	// h should go back
	m = keyPress(m, "h")
	if m.cursor != 2 {
		t.Errorf("h with sidebar hidden should move cursor back to 2, got %d", m.cursor)
	}
}

func TestTabFocusesPaneWhenSidebarHidden(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 40)
	if !m.sidebarHidden {
		t.Fatal("setup: sidebar should be hidden")
	}
	m = keyPress(m, "tab")
	if !m.paneFocused {
		t.Fatal("tab should focus the active pane when the sidebar is hidden")
	}
}

func TestHelpViewAdaptsWhenSidebarHidden(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 40)
	m.helpOpen = true
	view := stripANSI(m.View())
	if !strings.Contains(view, "Tab           enter pane") {
		t.Fatalf("hidden-sidebar help should explain Tab enters pane:\n%s", view)
	}
	if strings.Contains(view, "back to sidebar") {
		t.Fatalf("hidden-sidebar help should not mention a sidebar that is hidden:\n%s", view)
	}
}

func TestPaneFocusedLeftMovesPreviousSectionWhenSidebarHidden(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 40)
	m.cursor = 2
	m.paneFocused = true
	m = keyPress(m, "left")
	if m.cursor != 1 {
		t.Fatalf("left in pane-focused hidden-sidebar mode should move to previous section, got %d", m.cursor)
	}
	if !m.paneFocused {
		t.Fatal("left in pane-focused hidden-sidebar mode should keep pane focus")
	}
}

func TestPaneFocusedShiftTabMovesPreviousSectionWhenSidebarHidden(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 40)
	m.cursor = 2
	m.paneFocused = true
	m = keyPress(m, "shift+tab")
	if m.cursor != 1 {
		t.Fatalf("shift+tab in pane-focused hidden-sidebar mode should move to previous section, got %d", m.cursor)
	}
	if !m.paneFocused {
		t.Fatal("shift+tab in pane-focused hidden-sidebar mode should keep pane focus")
	}
}

func TestPendingCancelFooterStaysBoundedWhenSidebarHidden(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(50, 40)
	m.pendingCancel = true
	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > m.width {
			t.Fatalf("line width = %d exceeds terminal width %d: %q", w, m.width, line)
		}
	}
}

func TestSettingsViewKeepsFrameBounded(t *testing.T) {
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"default": {Roles: map[routing.AgentRole]string{routing.RoleImplementer: "local"}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"local": {Name: "local", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 30)

	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	maxW := 0
	for _, line := range lines {
		if w := len([]rune(line)); w > maxW {
			maxW = w
		}
	}
	if maxW > m.width {
		t.Fatalf("settings width = %d, want <= %d", maxW, m.width)
	}
	if maxW < 30 {
		t.Fatalf("settings width = %d, looks broken", maxW)
	}
}
