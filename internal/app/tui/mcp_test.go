package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/mcpauth"
)

// lastMessage returns the text of the most recent system message.
func lastMessage(t *testing.T, m Model) string {
	t.Helper()
	msgs := m.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages recorded")
	}
	return msgs[len(msgs)-1].Content
}

// TestMCPAuthDoneReconnectsRuntime pins the fix for the post-auth reconnect
// gap: after Authorize stores a token, the runtime must be rebuilt so the
// server that failed to start with ErrAuthRequired is connected and its tools
// are registered. Without the reload the command reports success while the
// server stays unreachable until a restart.
func TestMCPAuthDoneReconnectsRuntime(t *testing.T) {
	m := newTestModel(t)
	m.mcpAuthServer = "linear"

	var reloaded []config.Config
	m.configReloader = func(cfg config.Config) error {
		reloaded = append(reloaded, cfg)
		return nil
	}

	updated, _ := m.Update(mcpauth.AuthDoneMsg{})
	m = asModel(t, updated)

	if len(reloaded) != 1 {
		t.Fatalf("successful auth should trigger exactly one runtime reload, got %d", len(reloaded))
	}
	msg := lastMessage(t, m)
	if !strings.Contains(msg, "Authorized MCP server") {
		t.Fatalf("expected success message, got %q", msg)
	}
	if m.mcpAuthServer != "" {
		t.Fatalf("mcpAuthServer should be cleared, got %q", m.mcpAuthServer)
	}
}

// TestMCPAuthDoneReloadFailureIsReported: a failed rebuild must not be
// reported as a clean success. The user needs to know the server is still
// unreachable even though the token was stored.
func TestMCPAuthDoneReloadFailureIsReported(t *testing.T) {
	m := newTestModel(t)
	m.mcpAuthServer = "linear"
	m.configReloader = func(config.Config) error { return errors.New("provider build failed") }

	updated, _ := m.Update(mcpauth.AuthDoneMsg{})
	m = asModel(t, updated)

	msg := lastMessage(t, m)
	if !strings.HasPrefix(msg, "\u2717") {
		t.Fatalf("reload failure should be reported, got %q", msg)
	}
	if !strings.Contains(msg, "linear") || !strings.Contains(msg, "provider build failed") {
		t.Fatalf("failure message should name the server and the cause, got %q", msg)
	}
}

// TestMCPAuthDoneNoReconnectOnNonSuccess: a cancelled or failed flow persists
// no token, so rebuilding the runtime would be pointless churn.
func TestMCPAuthDoneNoReconnectOnNonSuccess(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"cancelled", context.Canceled},
		{"failed", errors.New("exchange failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t)
			m.mcpAuthServer = "linear"
			reloads := 0
			m.configReloader = func(config.Config) error {
				reloads++
				return nil
			}

			updated, _ := m.Update(mcpauth.AuthDoneMsg{Err: tt.err})
			m = asModel(t, updated)

			if reloads != 0 {
				t.Fatalf("no reload expected when auth did not succeed, got %d", reloads)
			}
		})
	}
}

// TestReconnectKeepsAFreshMCPFailureNotice pins the ordering inside
// reconnectMCPServer. The notices must be cleared BEFORE the rebuild, because
// a rebuild whose MCP server still fails to start sets its own notice; clearing
// afterwards would wipe that notice and tell the user the reconnect worked when
// the server is still unreachable.
func TestReconnectKeepsAFreshMCPFailureNotice(t *testing.T) {
	m := newTestModel(t)
	// The stale notice the flow is supposed to resolve.
	m.state.SetNotice(session.Notice{
		Category: session.NoticeConfig,
		Severity: session.SeverityWarn,
		Message:  "MCP server 'linear' needs OAuth — run /mcp auth linear",
		Source:   "mcp",
	})
	// Stand in for the real rebuild: it starts the server, it fails again for
	// a different reason, and buildAgentRunnerWithLock sets a fresh notice.
	m.configReloader = func(config.Config) error {
		m.state.SetNotice(session.Notice{
			Category: session.NoticeConfig,
			Severity: session.SeverityWarn,
			Message:  "MCP server 'linear' is unavailable: 403 forbidden",
			Source:   "mcp",
		})
		return nil
	}

	m.reconnectMCPServer("linear")

	notice, ok := m.state.Notice()
	if !ok {
		t.Fatal("the reconnect must not clear the fresh failure notice")
	}
	if !strings.Contains(notice.Message, "unavailable") {
		t.Fatalf("wanted the post-reload notice, got %q", notice.Message)
	}
	if strings.Contains(notice.Message, "needs OAuth") {
		t.Fatalf("the stale needs-OAuth notice should have been cleared first, got %q", notice.Message)
	}
}

// TestReconnectMCPServerWithoutReloaderIsSafe: a model with no reload path
// (tests, embedded use) must not panic or claim a reconnect happened.
func TestReconnectMCPServerWithoutReloaderIsSafe(t *testing.T) {
	m := newTestModel(t)
	m.configReloader = nil
	before := len(m.state.Messages())

	m.reconnectMCPServer("linear")

	if got := len(m.state.Messages()); got != before {
		t.Fatalf("reconnect without a reloader should add no messages, got %d new", got-before)
	}
}
