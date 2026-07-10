package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func TestPermissionsPaneAddAndEdit(t *testing.T) {
	m := New(config.Default(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "permissions")
	m = keyPress(m, "a", "enter")
	if len(m.state.cfg.Permissions.Rules) != 1 {
		t.Fatalf("add should append a rule, got %d", len(m.state.cfg.Permissions.Rules))
	}
	m = keyPress(m, "enter") // edit
	if got := m.FocusedFieldTitle(); got != "Permission" {
		t.Fatalf("focused = %q, want Permission", got)
	}
}

func TestPermissionsPaneActionSelect(t *testing.T) {
	// Slow huh-driven test (~20s) because the action selector rebuilds
	// its form repeatedly during navigation.
	if testing.Short() {
		t.Skip("slow huh-driven test")
	}

	cfg := config.Default()
	cfg.Permissions.Rules = []config.PermissionRule{{Permission: "shell", Pattern: "go *", Action: "allow"}}
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "permissions")
	m = keyPress(m, "enter") // edit
	// Permission, Pattern, Action — navigate to Action (index 2)
	m = keyPress(m, "down", "down")
	if got := m.FocusedFieldTitle(); got != "Action" {
		t.Fatalf("focused = %q, want Action", got)
	}
}
