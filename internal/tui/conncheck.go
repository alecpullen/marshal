// internal/tui/conncheck.go
// Background connectivity checks for executor and critic endpoints.
// Pings {baseURL}/models — any non-5xx response (including 401/403) means the
// server is reachable. Connection errors and 5xx count as failures.

package tui

import (
	"net/http"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Types ─────────────────────────────────────────────────────────────────────

type connStatus int

const (
	connUnknown connStatus = iota
	connOK
	connFail
)

type connCheckMsg struct {
	exec   connStatus
	critic connStatus
}

// ── Command ───────────────────────────────────────────────────────────────────

// doConnCheck fires both endpoint pings concurrently and returns a connCheckMsg.
func doConnCheck(execURL, criticURL string) tea.Cmd {
	return func() tea.Msg {
		type taggedResult struct {
			isExec bool
			status connStatus
		}
		ch := make(chan taggedResult, 2)
		ping := func(url string, isExec bool) {
			ch <- taggedResult{isExec, pingEndpoint(url)}
		}
		go ping(execURL, true)
		go ping(criticURL, false)

		msg := connCheckMsg{}
		for i := 0; i < 2; i++ {
			r := <-ch
			if r.isExec {
				msg.exec = r.status
			} else {
				msg.critic = r.status
			}
		}
		return msg
	}
}

func pingEndpoint(baseURL string) connStatus {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(baseURL + "/models")
	if err != nil {
		return connFail
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return connFail
	}
	return connOK
}

// ── Render helper ─────────────────────────────────────────────────────────────

// connDot returns a coloured dot for a status: ● green, ● red, ◌ amber (unknown).
func connDot(s connStatus) string {
	switch s {
	case connOK:
		return lipgloss.NewStyle().Foreground(colGr).Render("●")
	case connFail:
		return lipgloss.NewStyle().Foreground(colRd).Render("●")
	default:
		return lipgloss.NewStyle().Foreground(colAm).Render("◌")
	}
}
