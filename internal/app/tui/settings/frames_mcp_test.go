package settings

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
)

func TestMCPServerNestedDrill(t *testing.T) {
	s := newState(config.Default())
	s.cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"github": {Command: "gh-mcp", Args: []string{"--stdio"}, Env: map[string]string{}},
	}
	ps := newPaneStack(mcpFrame(s))
	ps.SetSize(80, 24)

	// root rows: Disclosure threshold, Servers, Policies
	rows := ps.top().list.Rows()
	if len(rows) != 3 {
		t.Fatalf("expected 3 root rows, got %d", len(rows))
	}

	// drill: Servers → github → Args
	ps.top().list.SetCursor(1)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Servers
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // github
	for ps.top().list.CursorRow().title != "Args" {
		ps.Update(kp("j"))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Args
	if got := ps.breadcrumb("MCP"); got != "MCP › Servers › github › Args" {
		t.Fatalf("breadcrumb wrong: %q", got)
	}
	// add an arg and confirm it lands in the working copy
	ps.Update(kp("a"))
	for _, r := range "-v" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := s.cfg.MCP.Servers["github"].Args; len(got) != 2 || got[1] != "-v" {
		t.Fatalf("arg add should apply to working copy, got %v", got)
	}
}
