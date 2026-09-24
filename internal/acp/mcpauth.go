package acp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/credentials"
	"marshal/internal/tools/mcp/oauth"
)

// headlessCommandImpl is the signature of a command the manager implements
// itself for the wire surface. sessionID is the ACP session the command was
// dispatched for (needed to address notifications).
type headlessCommandImpl func(ctx context.Context, sessionID string, rt *CommandRuntime, args []string) (commands.Result, error)

// headlessCommandNames is the package-level set of commands the manager
// implements itself for the wire surface. Kept beside headlessCommands (and
// checked by a test) so the two cannot drift.
var headlessCommandNames = map[string]bool{
	"mcp": true,
}

// headlessCommands maps a command name to the manager's own wire
// implementation. Each entry corresponds to a commands.Command registered
// TUIOnly whose interactive form lives in the TUI dispatch table; the
// manager supplies the non-interactive equivalent here so
// session/command_list reports it as "headless" and session/command can run
// it. This mirrors commands that carry both TUIOnly and a Handler (e.g.
// /trust), except the implementation lives in the manager so the wire-only
// pieces (notifications) are reachable.
func (m *CommandManager) headlessCommands() map[string]headlessCommandImpl {
	return map[string]headlessCommandImpl{
		"mcp": m.mcpAuth,
	}
}

// supportsHeadless reports whether the manager implements name over the
// wire itself. The registry entry may be TUIOnly; the manager's
// implementation is what makes the command headless-capable.
func (m *CommandManager) supportsHeadless(name string) bool {
	return headlessCommandNames[strings.ToLower(strings.TrimSpace(name))]
}

// runHeadless dispatches name to the manager's own implementation. The
// caller has already established that name is registered (so a description,
// args, and group are reported) and that supportsHeadless is true.
func (m *CommandManager) runHeadless(ctx context.Context, sessionID string, rt *CommandRuntime, name string, args []string) (commands.Result, error) {
	fn, ok := m.headlessCommands()[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return commands.Result{}, &jsonRPCError{Code: internalError, Message: fmt.Sprintf("command %q has no headless implementation", name)}
	}
	return fn(ctx, sessionID, rt, args)
}

// mcpAuth is the headless /mcp auth <name> implementation. It resolves the
// named server with commands.ResolveOAuthServer (the same validation the TUI
// runs, so failure sentences match), opens the OS credential store, and
// drives oauth.Engine.Authorize against a display that emits the
// authorization URL to the connected client.
//
// Authorize blocks until the loopback callback arrives, the context is
// cancelled, or the five-minute loopback timeout elapses, so it runs on its
// own goroutine and the command returns as soon as the flow is started. The
// URL reaches the client as a session/update notification once it is known;
// the loopback receiver stays up in the meantime, so an operator on the same
// machine can complete the flow in a local browser. Operator-side out-of-band
// completion (pasting a redirect URL back) is not reachable with the landed
// engine — see headlessMCPDisplay.
func (m *CommandManager) mcpAuth(ctx context.Context, sessionID string, rt *CommandRuntime, args []string) (commands.Result, error) {
	name := ""
	if len(args) > 0 {
		name = strings.TrimSpace(strings.Join(args[1:], " "))
	}
	if len(args) == 0 || !strings.EqualFold(strings.TrimSpace(args[0]), "auth") {
		// Run the canonical argument validator so the usage sentence matches
		// the TUI exactly (and keeps a name supplied after a bad subcommand
		// from silently passing).
		_, err := commands.ResolveOAuthServer(mcpServers(rt), "")
		return commands.Result{}, invalidParamsError("%s", err.Error())
	}

	srv, err := commands.ResolveOAuthServer(mcpServers(rt), name)
	if err != nil {
		// The sentence already names the fix (add the server, set auth =
		// "oauth", give it a url); pass it through verbatim.
		return commands.Result{}, invalidParamsError("%s", err.Error())
	}

	store, err := m.openCredentialStore()
	if err != nil {
		if errors.Is(err, credentials.ErrKeyringUnavailable) {
			return commands.Result{}, &jsonRPCError{Code: internalError, Message: fmt.Sprintf(
				"OAuth tokens are stored in the OS keychain, which is unavailable on this host. "+
					"Run Marshal on a machine with a working Keychain, Credential Manager, or Secret Service, then retry /mcp auth %s.", name)}
		}
		return commands.Result{}, &jsonRPCError{Code: internalError, Message: "Cannot open the credential store: " + err.Error()}
	}

	if sessionID == "" {
		return commands.Result{}, serverErrorf("session has no id; cannot address the authorization notice")
	}

	disp := &headlessMCPDisplay{notify: m.notify, sessionID: sessionID, name: name}
	eng := &oauth.Engine{
		ServerURL:  srv.URL,
		ClientName: "marshal",
		Store:      store,
		// Open is deliberately nil: a headless server must not spawn a
		// browser on the host it runs on. The authorization URL is emitted
		// as a notification instead, and the loopback receiver still serves
		// a same-machine browser that visits it.
		Logger: rt.State.Logger(),
	}

	// Derive the authorization context from the request context so a client
	// disconnect (or server shutdown) aborts the flow; the flow's own
	// five-minute loopback timeout bounds it otherwise.
	authCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		err := eng.Authorize(authCtx, disp)
		if err != nil {
			disp.finish(err)
			return
		}
		// Terminal success: the token is stored. Rebuild the runtime so the
		// server that returned ErrAuthRequired at startup (and never entered
		// the manager's client set) is started and its tools registered on
		// the live registry — the same reload the TUI's reconnectMCPServer
		// performs. A nil ReloadConfig (embedded/test runtimes) keeps the
		// token-stored-only sentence.
		if rt.ReloadConfig == nil {
			disp.finish(nil)
			return
		}
		// Clear the startup notices the rebuild resolves BEFORE reloading, as
		// the TUI's reconnectMCPServer does: the stale "needs OAuth" notice is
		// exactly what this flow fixes, but a rebuild that still fails sets a
		// fresh notice of its own, so clearing afterwards would wipe it and
		// leave the operator with a bare success message.
		// No nil-State guard: rt.State is already dereferenced above (its
		// Logger, and the server lookup in mcpServers), so a nil State would
		// have failed long before this goroutine was started.
		rt.State.ClearNotice(session.NoticeProvider)
		rt.State.ClearNotice(session.NoticeConfig)
		disp.finishConnected(rt.ReloadConfig(rt.State.Config))
	}()

	return commands.Text(fmt.Sprintf(
		"Authorizing MCP server %q. The authorization URL will arrive as a session update; open it in a browser to complete the flow. "+
			"Tokens are stored in the OS keychain.", name)), nil
}

// mcpServers returns the configured MCP servers for a runtime, tolerating a
// nil State (embedded/test runtimes) so resolution reports a clean
// "no server configured" sentence rather than panicking.
func mcpServers(rt *CommandRuntime) map[string]config.MCPServerConfig {
	if rt == nil || rt.State == nil {
		return nil
	}
	return rt.State.Config.MCP.Servers
}

// openCredentialStore opens the OS credential store, honouring the
// injectable seam when one is configured.
func (m *CommandManager) openCredentialStore() (credentials.Store, error) {
	if m.openStore != nil {
		return m.openStore()
	}
	return credentials.New("marshal")
}

// headlessMCPDisplay is the oauth.Display for the wire surface. ShowURL
// emits the authorization URL to the connected client as a session update;
// Wait blocks until the flow's context is done.
//
// The spec's out-of-band fallback — a user pastes the full redirect URL back
// so the flow completes on a machine other than the Marshal host — is NOT
// reachable with the landed engine API. oauth.Engine.Authorize binds its
// loopback receiver internally (StartLoopback) and reads the single result
// it produces; there is no exported seam to inject a pasted redirect URL or
// callback result, and the loopback redirect_uri is pinned to 127.0.0.1 on
// the Marshal host, which the user's browser must be able to reach. Making
// the paste path reachable would require an oauth-package change (out of
// scope for this task), e.g. an exported RedirectResult/Inject or an
// Authorize variant that accepts a caller-supplied redirect handler.
//
// Until then the only completion path over ACP is the same-machine loopback
// browser flow; the URL notice includes that instruction.
type headlessMCPDisplay struct {
	notify    NotifyFunc
	sessionID string
	name      string
}

var _ oauth.Display = (*headlessMCPDisplay)(nil)

// ShowURL records the authorization URL and emits it to the client.
func (d *headlessMCPDisplay) ShowURL(url string) {
	d.emit(fmt.Sprintf(
		"Authorize MCP server %q by opening this URL (a browser on the Marshal host can also complete the flow):\n%s",
		d.name, url))
}

// Wait blocks until ctx is cancelled, then returns ctx.Err(). The engine's
// Authorize waits on the loopback callback directly and treats Wait as a
// hint, so this is never on the critical path; it exists so the display
// satisfies oauth.Display and reports cancellation the same way the TUI
// display does.
func (d *headlessMCPDisplay) Wait(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// finish emits the terminal result of Authorize as a session update, so a
// client learns whether the flow succeeded without polling.
func (d *headlessMCPDisplay) finish(err error) {
	switch {
	case err == nil:
		d.emit(fmt.Sprintf("\u2713 Authorized MCP server %q. Tokens are stored in the OS keychain.", d.name))
	case errors.Is(err, context.Canceled):
		d.emit(fmt.Sprintf("Authorization of MCP server %q was cancelled; no credentials were saved.", d.name))
	case err != nil:
		d.emit(fmt.Sprintf("Authorization of MCP server %q failed: %v", d.name, err))
	}
}

// finishConnected reports the post-authorization reload outcome. The
// success sentence names the connection because the reload is what makes
// it true; a reload failure reports the TUI's could-not-be-connected shape
// (internal/app/tui/mcp.go reconnectMCPServer) — the token stays stored,
// so a later rebuild connects the server.
func (d *headlessMCPDisplay) finishConnected(reloadErr error) {
	if reloadErr == nil {
		d.emit(fmt.Sprintf("\u2713 Authorized MCP server %q. Tokens are stored in the OS keychain; the server is now connected.", d.name))
		return
	}
	d.emit(fmt.Sprintf("\u2717 Authorized MCP server %q, but it could not be connected: %v", d.name, reloadErr))
}

// emit sends text to the connected client as an agent message chunk, the
// same session/update shape every other agent-authored line uses, so any ACP
// client renders it in the transcript without a new method.
func (d *headlessMCPDisplay) emit(text string) {
	if d.notify == nil {
		return
	}
	_ = d.notify("session/update", SessionUpdateParams{
		SessionID: d.sessionID,
		Update: map[string]any{
			"kind": "agent_message_chunk",
			"content": map[string]any{
				"type": "text",
				"text": text,
			},
		},
	})
}
