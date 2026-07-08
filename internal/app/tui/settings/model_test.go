package settings

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

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
	if m.form == nil {
		t.Fatal("expected form to be built")
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
	m = initForm(m)

	view := stripANSI(m.View())
	for _, want := range []string{
		"Max tool iterations",
		"Max retries",
		"Shell timeout",
		"Max shell output",
		"Allow network",
		"Auto-approve shell",
		"12", // Max tool iterations value
		"90", // Shell timeout value
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestCancelReturnsCancelledMsg(t *testing.T) {
	m := New(newTestConfig(), "/tmp", "/tmp/.marshal/config.toml")
	m.SetSize(80, 24)
	m = initForm(m)
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
	m = initForm(m)

	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	maxW := 0
	for _, line := range lines {
		if w := len([]rune(line)); w > maxW {
			maxW = w
		}
	}
	// huh renders its own box; the width is governed by WithWidth(frameWidth)
	// which caps at 60. Allow a small margin for the focus border padding.
	if maxW > 62 {
		t.Fatalf("settings width = %d, want <= 62", maxW)
	}
	if maxW < 30 {
		t.Fatalf("settings width = %d, looks broken", maxW)
	}
}

// TestTabNavigatesBetweenFields verifies Tab moves focus between fields. We
// assert on the focused field's type changing, since huh uses a left border
// rather than a `>` prefix glyph. The first Tab is absorbed by the select's
// option cursor; the second advances to the Provider input.
func TestTabNavigatesBetweenFields(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)
	m = initForm(m)

	if _, ok := m.form.GetFocusedField().(*huh.Select[string]); !ok {
		t.Fatalf("first field should be a select, got %T", m.form.GetFocusedField())
	}
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if _, ok := m.form.GetFocusedField().(*huh.Input); !ok {
		t.Fatalf("Tab should move focus to an input field, got %T", m.form.GetFocusedField())
	}
}

func TestTypingUpdatesStringField(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)
	m = initForm(m)

	// Tab twice to reach the Provider input field.
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})

	m = sendUpdate(m, tea.KeyPressMsg{Text: "x"})
	// huh writes back via Validate on field exit; Tab away to trigger it.
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})

	if m.state.provider != "ollamax" {
		t.Fatalf("typing 'x' then leaving field should write back Provider, got %q", m.state.provider)
	}
}

func TestUpDownNavigatesBetweenFields(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)
	m = initForm(m)

	// Tab forward to an input field, then Shift+Tab back to the first field.
	// (huh uses Up/Down to move a select's *options*, not between fields, so
	// field navigation is driven by Tab/Shift+Tab.)
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if _, ok := m.form.GetFocusedField().(*huh.Input); !ok {
		t.Fatalf("Tab should move focus to an input field, got %T", m.form.GetFocusedField())
	}
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if _, ok := m.form.GetFocusedField().(*huh.Select[string]); !ok {
		t.Fatalf("Shift+Tab should move focus back to the first field, got %T", m.form.GetFocusedField())
	}
}

func TestConfirmFieldTogglesOnLeft(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)
	m = initForm(m)

	// Navigate to the Local only confirm field (index 4).
	for i := 0; i < 4; i++ {
		m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	}
	if _, ok := m.form.GetFocusedField().(*huh.Confirm); !ok {
		t.Fatalf("expected Confirm focused at index 4, got %T", m.form.GetFocusedField())
	}
	before := m.state.localOnly
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.state.localOnly == before {
		t.Fatalf("left should toggle Local only: before=%v after=%v", before, m.state.localOnly)
	}
}

func TestNumericFieldValidatesAndWritesBack(t *testing.T) {
	cfg := newTestConfig()
	m := New(cfg, "/repo", "/repo/.marshal/config.toml")
	m.SetSize(80, 24)
	m = initForm(m)

	// Navigate to Max tool iterations (index 6).
	for i := 0; i < 6; i++ {
		m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	}
	// Clear current value and type a new one.
	for i := 0; i < len(m.state.maxToolIter); i++ {
		m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "42" {
		m = sendUpdate(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	// Tab away to trigger Validate, which writes back to cfg.
	m = sendUpdate(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.state.cfg.Agent.MaxToolIterations != 42 {
		t.Fatalf("expected MaxToolIterations to be written back as 42, got %d", m.state.cfg.Agent.MaxToolIterations)
	}
}
