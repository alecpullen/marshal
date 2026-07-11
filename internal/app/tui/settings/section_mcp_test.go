package settings

import (
	"reflect"
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

// TestMCPPaneServerEditPreservesSpacesInArgs asserts that an argument
// containing a space round-trips through the sub-pane without being
// re-tokenized. The previous space-separated string input corrupted paths
// like "/Users/me/My Project".
func TestMCPPaneServerEditPreservesSpacesInArgs(t *testing.T) {
	// Slow huh-driven test (~7s) because the sub-pane walks both the
	// scalar form and the listStrings add/commit cycle.
	if testing.Short() {
		t.Skip("slow huh-driven test")
	}

	cfg := mcpTestConfig()
	cfg.MCP.Servers["fs"] = config.MCPServerConfig{
		Command: "mcp-fs",
		Args:    []string{"--root", "/Users/me/My Project", "--flag"},
		Env:     map[string]string{},
	}
	m := New(cfg, t.TempDir(), "")
	m.SetSize(100, 40)
	m = enterSection(t, m, "mcp")
	m = keyPress(m, "tab") // servers
	m = keyPress(m, "enter")
	// Args are now an editable list: tab to reach them, add a new arg
	// with a space, then esc out to commit.
	m = keyPress(m, "tab") // form -> args listStrings
	m = keyPress(m, "a")   // enter add mode
	m = keyPress(m, "-", "-", "p", "a", "t", "h", "space", "/", "a", " ", "b", "/", "c")
	m = keyPress(m, "enter") // commit add
	m = keyPress(m, "esc")   // close sub-pane (commit)
	if !reflect.DeepEqual(m.state.cfg.MCP.Servers["fs"].Args,
		[]string{"--root", "/Users/me/My Project", "--flag", "--path /a b/c"}) {
		t.Fatalf("args round-trip lost spaces: %v", m.state.cfg.MCP.Servers["fs"].Args)
	}
	if !reflect.DeepEqual(m.state.cfg.MCP.Servers["fs"].Args,
		[]string{"--root", "/Users/me/My Project", "--flag", "--path /a b/c"}) {
		t.Fatalf("args round-trip lost spaces: %v", m.state.cfg.MCP.Servers["fs"].Args)
	}
}
