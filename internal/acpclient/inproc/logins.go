package inproc

import (
	"context"
	"errors"
	"log/slog"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/commands"
	"marshal/internal/credentials"
	"marshal/internal/oauth"
)

// urlDisplay is the oauth.Display a sign-in reports through. ShowURL hands the
// authorization URL to the client's callback, which opens the browser; Wait has
// nothing of its own to wait for, because the engine waits on its loopback
// callback.
type urlDisplay struct {
	show func(url string)
}

// ShowURL reports the authorization URL to the client.
func (d *urlDisplay) ShowURL(url string) {
	if d.show != nil {
		d.show(url)
	}
}

// Wait reports success at once: the engine's own loopback wait is the
// critical path, and nothing calls Wait.
func (d *urlDisplay) Wait(ctx context.Context) error { return nil }

// PrepareProviderLogin checks that the browser sign-in for the provider called
// name can start, and returns why not.
func (h *Host) PrepareProviderLogin(ctx context.Context, name string) error {
	if _, err := h.AuthResolver().CodexOAuthEngine(name); err != nil {
		return loginError(err)
	}
	return nil
}

// LoginProvider runs the browser sign-in for name and stores its tokens. The
// engine's Open is never set: show is how the client hears of the URL. Its
// errors are coded the way PrepareProviderLogin's are, so a client sees the
// same keychain sentence whichever call surfaces the failure.
func (h *Host) LoginProvider(ctx context.Context, name string, show func(url string)) error {
	engine, err := h.AuthResolver().CodexOAuthEngine(name)
	if err != nil {
		return loginError(err)
	}
	return loginError(engine.Authorize(ctx, &urlDisplay{show: show}))
}

// PrepareMCPLogin checks that the OAuth sign-in for server can start: the
// server resolves and the token store opens. Its error is the sentence the
// client shows.
func (s *Session) PrepareMCPLogin(ctx context.Context, server string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	_, _, err := s.mcpLoginTarget(server)
	return err
}

// LoginMCP runs the OAuth sign-in for server and stores its tokens.
func (s *Session) LoginMCP(ctx context.Context, server string, show func(url string)) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	srv, store, err := s.mcpLoginTarget(server)
	if err != nil {
		return err
	}
	return mcpEngine(srv, store, s.state.Logger()).Authorize(ctx, &urlDisplay{show: show})
}

// mcpLoginTarget resolves server and opens its token store, returning the
// error the client shows when either fails.
func (s *Session) mcpLoginTarget(server string) (config.MCPServerConfig, credentials.Store, error) {
	srv, err := commands.ResolveOAuthServer(s.state.ConfigSnapshot().MCP.Servers, server)
	if err != nil {
		return config.MCPServerConfig{}, nil, err
	}
	store, err := s.auth.OpenStore("marshal")
	if err != nil {
		return config.MCPServerConfig{}, nil, mcpLoginError(server, err)
	}
	return srv, store, nil
}

// loginError codes an unavailable keychain so the client can act on it; every
// other failure keeps its own text.
func loginError(err error) error {
	if errors.Is(err, credentials.ErrKeyringUnavailable) {
		return &acpwire.Error{
			Code:    acpwire.CodeKeyringUnavailable,
			Message: err.Error(),
		}
	}
	return err
}

// mcpLoginError turns a store failure into the sentence the client shows.
func mcpLoginError(name string, err error) error {
	if errors.Is(err, credentials.ErrKeyringUnavailable) {
		return &acpwire.Error{
			Code: acpwire.CodeKeyringUnavailable,
			Message: "OAuth tokens are stored in the OS keychain, which is unavailable here. " +
				"Run Marshal on a machine with a working Keychain, Credential Manager, or Secret Service, then retry /mcp auth " + name + ".",
		}
	}
	return &storeError{name: name, err: err}
}

// storeError is "Cannot open the credential store: <cause>": the sentence the
// TUI showed for a store failure that is not a missing keychain.
type storeError struct {
	name string
	err  error
}

func (e *storeError) Error() string { return "Cannot open the credential store: " + e.err.Error() }

// Unwrap exposes the store's own failure.
func (e *storeError) Unwrap() error { return e.err }

// mcpEngine builds the authorization engine for one MCP server: the engine the
// TUI built. Open stays nil, so the browser is the client's job through show.
func mcpEngine(srv config.MCPServerConfig, store credentials.Store, logger *slog.Logger) *oauth.Engine {
	return &oauth.Engine{
		ServerURL:  srv.URL,
		ClientName: "marshal",
		Store:      store,
		Logger:     logger,
	}
}
