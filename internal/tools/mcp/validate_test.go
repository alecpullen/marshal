package mcp

import (
	"context"
	"net"
	"strings"
	"testing"

	"marshal/internal/app/config"
)

func TestTransportDerivesFromFields(t *testing.T) {
	tests := []struct {
		name string
		srv  config.MCPServerConfig
		want string
	}{
		{"url only", config.MCPServerConfig{URL: "https://example.com/mcp"}, "http"},
		{"command only", config.MCPServerConfig{Command: "npx"}, "stdio"},
		{"explicit http", config.MCPServerConfig{Type: "http", URL: "https://example.com/mcp"}, "http"},
		{"explicit stdio", config.MCPServerConfig{Type: "stdio", Command: "npx"}, "stdio"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Transport(tc.srv)
			if err != nil {
				t.Fatalf("Transport: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Transport = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTransportRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name string
		srv  config.MCPServerConfig
	}{
		{"both set", config.MCPServerConfig{Command: "npx", URL: "https://example.com/mcp"}},
		{"neither set", config.MCPServerConfig{}},
		{"http type without url", config.MCPServerConfig{Type: "http", Command: "npx"}},
		{"http type with command", config.MCPServerConfig{Type: "http", URL: "https://example.com/mcp", Command: "npx"}},
		{"stdio type without command", config.MCPServerConfig{Type: "stdio", URL: "https://example.com/mcp"}},
		{"stdio type with url", config.MCPServerConfig{Type: "stdio", Command: "npx", URL: "https://example.com/mcp"}},
		{"unknown type", config.MCPServerConfig{Type: "sse", URL: "https://example.com/mcp"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Transport(tc.srv); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// withPublicResolver stubs DNS so the SSRF check never touches the network.
func withPublicResolver(t *testing.T) {
	t.Helper()
	orig := remoteResolver
	remoteResolver = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	t.Cleanup(func() { remoteResolver = orig })
}

func TestValidateRemoteServerRequiresHTTPS(t *testing.T) {
	withPublicResolver(t)
	srv := config.MCPServerConfig{URL: "http://example.com/mcp", Trust: "unrestricted"}
	err := ValidateRemoteServer(srv)
	if err == nil {
		t.Fatal("expected plain http to be rejected")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("error should mention https, got %v", err)
	}
}

func TestValidateRemoteServerAllowsInsecureHTTPWhenSeamSet(t *testing.T) {
	withPublicResolver(t)
	orig := allowInsecureHTTP
	allowInsecureHTTP = true
	t.Cleanup(func() { allowInsecureHTTP = orig })

	srv := config.MCPServerConfig{URL: "http://example.com/mcp", Trust: "unrestricted"}
	if err := ValidateRemoteServer(srv); err != nil {
		t.Fatalf("expected http to be accepted with the seam set, got %v", err)
	}
}

func TestValidateRemoteServerRejectsNonHTTPScheme(t *testing.T) {
	withPublicResolver(t)
	srv := config.MCPServerConfig{URL: "ftp://example.com/mcp", Trust: "unrestricted"}
	if err := ValidateRemoteServer(srv); err == nil {
		t.Fatal("expected a non-http scheme to be rejected")
	}
}

func TestValidateRemoteServerRequiresTrust(t *testing.T) {
	withPublicResolver(t)
	for _, trust := range []string{"", "false", "restricted"} {
		srv := config.MCPServerConfig{URL: "https://example.com/mcp", Trust: trust}
		err := ValidateRemoteServer(srv)
		if err == nil {
			t.Fatalf("trust %q: expected rejection", trust)
		}
		if !strings.Contains(err.Error(), "trust") {
			t.Fatalf("trust %q: error should mention trust, got %v", trust, err)
		}
	}
}

func TestValidateRemoteServerAcceptsTrustedPublicEndpoint(t *testing.T) {
	withPublicResolver(t)
	srv := config.MCPServerConfig{URL: "https://example.com/mcp", Trust: "unrestricted"}
	if err := ValidateRemoteServer(srv); err != nil {
		t.Fatalf("expected acceptance, got %v", err)
	}
}

func TestValidateRemoteServerBlocksPrivateHosts(t *testing.T) {
	withPublicResolver(t)
	hosts := []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "localhost", "LOCALHOST", "0.0.0.0", "[::1]"}
	for _, host := range hosts {
		srv := config.MCPServerConfig{URL: "https://" + host + "/mcp"}
		err := ValidateRemoteServer(srv)
		if err == nil {
			t.Fatalf("host %q: expected SSRF rejection", host)
		}
		if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("host %q: expected private/loopback error, got %v", host, err)
		}
	}
}

func TestValidateRemoteServerBlocksHostnameResolvingToPrivateIP(t *testing.T) {
	orig := remoteResolver
	remoteResolver = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.168.1.10")}, nil
	}
	t.Cleanup(func() { remoteResolver = orig })

	srv := config.MCPServerConfig{URL: "https://internal.example.com/mcp"}
	err := ValidateRemoteServer(srv)
	if err == nil {
		t.Fatal("expected a hostname resolving to a private IP to be rejected")
	}
	if !strings.Contains(err.Error(), "private") {
		t.Fatalf("expected private-address error, got %v", err)
	}
}

func TestValidateRemoteServerTrustedAllowsPrivateHost(t *testing.T) {
	srv := config.MCPServerConfig{URL: "https://127.0.0.1:8443/mcp", Trust: "unrestricted"}
	if err := ValidateRemoteServer(srv); err != nil {
		t.Fatalf("trusted private endpoint should be allowed, got %v", err)
	}
}

func TestValidateRemoteServerHeaderSyntax(t *testing.T) {
	withPublicResolver(t)
	base := config.MCPServerConfig{URL: "https://example.com/mcp", Trust: "unrestricted"}

	ok := base
	ok.Headers = map[string]string{"Authorization": "Bearer x", "X-Api-Key": "y"}
	if err := ValidateRemoteServer(ok); err != nil {
		t.Fatalf("valid headers rejected: %v", err)
	}

	for _, key := range []string{"Bad Key", "Bad:Key", ""} {
		bad := base
		bad.Headers = map[string]string{key: "v"}
		if err := ValidateRemoteServer(bad); err == nil {
			t.Fatalf("header key %q: expected rejection", key)
		}
	}

	injected := base
	injected.Headers = map[string]string{"Authorization": "Bearer x\r\nX-Evil: 1"}
	if err := ValidateRemoteServer(injected); err == nil {
		t.Fatal("expected CRLF header value to be rejected")
	}
}

func TestResolveHeadersExpandsEnv(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "s3cret")

	got, err := ResolveHeaders(map[string]string{
		"Authorization": "Bearer $MCP_TEST_TOKEN",
		"X-Braced":      "${MCP_TEST_TOKEN}",
		"X-Literal":     "plain",
	})
	if err != nil {
		t.Fatalf("ResolveHeaders: %v", err)
	}
	if got["Authorization"] != "Bearer s3cret" {
		t.Errorf("Authorization = %q", got["Authorization"])
	}
	if got["X-Braced"] != "s3cret" {
		t.Errorf("X-Braced = %q", got["X-Braced"])
	}
	if got["X-Literal"] != "plain" {
		t.Errorf("X-Literal = %q", got["X-Literal"])
	}
}

func TestResolveHeadersErrorsOnUnsetVariable(t *testing.T) {
	_, err := ResolveHeaders(map[string]string{"Authorization": "Bearer $MCP_TEST_DEFINITELY_UNSET"})
	if err == nil {
		t.Fatal("expected an error for an unset variable")
	}
	if !strings.Contains(err.Error(), "MCP_TEST_DEFINITELY_UNSET") {
		t.Fatalf("error should name the variable, got %v", err)
	}
}

func TestResolveHeadersDoesNotMutateInput(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "s3cret")
	in := map[string]string{"Authorization": "Bearer $MCP_TEST_TOKEN"}
	if _, err := ResolveHeaders(in); err != nil {
		t.Fatalf("ResolveHeaders: %v", err)
	}
	if in["Authorization"] != "Bearer $MCP_TEST_TOKEN" {
		t.Fatalf("input map was mutated: %q", in["Authorization"])
	}
}

func TestResolveHeadersEmpty(t *testing.T) {
	got, err := ResolveHeaders(nil)
	if err != nil {
		t.Fatalf("ResolveHeaders(nil): %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
