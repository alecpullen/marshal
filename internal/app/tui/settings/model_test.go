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
