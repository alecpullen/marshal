package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/tui/mcpauth"
)

// handleMCPCommand implements /mcp. Only the "auth <name>" subcommand is
// recognised: it drives the OAuth authorization-code flow for a remote MCP
// server whose config sets auth = "oauth".
func (m *Model) handleMCPCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 || strings.ToLower(args[0]) != "auth" {
		m.note("Usage: /mcp auth <name>")
		m.refreshViewport()
		return m, nil
	}

	name := strings.TrimSpace(strings.Join(args[1:], " "))
	if err := m.sess.PrepareMCPLogin(m.ctx, name); err != nil {
		m.note(err.Error())
		m.refreshViewport()
		return m, nil
	}

	disp := mcpauth.NewDisplay()
	panel := mcpauth.NewPanel(name, disp)
	ctx, cancel := context.WithCancel(m.ctx)
	panel.SetCancel(cancel)
	m.mcpAuthServer = name

	m.dock.Open(panel)
	m.refreshViewport()
	return m, tea.Batch(mcpauth.Tick(), authorizeMCPCmd(ctx, m.sess, name, disp, m.openBrowser, cancel))
}

// reconnectMCPServer rebuilds the runtime so a server that failed to start
// for want of a token is started now that Authorize has stored one.
//
// Startup is the only place MCP servers are connected and their tools are
// registered: a server that returned ErrAuthRequired was recorded as a
// per-server failure and never entered the manager's client set, so it is
// invisible to the agent until the runtime is rebuilt. Reloading the
// (unchanged) config runs buildAgentRunnerWithLock again, which constructs a
// fresh manager, starts the now-authorized server, and registers its tools on
// the registry the runner is swapped onto. Without this, /mcp auth would
// report success while leaving the server's tools unreachable until restart.
func (m *Model) reconnectMCPServer(name string) {
	if m.configReloader == nil {
		// No reload path (embedded or test models). The token is stored, so a
		// later runtime build connects the server.
		return
	}
	cfg := m.state.Config
	// Clear the startup notices BEFORE the rebuild, not after. The stale
	// "needs OAuth" notice is exactly what this flow resolves, so it must go —
	// but if the server still fails to start, the rebuild sets a fresh notice
	// of its own, and clearing notices afterwards (as afterRuntimeReload does)
	// would wipe it and leave the user with a bare success message.
	_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
	_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeConfig)
	// reloadAgentRuntime may install cfg before reporting a cleanup error;
	// invalidate config-derived state before attempting it, mirroring the
	// settings-browser and /set reload paths.
	m.setReg = nil
	if err := m.configReloader(cfg); err != nil {
		m.applyNewConfig(cfg)
		m.note("\u2717 Authorized MCP server \"" + name + "\", but it could not be connected: " + err.Error())
		return
	}
	// A runner the reload builds is used from the next turn: the session reads
	// the runtime's current runner on each call, so nothing is adopted here.
	m.applyNewConfig(cfg)
	m.refreshDiagnostics()
}

// authorizeMCPCmd runs the blocking sign-in off the UI goroutine and reports
// its terminal error through AuthDoneMsg. The context is cancelled on return
// so the loopback receiver is always torn down; on cancellation the agent side
// returns before persisting, so no partial token set is written.
//
// The URL arrives through show: the TUI opens the browser and then records it
// for the panel, the order the engine itself used.
func authorizeMCPCmd(ctx context.Context, sess acpclient.Session, server string, disp *mcpauth.Display, open func(url string) error, cancel context.CancelFunc) tea.Cmd {
	if open == nil {
		open = mcpauth.OpenBrowser
	}
	return func() tea.Msg {
		defer cancel()
		err := sess.LoginMCP(ctx, server, func(u string) {
			_ = open(u)
			disp.ShowURL(u)
		})
		return mcpauth.AuthDoneMsg{Err: err}
	}
}

// handleMCPAuthDone closes the authorization panel and reports the outcome.
// Cancellation (Esc or shutdown) is a clean abort with no keychain write.
func (m *Model) handleMCPAuthDone(msg mcpauth.AuthDoneMsg) (tea.Model, tea.Cmd) {
	name := m.mcpAuthServer
	m.mcpAuthServer = ""
	m.dock.CloseNow()
	switch {
	case msg.Err == nil:
		m.note("\u2713 Authorized MCP server \"" + name + "\". Tokens are stored in the OS keychain.")
		m.reconnectMCPServer(name)
	case errors.Is(msg.Err, context.Canceled):
		m.note("Authorization cancelled; no credentials were saved.")
	default:
		m.note("Authorization failed: " + msg.Err.Error())
	}
	m.refreshViewport()
	return m, nil
}
