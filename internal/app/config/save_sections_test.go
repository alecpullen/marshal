package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/llm/routing"
)

// TestSaveProjectConfigWritesSessionRollover pins the fix for the sections
// writeSections silently dropped: a session.rollover.set write must
// survive a save/load round trip.
func TestSaveProjectConfigWritesSessionRollover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	cfg.Session.Rollover.Enabled = true
	cfg.Session.Rollover.ContextPercentThreshold = 55

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "[session.rollover]") {
		t.Errorf("session.rollover section missing from:\n%s", data)
	}

	loaded, err := Load(LoadOptions{WorkingDir: dir, HomeDir: filepath.Join(dir, "home")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Session.Rollover.Enabled {
		t.Error("session.rollover.enabled = false after round trip, want true")
	}
	if loaded.Session.Rollover.ContextPercentThreshold != 55 {
		t.Errorf("context_percent_threshold = %d, want 55", loaded.Session.Rollover.ContextPercentThreshold)
	}
}

func TestSaveProjectConfigWritesLSP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	disabled := false
	cfg.LSP.Enabled = &disabled
	cfg.LSP.Servers = map[string]LSPServerConfig{
		"go": {Command: "gopls"},
	}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(LoadOptions{WorkingDir: dir, HomeDir: filepath.Join(dir, "home")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.LSP.Enabled == nil || *loaded.LSP.Enabled {
		t.Error("lsp.enabled did not survive the round trip as false")
	}
	if got := loaded.LSP.Servers["go"]; got.Command != "gopls" {
		t.Errorf("lsp.servers.go.command = %q, want gopls", got.Command)
	}
}

func TestSaveProjectConfigWritesHistoryScratchpadAgents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	cfg.History.Enabled = false
	cfg.Scratchpad.MaxEntries = 7
	cfg.Agents = map[routing.AgentRole]AgentRoleConfig{
		routing.RolePlanner: {Context: routing.ContextBudget{MaxRepoContextTokens: 1234}},
	}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(LoadOptions{WorkingDir: dir, HomeDir: filepath.Join(dir, "home")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.History.Enabled {
		t.Error("history.enabled did not survive the round trip as false")
	}
	if loaded.Scratchpad.MaxEntries != 7 {
		t.Errorf("scratchpad.max_entries = %d, want 7", loaded.Scratchpad.MaxEntries)
	}
	if got := loaded.Agents[routing.RolePlanner]; got.Context.MaxRepoContextTokens != 1234 {
		t.Errorf("agents.planner.context = %+v, want 1234", got.Context)
	}
}