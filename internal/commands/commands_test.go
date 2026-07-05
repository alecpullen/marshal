package commands

import (
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func newTestState() *session.State {
	return session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
}

func TestNew(t *testing.T) {
	reg := New()
	if reg == nil {
		t.Fatal("New() returned nil")
	}
	if len(reg.commands) != 0 {
		t.Errorf("expected empty commands map, got %d commands", len(reg.commands))
	}
}

func TestRegister(t *testing.T) {
	reg := New()
	err := reg.Register(Command{Name: "test", Handler: func(s *session.State, a []string) string { return "" }})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	reg := New()
	h := func(s *session.State, a []string) string { return "" }
	reg.Register(Command{Name: "test", Handler: h})
	err := reg.Register(Command{Name: "test", Handler: h})
	if !errors.Is(err, ErrDuplicateCommand) {
		t.Errorf("expected ErrDuplicateCommand, got %v", err)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	reg := New()
	err := reg.Register(Command{Name: "", Handler: func(s *session.State, a []string) string { return "" }})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Errorf("expected ErrInvalidCommand, got %v", err)
	}
}

func TestRegisterNilHandler(t *testing.T) {
	reg := New()
	err := reg.Register(Command{Name: "test"})
	if !errors.Is(err, ErrInvalidCommand) {
		t.Errorf("expected ErrInvalidCommand, got %v", err)
	}
}

func TestLookup(t *testing.T) {
	reg := New()
	reg.Register(Command{Name: "test", Description: "desc", Handler: func(s *session.State, a []string) string { return "ok" }})

	cmd, ok := reg.Lookup("test")
	if !ok {
		t.Fatal("Lookup() not found")
	}
	if cmd.Description != "desc" {
		t.Errorf("expected desc, got %s", cmd.Description)
	}
	result := cmd.Handler(newTestState(), nil)
	if result != "ok" {
		t.Errorf("expected ok, got %s", result)
	}
}

func TestLookupNotFound(t *testing.T) {
	reg := New()
	_, ok := reg.Lookup("nonexistent")
	if ok {
		t.Error("Lookup() should not find nonexistent command")
	}
}

func TestList(t *testing.T) {
	reg := New()
	reg.Register(Command{Name: "b", Handler: func(s *session.State, a []string) string { return "" }})
	reg.Register(Command{Name: "a", Handler: func(s *session.State, a []string) string { return "" }})

	cmds := reg.List()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0].Name != "a" || cmds[1].Name != "b" {
		t.Errorf("commands not sorted: got %s, %s", cmds[0].Name, cmds[1].Name)
	}
}

func TestRegisterAll(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	err := RegisterAll(cmdReg, toolReg)
	if err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}
	cmds := cmdReg.List()
	if len(cmds) < 10 {
		t.Errorf("expected at least 10 commands, got %d", len(cmds))
	}
}

func TestHelpCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, _ := cmdReg.Lookup("help")
	result := cmd.Handler(newTestState(), nil)
	if !strings.Contains(result, "Available commands") {
		t.Errorf("help output missing header: %s", result)
	}
}

func TestToolsCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	cmd, _ := cmdReg.Lookup("tools")
	result := cmd.Handler(newTestState(), nil)
	if !strings.Contains(result, "Available tools") {
		t.Errorf("tools output missing header: %s", result)
	}
}

func TestRouteCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	state.SetActiveRoute(session.RouteInfo{
		Role:     "planner",
		Profile:  "default",
		Preset:   "gpt4",
		Provider: "openai",
		Model:    "gpt-4",
		Active:   true,
	})

	cmd, _ := cmdReg.Lookup("route")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "gpt-4") {
		t.Errorf("route output missing model info: %s", result)
	}
}

func TestContextCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "hi there", session.ContentTypePlain)

	cmd, _ := cmdReg.Lookup("context")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Messages: 2") {
		t.Errorf("context output missing message count: %s", result)
	}
}

func TestNewCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "hi", session.ContentTypePlain)

	cmd, _ := cmdReg.Lookup("new")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Cleared 2 messages") {
		t.Errorf("new command output wrong: %s", result)
	}
	if len(state.Messages()) != 0 {
		t.Errorf("expected 0 messages after clear, got %d", len(state.Messages()))
	}
}

func TestConfigCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("config")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Configuration") {
		t.Errorf("config output missing header: %s", result)
	}
}

func TestRollbackNoBackup(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("rollback")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "No backup available") {
		t.Errorf("rollback output wrong: %s", result)
	}
}

func TestExitQuitCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("exit")
	result := cmd.Handler(state, nil)
	if result != "Goodbye!" {
		t.Errorf("expected Goodbye!, got %s", result)
	}

	cmd, _ = cmdReg.Lookup("quit")
	result = cmd.Handler(state, nil)
	if result != "Goodbye!" {
		t.Errorf("expected Goodbye!, got %s", result)
	}
}

func TestModeSwitchCommands(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("ask")
	result := cmd.Handler(state, nil)
	if !strings.Contains(result, "Ask mode") {
		t.Errorf("ask output wrong: %s", result)
	}

	cmd, _ = cmdReg.Lookup("edit")
	result = cmd.Handler(state, nil)
	if !strings.Contains(result, "Edit mode") {
		t.Errorf("edit output wrong: %s", result)
	}

	cmd, _ = cmdReg.Lookup("auto")
	result = cmd.Handler(state, nil)
	if !strings.Contains(result, "Auto mode") {
		t.Errorf("auto output wrong: %s", result)
	}
}

func TestStopCommand(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("stop")
	result := cmd.Handler(state, nil)
	if result != "" {
		t.Errorf("stop handler should return empty string, got %s", result)
	}
}

func TestModelCommandEmptyArgs(t *testing.T) {
	cmdReg := New()
	toolReg := registry.New()
	RegisterAll(cmdReg, toolReg)

	state := newTestState()
	cmd, _ := cmdReg.Lookup("model")
	result := cmd.Handler(state, nil)
	if result != "" {
		t.Errorf("model handler should return empty string when no args, got %s", result)
	}
}

func TestRegisterAllIncludesSwarmCommand(t *testing.T) {
	cmdReg := New()
	if err := RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	cmd, ok := cmdReg.Lookup("swarm")
	if !ok {
		t.Fatal("swarm command not registered")
	}
	if cmd.Args != "<goal>" {
		t.Fatalf("swarm Args = %q, want \"<goal>\"", cmd.Args)
	}
	// The handler is a no-op; the TUI special-cases dispatch like /ask.
	if got := cmd.Handler(nil, []string{"fix", "bug"}); got != "" {
		t.Fatalf("swarm handler returned %q, want empty", got)
	}
}
