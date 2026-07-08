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
	if len(m.fields) == 0 {
		t.Fatal("expected fields")
	}
}

func TestSettingsExposeAgentAndToolFields(t *testing.T) {
	cfg := newTestConfig()
	cfg.Agent.MaxToolIterations = 12
	cfg.Agent.MaxRetries = 3
	cfg.Tools.Shell.DefaultTimeoutSeconds = 90
	cfg.Tools.Shell.MaxOutputBytes = 123456
	cfg.Tools.Shell.AllowNetwork = true
	cfg.Tools.Shell.AutoApprove = false

	m := New(cfg, "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 24)

	view := m.View()
	for _, want := range []string{
		"Max tool iterations",
		"Max retries",
		"Shell timeout",
		"Max shell output",
		"Allow network",
		"Auto-approve shell",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing settings field %q:\n%s", want, view)
		}
	}

}

func TestCancelReturnsCancelledMsg(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	m.SetSize(80, 24)

	view := m.View()
	lines := strings.Split(view, "\n")
	maxW := 0
	for _, line := range lines {
		if w := len([]rune(line)); w > maxW {
			maxW = w
		}
	}
	if maxW > 60 {
		t.Fatalf("settings width = %d, want <= 60", maxW)
	}
	if maxW < 30 {
		t.Fatalf("settings width = %d, looks broken", maxW)
	}
	first := lines[0]
	last := lines[len(lines)-2]
	if !strings.HasPrefix(first, "┌") || !strings.HasSuffix(first, "┐") {
		t.Fatalf("top border broken: %q", first)
	}
	if !strings.HasPrefix(last, "└") || !strings.HasSuffix(last, "┘") {
		t.Fatalf("bottom border broken: %q", last)
	}
}
