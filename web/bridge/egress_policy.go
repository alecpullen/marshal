package bridge

import "strings"

// Egress modes. A workspace's [network] mode picks one.
const (
	EgressModeOff       = "off"
	EgressModeOpen      = "open"
	EgressModeAllowlist = "allowlist"
)

// EgressPolicy is the proxy's whole configuration: one entry per agent,
// keyed by agent ID. The bridge replaces it atomically on every change.
type EgressPolicy struct {
	Agents map[string]EgressAgentPolicy `json:"agents"`
}

// EgressAgentPolicy is what one agent may reach and how it authenticates
// to the proxy.
type EgressAgentPolicy struct {
	// Token is the agent's proxy password. It is compared in constant
	// time and never logged.
	Token string `json:"token"`
	// IP, when set, must equal the connection's source address.
	IP        string `json:"ip,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Mode      string `json:"mode"`
	// Allow is the workspace's egress list; Grants are per-agent
	// additions made at decision time. Both accept "host" and "*.suffix".
	Allow  []string `json:"allow,omitempty"`
	Grants []string `json:"grants,omitempty"`
	// Inject maps a host to the header the proxy adds to requests for it.
	// Presence of an entry makes the proxy terminate TLS for that host.
	Inject map[string]EgressInjection `json:"inject,omitempty"`
}

// EgressInjection is one injected header; Value is already formatted.
type EgressInjection struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

// EgressRecord is one connection (or plain request) the proxy handled.
type EgressRecord struct {
	At         int64  `json:"at"` // Unix ms
	AgentID    string `json:"agentId"`
	Workspace  string `json:"workspace,omitempty"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Decision   string `json:"decision"` // "allow" or "block"
	Injected   bool   `json:"injected,omitempty"`
	BytesUp    int64  `json:"bytesUp"`
	BytesDown  int64  `json:"bytesDown"`
	DurationMs int64  `json:"durationMs"`
}

// normalizeHost lowercases and strips a trailing dot, so "Example.COM."
// and "example.com" are the same host everywhere.
func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// hostMatches reports whether pattern covers host. A pattern is an exact
// host or "*.suffix", which matches any subdomain of suffix (not suffix
// itself).
func hostMatches(pattern, host string) bool {
	pattern, host = normalizeHost(pattern), normalizeHost(host)
	if pattern == "" || host == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(pattern, "*."); ok {
		return rest != "" && strings.HasSuffix(host, "."+rest)
	}
	return pattern == host
}

// allowed applies the agent's mode to host. An unknown mode refuses:
// the proxy fails closed.
func allowed(p EgressAgentPolicy, host string) bool {
	switch p.Mode {
	case EgressModeOpen:
		return true
	case EgressModeAllowlist:
		for _, list := range [][]string{p.Allow, p.Grants} {
			for _, pat := range list {
				if hostMatches(pat, host) {
					return true
				}
			}
		}
	}
	return false
}

// injectionFor returns the injection configured for host, if any.
func (p EgressAgentPolicy) injectionFor(host string) (EgressInjection, bool) {
	host = normalizeHost(host)
	for h, inj := range p.Inject {
		if normalizeHost(h) == host && inj.Header != "" {
			return inj, true
		}
	}
	return EgressInjection{}, false
}
