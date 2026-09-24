package tui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/mcpauth"
	"marshal/internal/commands"
	"marshal/internal/credentials"
	"marshal/internal/tools/mcp/oauth"
)

// handleMCPCommand implements /mcp. Only the "auth <name>" subcommand is
// recognised: it drives the OAuth authorization-code flow for a remote MCP
// server whose config sets auth = "oauth".
func (m *Model) handleMCPCommand(args []string) (tea.Model, tea.Cmd) {
	if len(args) == 0 || strings.ToLower(args[0]) != "auth" {
		m.state.AddMessage(session.RoleSystem, "Usage: /mcp auth <name>", session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}

	name := strings.TrimSpace(strings.Join(args[1:], " "))
	srv, err := commands.ResolveOAuthServer(m.state.Config.MCP.Servers, name)
	if err != nil {
		m.state.AddMessage(session.RoleSystem, err.Error(), session.ContentTypePlain)
		m.refreshViewport()
		return m, nil
	}

	store, err := credentials.New("marshal")
	if err != nil {
		if errors.Is(err, credentials.ErrKeyringUnavailable) {
			m.state.AddMessage(session.RoleSystem,
				"OAuth tokens are stored in the OS keychain, which is unavailable here. "+
					"Run Marshal on a machine with a working Keychain, Credential Manager, or Secret Service, then retry /mcp auth "+name+".",
				session.ContentTypePlain)
		} else {
			m.state.AddMessage(session.RoleSystem, "Cannot open the credential store: "+err.Error(), session.ContentTypePlain)
		}
		m.refreshViewport()
		return m, nil
	}

	disp := mcpauth.NewDisplay()
	panel := mcpauth.NewPanel(name, disp)
	ctx, cancel := context.WithCancel(m.ctx)
	panel.SetCancel(cancel)
	m.mcpAuthServer = name

	eng := &oauth.Engine{
		ServerURL:  srv.URL,
		ClientName: "marshal",
		Store:      store,
		Open:       func(u string) { _ = mcpauth.OpenBrowser(u) },
		Logger:     m.state.Logger(),
	}

	m.dock.Open(panel)
	m.refreshViewport()
	return m, tea.Batch(mcpauth.Tick(), authorizeMCPCmd(ctx, eng, disp, cancel))
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
	m.state.ClearNotice(session.NoticeProvider)
	m.state.ClearNotice(session.NoticeConfig)
	// reloadAgentRuntime may install cfg before reporting a cleanup error;
	// invalidate config-derived state before attempting it, mirroring the
	// settings-browser and /set reload paths.
	m.setReg = nil
	if err := m.configReloader(cfg); err != nil {
		m.applyNewConfig(cfg)
		m.state.AddMessage(session.RoleSystem,
			"\u2717 Authorized MCP server \""+name+"\", but it could not be connected: "+err.Error(),
			session.ContentTypePlain)
		return
	}
	// The provider notice is already cleared above; adopt a runner rebuilt
	// after a startup failure without re-clearing the notices this reload set.
	m.adoptRunner()
	m.applyNewConfig(cfg)
	m.refreshDiagnostics()
}

// authorizeMCPCmd runs the blocking Authorize call off the UI goroutine and
// reports its terminal error through AuthDoneMsg. The context is cancelled on
// return so the loopback receiver is always torn down; on cancellation
// Authorize returns before persisting, so no partial token set is written.
func authorizeMCPCmd(ctx context.Context, eng *oauth.Engine, disp oauth.Display, cancel context.CancelFunc) tea.Cmd {
	return func() tea.Msg {
		defer cancel()
		return mcpauth.AuthDoneMsg{Err: eng.Authorize(ctx, disp)}
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
		m.state.AddMessage(session.RoleSystem,
			"\u2713 Authorized MCP server \""+name+"\". Tokens are stored in the OS keychain.",
			session.ContentTypePlain)
		m.reconnectMCPServer(name)
	case errors.Is(msg.Err, context.Canceled):
		m.state.AddMessage(session.RoleSystem,
			"Authorization cancelled; no credentials were saved.",
			session.ContentTypePlain)
	default:
		m.state.AddMessage(session.RoleSystem,
			"Authorization failed: "+msg.Err.Error(),
			session.ContentTypePlain)
	}
	m.refreshViewport()
	return m, nil
}
