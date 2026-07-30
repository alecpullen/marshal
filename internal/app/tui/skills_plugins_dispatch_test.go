package tui

import (
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/plugins"
	"marshal/internal/app/tui/skills"
	"marshal/internal/commands"
)

func TestSkillsCommandOpensPanel(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := New(state, WithHomeDir(t.TempDir()), WithWorkingDir(t.TempDir()), WithCommandRegistry(reg))

	m.dispatchCommand("/skills")
	if _, ok := m.dock.Panel().(*skills.Panel); !ok {
		t.Fatalf("expected skills panel, got %T", m.dock.Panel())
	}
}

func TestPluginsCommandOpensPanel(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := New(state, WithHomeDir(t.TempDir()), WithWorkingDir(t.TempDir()), WithCommandRegistry(reg))

	m.dispatchCommand("/plugins")
	if _, ok := m.dock.Panel().(*plugins.Panel); !ok {
		t.Fatalf("expected plugins panel, got %T", m.dock.Panel())
	}
}
