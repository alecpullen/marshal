package mcp

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"marshal/internal/app/config"
)

// allowInsecureHTTP permits plain-http remote endpoints. Production default
// is false (https-only, per B4); tests point it at an httptest.Server, which
// serves plain HTTP.
var allowInsecureHTTP = false

// remoteResolver is the DNS resolver used by the SSRF check. Nil means
// net.DefaultResolver.
var remoteResolver func(ctx context.Context, host string) ([]net.IP, error)

// remoteResolveTimeout bounds the DNS lookup performed by the SSRF check.
var remoteResolveTimeout = 5 * time.Second

// Transport returns the effective transport for a server config: "http" or
// "stdio". It enforces the command/url exclusivity invariant and Type
// consistency.
func Transport(srv config.MCPServerConfig) (string, error) {
	switch srv.Type {
	case "http":
		if srv.URL == "" {
			return "", fmt.Errorf("mcp server: type = \"http\" requires url to be set")
		}
		if srv.Command != "" {
			return "", fmt.Errorf("mcp server: type = \"http\" cannot be combined with command")
		}
		return "http", nil
	case "stdio":
		if srv.Command == "" {
			return "", fmt.Errorf("mcp server: type = \"stdio\" requires command to be set")
		}
		if srv.URL != "" {
			return "", fmt.Errorf("mcp server: type = \"stdio\" cannot be combined with url")
		}
		return "stdio", nil
	case "":
		switch {
		case srv.URL != "" && srv.Command != "":
			return "", fmt.Errorf("mcp server: command and url are mutually exclusive; set exactly one")
		case srv.URL != "":
			return "http", nil
		case srv.Command != "":
			return "stdio", nil
		default:
			return "", fmt.Errorf("mcp server: neither command nor url is set")
		}
	default:
		return "", fmt.Errorf("mcp server: unknown type %q (accepted: \"\", \"stdio\", \"http\")", srv.Type)
	}
}

// ValidateRemoteServer validates a remote (Streamable HTTP) MCP server
// config: https-only, trust required, SSRF posture, header syntax.
//
// Ordering is deliberate. For an untrusted server the SSRF check runs first
// so a private/LAN endpoint reports the specific problem ("this points at a
// private address") rather than the generic trust prompt; a public endpoint
// then falls through to the trust requirement. A trusted server skips the
// SSRF check entirely — trust is the explicit acknowledgment that unlocks a
// private endpoint, matching web.fetch's "blocked unless explicitly allowed"
// rule.
func ValidateRemoteServer(srv config.MCPServerConfig) error {
	u, err := url.Parse(srv.URL)
	if err != nil {
		return fmt.Errorf("mcp server url %q is not a valid URL: %w", srv.URL, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecureHTTP {
			return fmt.Errorf("mcp server url %q must use https; plain http is rejected", srv.URL)
		}
	default:
		return fmt.Errorf("mcp server url %q must use https (got scheme %q)", srv.URL, u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("mcp server url %q has no host", srv.URL)
	}

	if srv.Trust != "unrestricted" {
		if err := checkRemoteHost(u.Hostname()); err != nil {
			return err
		}
		return fmt.Errorf("remote MCP server requires trust = \"unrestricted\" to acknowledge that a remote server can feed the agent tool descriptions")
	}

	return validateHeaders(srv.Headers)
}

// checkRemoteHost applies the same SSRF posture as web.fetch: loopback,
// unspecified, link-local, and private addresses are refused.
func checkRemoteHost(host string) error {
	if host == "" {
		return fmt.Errorf("mcp server url has no host")
	}
	if strings.EqualFold(host, "localhost") {
		return privateHostError(host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return privateHostError(host)
		}
		return nil
	}
	ips, err := lookupHost(host)
	if err != nil {
		return fmt.Errorf("mcp server url host %q could not be resolved: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("mcp server url host %q could not be resolved", host)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return privateHostError(host)
		}
	}
	return nil
}

func privateHostError(host string) error {
	return fmt.Errorf("mcp server url host %q is a private, loopback, or link-local address; set trust = \"unrestricted\" to allow it", host)
}

func lookupHost(host string) ([]net.IP, error) {
	if remoteResolver != nil {
		return remoteResolver(context.Background(), host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteResolveTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate()
}

// validateHeaders checks header names against the RFC 7230 token grammar and
// rejects values carrying CR/LF/NUL, which would allow header injection.
func validateHeaders(headers map[string]string) error {
	for k, v := range headers {
		if k == "" {
			return fmt.Errorf("mcp server header name must not be empty")
		}
		if !isHeaderToken(k) {
			return fmt.Errorf("mcp server header name %q contains characters that are not valid in an HTTP header name", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("mcp server header %q contains a forbidden control character", k)
		}
	}
	return nil
}

// isHeaderToken reports whether s is a non-empty RFC 7230 token.
func isHeaderToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// ResolveHeaders resolves $VAR and ${VAR} references in header values from
// the process environment. A reference to an unset variable is an error.
func ResolveHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		expanded, err := expandEnv(v)
		if err != nil {
			return nil, fmt.Errorf("mcp server header %q: %w", k, err)
		}
		out[k] = expanded
	}
	return out, nil
}

// expandEnv expands $VAR and ${VAR} references from the process environment.
// A reference to an unset or empty variable is an error, so a missing secret
// fails loudly instead of sending an empty Authorization header. A '$' that
// does not begin a valid reference is left literal.
func expandEnv(s string) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		name, next, ok := envRef(s, i)
		if !ok {
			b.WriteByte('$')
			i++
			continue
		}
		val := os.Getenv(name)
		if val == "" {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		b.WriteString(val)
		i = next
	}
	return b.String(), nil
}

// envRef parses a $VAR or ${VAR} reference starting at s[i], which must be
// '$'. It returns the variable name and the index just past the reference.
func envRef(s string, i int) (name string, next int, ok bool) {
	j := i + 1
	if j < len(s) && s[j] == '{' {
		end := strings.IndexByte(s[j:], '}')
		if end < 0 {
			return "", 0, false
		}
		name = s[j+1 : j+end]
		if !isEnvName(name) {
			return "", 0, false
		}
		return name, j + end + 1, true
	}
	k := j
	for k < len(s) && isEnvNameByte(s[k], k == j) {
		k++
	}
	if k == j {
		return "", 0, false
	}
	return s[j:k], k, true
}

func isEnvNameByte(c byte, first bool) bool {
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
		return true
	}
	return !first && c >= '0' && c <= '9'
}

func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isEnvNameByte(s[i], i == 0) {
			return false
		}
	}
	return true
}
