package settings

import (
	"regexp"
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

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// sendUpdate applies msg to the model and, if the returned command produces a
// message synchronously, feeds that single message back. This is enough to
// dispatch huh's NextField navigation commands (which return a func producing
// nextFieldMsg). Async cmds (cursor blink, window-size requests) are not
// drained — the bubbletea runtime owns those.
func sendUpdate(m Model, msg tea.Msg) Model {
	updated, cmd := m.Update(msg)
	m = updated
	if cmd != nil {
		if nextMsg := cmd(); nextMsg != nil {
			updated, _ = m.Update(nextMsg)
			m = updated
		}
	}
	return m
}

func initForm(m Model) Model {
	// The parent TUI never calls settings.Init(); the form focuses its first
	// field during construction. We follow the same path here.
	return m
}

func TestNewModelHasFields(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(100, 40)
	if !strings.Contains(stripANSI(m.View()), "Agent") {
		t.Fatal("view should contain the Agent sidebar entry")
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
	m = initForm(m)

	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	maxW := 0
	for _, line := range lines {
		if w := len([]rune(line)); w > maxW {
			maxW = w
		}
	}
	if maxW > 80 {
		t.Fatalf("settings width = %d, want <= 80", maxW)
	}
	if maxW < 30 {
		t.Fatalf("settings width = %d, looks broken", maxW)
	}
}
