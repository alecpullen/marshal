package commands

import (
	"fmt"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/tools/mcp"
)

// ResolveOAuthServer looks up a configured MCP server by name and validates
// that it is a remote server opted into the OAuth authorization-code flow.
//
// It is the headless, testable core of the /mcp auth command: the TUI
// dispatch resolves the server here, then drives oauth.Engine.Authorize
// against it. Every failure is a user-facing sentence naming the fix rather
// than a bare error, because the command's whole job is to tell the user what
// to change when a name is wrong or a server is not OAuth-enabled.
func ResolveOAuthServer(servers map[string]config.MCPServerConfig, name string) (config.MCPServerConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.MCPServerConfig{}, fmt.Errorf("Usage: /mcp auth <name> \u2014 name a remote MCP server configured with auth = \"oauth\".")
	}
	srv, ok := servers[name]
	if !ok {
		return config.MCPServerConfig{}, fmt.Errorf("No MCP server named %q is configured. Add one under [mcp.servers.%s], then run /mcp auth %s.", name, name, name)
	}
	if srv.Auth != "oauth" {
		return config.MCPServerConfig{}, fmt.Errorf("MCP server %q is not configured for OAuth (auth = %q). Set auth = \"oauth\" on its [mcp.servers.%s] entry, then run /mcp auth %s again.", name, srv.Auth, name, name)
	}
	if srv.URL == "" {
		return config.MCPServerConfig{}, fmt.Errorf("MCP server %q sets auth = \"oauth\" but has no url; OAuth requires the remote (http) transport.", name)
	}
	// Apply the same remote-endpoint gate the startup path enforces
	// (manager.go calls mcp.ValidateRemoteServer before dialing). Without it a
	// hand-written [mcp.servers.x] entry would be rejected at startup but still
	// drive the full authorization-code flow here, sending the code, PKCE
	// verifier, and refresh token over a transport the user never acknowledged.
	if err := mcp.ValidateRemoteServer(srv); err != nil {
		return config.MCPServerConfig{}, fmt.Errorf("MCP server %q is not a usable OAuth endpoint: %v. Fix its [mcp.servers.%s] entry \u2014 url must be https, and a private or loopback host additionally needs trust = \"unrestricted\" \u2014 then run /mcp auth %s again.", name, err, name, name)
	}
	return srv, nil
}
