package commands

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

func TestResolveOAuthServerAcceptsConfiguredServer(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"github": {URL: "https://mcp.example.com/mcp", Type: "http", Auth: "oauth", Trust: "unrestricted"},
	}
	srv, err := ResolveOAuthServer(servers, "github")
	if err != nil {
		t.Fatalf("ResolveOAuthServer() error = %v", err)
	}
	if srv.URL != "https://mcp.example.com/mcp" {
		t.Errorf("URL = %q, want the configured url", srv.URL)
	}
}

func TestResolveOAuthServerRejectsUnknownName(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"github": {URL: "https://mcp.example.com/mcp", Auth: "oauth"},
	}
	_, err := ResolveOAuthServer(servers, "gitlab")
	if err == nil {
		t.Fatal("ResolveOAuthServer() = nil error, want unknown-name error")
	}
	if !strings.Contains(err.Error(), "gitlab") {
		t.Errorf("error %q should name the missing server", err)
	}
}

func TestResolveOAuthServerRejectsEmptyName(t *testing.T) {
	_, err := ResolveOAuthServer(map[string]config.MCPServerConfig{}, "  ")
	if err == nil {
		t.Fatal("ResolveOAuthServer() = nil error, want usage error")
	}
	if !strings.Contains(err.Error(), "Usage:") {
		t.Errorf("error %q should print usage", err)
	}
}

func TestResolveOAuthServerRejectsNonOAuthServer(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"local": {URL: "https://mcp.example.com/mcp", Type: "http"},
	}
	_, err := ResolveOAuthServer(servers, "local")
	if err == nil {
		t.Fatal("ResolveOAuthServer() = nil error, want non-oauth rejection")
	}
	if !strings.Contains(err.Error(), "oauth") {
		t.Errorf("error %q should tell the user to set auth = \"oauth\"", err)
	}
}

func TestResolveOAuthServerRejectsOAuthWithoutURL(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"stdio": {Command: "mcp-server", Auth: "oauth"},
	}
	_, err := ResolveOAuthServer(servers, "stdio")
	if err == nil {
		t.Fatal("ResolveOAuthServer() = nil error, want url-required error")
	}
}

// The auth path must apply the same remote-endpoint gate as the startup path:
// ResolveOAuthServer previously accepted any non-empty URL, so a hand-written
// [mcp.servers.x] entry with a plain-http url was rejected at startup but still
// drove the whole authorization-code flow over cleartext.
func TestResolveOAuthServerRejectsPlainHTTPRemote(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"lan": {URL: "http://10.1.2.3/mcp", Type: "http", Auth: "oauth"},
	}
	_, err := ResolveOAuthServer(servers, "lan")
	if err == nil {
		t.Fatal("ResolveOAuthServer() = nil error, want plain-http rejection")
	}
	if !strings.Contains(err.Error(), "lan") {
		t.Errorf("error %q should name the server", err)
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error %q should state the https requirement", err)
	}
}

// A private/loopback host over https still needs an explicit trust =
// "unrestricted" acknowledgment before OAuth may point at it.
func TestResolveOAuthServerRejectsPrivateHostWithoutTrust(t *testing.T) {
	servers := map[string]config.MCPServerConfig{
		"lan": {URL: "https://10.1.2.3/mcp", Type: "http", Auth: "oauth"},
	}
	_, err := ResolveOAuthServer(servers, "lan")
	if err == nil {
		t.Fatal("ResolveOAuthServer() = nil error, want private-host rejection")
	}
	if !strings.Contains(err.Error(), "trust") {
		t.Errorf("error %q should name the trust requirement", err)
	}
}

func TestMCPCommandRegistered(t *testing.T) {
	reg := New()
	if err := RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	cmd, ok := reg.Lookup("mcp")
	if !ok {
		t.Fatal("/mcp not registered")
	}
	if !cmd.TUIOnly {
		t.Error("/mcp should be TUIOnly")
	}
	if cmd.Args != "auth <name>" {
		t.Errorf("/mcp Args = %q, want \"auth <name>\"", cmd.Args)
	}
}
