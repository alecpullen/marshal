package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestShellPaneTogglesAndLists(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "shell")
	if got := m.FocusedFieldTitle(); got != "Default timeout (s)" {
		t.Fatalf("focused = %q", got)
	}
	// tab to the first list and add an allow command
	m = keyPress(m, "tab")
	if got := m.FocusedFieldTitle(); got != "Allow commands" {
		t.Fatalf("tab should reach Allow commands, got %q", got)
	}
	m = keyPress(m, "a", "l", "s", "enter")
	cmds := m.state.cfg.Tools.Shell.Allow.Commands
	if cmds[len(cmds)-1] != "ls" {
		t.Fatalf("allow commands = %v", cmds)
	}
}

func TestSandboxPaneBackendSelect(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "sandbox")
	if got := m.FocusedFieldTitle(); got != "Backend" {
		t.Fatalf("focused = %q", got)
	}
}

func TestSandboxEnvListsEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "sandbox")
	m = keyPress(m, "tab", "tab") // form → allowlist → denylist
	if got := m.FocusedFieldTitle(); got != "Env denylist" {
		t.Fatalf("focused = %q", got)
	}
	m = keyPress(m, "a", "F", "O", "O", "enter")
	deny := m.state.cfg.Tools.Shell.Sandbox.EnvDenylist
	if len(deny) != 1 || deny[0] != "FOO" {
		t.Fatalf("denylist = %v", deny)
	}
}
