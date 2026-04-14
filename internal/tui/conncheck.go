// internal/tui/conncheck.go
// Background connectivity checks for executor and critic endpoints.
//
// Semantics per provider:
//   - ollama: ping GET /api/tags; require 2xx → green, error/4xx/5xx → red
//   - cloud (fireworks, openai, together, …): can't verify without auth;
//     if URL is non-empty → amber (configured, unverified); empty → red
//
// "Green" means the server is reachable AND responding correctly.
// "Amber" means the endpoint is configured but not verifiable without credentials.
// "Red"   means the server is unreachable or returned an error response.

package tui

import (
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Types ─────────────────────────────────────────────────────────────────────

type connStatus int

const (
	connUnknown connStatus = iota // amber — configured but unverifiable without auth
	connOK                        // green — server reachable and responding correctly
	connFail                      // red   — unreachable or bad response
)

// endpointCfg bundles an endpoint's URL and provider type for the health check.
type endpointCfg struct {
	url      string
	provider string // "ollama", "fireworks", "openai", "together", …
}

type connCheckMsg struct {
	exec    connStatus
	critic  connStatus
	marshal connStatus
}

// ── Command ───────────────────────────────────────────────────────────────────

// doConnCheck fires all endpoint pings concurrently and returns a connCheckMsg.
func doConnCheck(exec, critic, marshal endpointCfg) tea.Cmd {
	return func() tea.Msg {
		type taggedResult struct {
			role   string
			status connStatus
		}
		ch := make(chan taggedResult, 3)
		ping := func(cfg endpointCfg, role string) {
			ch <- taggedResult{role, pingEndpoint(cfg)}
		}
		go ping(exec, "exec")
		go ping(critic, "critic")
		go ping(marshal, "marshal")

		msg := connCheckMsg{}
		for i := 0; i < 3; i++ {
			r := <-ch
			switch r.role {
			case "exec":
				msg.exec = r.status
			case "critic":
				msg.critic = r.status
			case "marshal":
				msg.marshal = r.status
			}
		}
		return msg
	}
}

// pingEndpoint returns the connection status for a single endpoint.
// For Ollama: actually pings /api/tags and requires a 2xx response.
// For cloud APIs: returns connUnknown (amber) — can't verify without auth.
func pingEndpoint(cfg endpointCfg) connStatus {
	if cfg.url == "" {
		return connFail
	}

	if strings.EqualFold(cfg.provider, "ollama") {
		return pingURL(strings.TrimRight(cfg.url, "/") + "/api/tags")
	}

	// Cloud provider — assume configured if a URL is present; can't probe without a key.
	return connUnknown
}

// pingURL performs an HTTP GET and requires a 2xx response for connOK.
func pingURL(url string) connStatus {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return connFail
	}
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return connOK
	}
	return connFail
}

// ── Render helper ─────────────────────────────────────────────────────────────

// connDot returns a coloured dot for a status: ● green, ● red, ◌ amber (unknown/configured).
func connDot(s connStatus) string {
	switch s {
	case connOK:
		return lipgloss.NewStyle().Foreground(colGr).Render("●")
	case connFail:
		return lipgloss.NewStyle().Foreground(colRd).Render("●")
	default: // connUnknown
		return lipgloss.NewStyle().Foreground(colAm).Render("◌")
	}
}
