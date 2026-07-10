package settings

import (
	"testing"

	"marshal/internal/app/config"
)

func mcpTestConfig() config.Config {
	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"fs": {Command: "mcp-fs", Args: []string{"--root", "."}, Env: map[string]string{"A": "1"}},
	}
	cfg.MCP.Policies = map[string]string{"fs": "confirm"}
	return cfg
}

func TestMCPPaneListsServers(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	if got := m.FocusedFieldTitle(); got != "Disclosure threshold tools" {
		t.Fatalf("focused = %q, want Disclosure threshold tools", got)
	}
}

func TestMCPPaneTabToServers(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	m = keyPress(m, "tab")
	if got := m.FocusedFieldTitle(); got != "Servers" {
		t.Fatalf("tab should reach the servers collection, got %q", got)
	}
}

func TestMCPPaneTabToPoliciesMap(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	m = keyPress(m, "tab", "tab") // form → servers → policies
	if got := m.FocusedFieldTitle(); got != "Policies" {
		t.Fatalf("tab should reach the policies map, got %q", got)
	}
}

func TestMCPPaneAddServer(t *testing.T) {
	m := New(mcpTestConfig(), t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	m = keyPress(m, "tab") // servers
	m = keyPress(m, "a", "g", "i", "t", "enter")
	if _, ok := m.state.cfg.MCP.Servers["git"]; !ok {
		t.Fatal("add should create the git server entry")
	}
}
