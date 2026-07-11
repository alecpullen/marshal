package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
