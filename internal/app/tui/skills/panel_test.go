package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/settings"
)

func TestPanelRootList(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	globalDir := filepath.Join(home, ".config", "marshal", "skills")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(globalDir, "debug.md")
	content := `+++
name = "debug"
description = "debugging skill"
+++

# Debug`
	if err := os.WriteFile(skillFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	state := session.New(config.Config{}, work, time.Now(), session.Persistence{})
	p := NewPanel(home, work, true, state)

	rows := settings.FieldListRows(p.list)
	if len(rows) == 0 {
		t.Fatalf("expected rows, got none")
	}
	found := false
	for _, r := range rows {
		if settings.FieldID(r) == "skill.debug" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skill.debug row, rows: %v", rows)
	}
}

func TestPanelProjectScopeDisabled(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	state := session.New(config.Config{}, work, time.Now(), session.Persistence{})
	p := NewPanel(home, work, false, state)

	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open install frame
	// The install frame should only list "global" in the scope enum.
	// This is a smoke test; exact assertions depend on frame navigation.
}
