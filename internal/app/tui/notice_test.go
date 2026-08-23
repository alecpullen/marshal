package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
)

func TestNoticeForErrorClassifiesProviderHTTPError(t *testing.T) {
	err := &provider.ProviderError{Provider: "openai", StatusCode: 500, Body: "boom"}
	n := noticeForError(err, "turn")
	if n.Category != session.NoticeProvider {
		t.Fatalf("category = %v, want NoticeProvider", n.Category)
	}
	if n.Hint == "" {
		t.Fatal("provider notice must carry a hint")
	}
}

func TestNoticeForErrorClassifiesWrappedTransportError(t *testing.T) {
	err := fmt.Errorf("outer: %w", &provider.RequestError{Provider: "ollama", Op: "chat request failed", Err: errors.New("connection refused")})
	n := noticeForError(err, "turn")
	if n.Category != session.NoticeProvider {
		t.Fatalf("category = %v, want NoticeProvider for wrapped RequestError", n.Category)
	}
}

func TestNoticeForErrorDefaultsToInternal(t *testing.T) {
	n := noticeForError(errors.New("something weird"), "turn")
	if n.Category != session.NoticeInternal {
		t.Fatalf("category = %v, want NoticeInternal", n.Category)
	}
}

func TestRenderNoticeShowsCategoryMessageHint(t *testing.T) {
	n := session.Notice{
		Category: session.NoticeProvider,
		Severity: session.SeverityError,
		Message:  "connection refused",
		Hint:     "Run /connect to review the provider.",
	}
	out := renderNotice(n, 100)
	for _, want := range []string{"provider", "connection refused", "Run /connect", "esc"} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderNotice missing %q:\n%s", want, out)
		}
	}
}

func TestRenderNoticeClampsMessageToTwoLines(t *testing.T) {
	n := session.Notice{
		Category: session.NoticeInternal,
		Severity: session.SeverityError,
		Message:  strings.Repeat("very long error message ", 40),
	}
	out := renderNotice(n, 60)
	if got := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1; got > 3 {
		t.Fatalf("renderNotice produced %d lines, want <= 3 (2 message + 1 hint):\n%s", got, out)
	}
}

// Regression: a notice injected with a store-stamped timestamp (the
// runtime layer's path) must auto-dismiss on the tick — the old code only
// dismissed TUI-stamped errors, pinning runtime errors forever.
func TestTickDismissesRuntimeInjectedNoticeAfterTTL(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Severity: session.SeverityError, Message: "down", SetAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(100+int64(noticeBannerDuration/time.Second)+1, 0) }

	updated, _ := m.handleAgentTick(agentTickMsg{})
	m = updated
	if _, ok := m.state.Notice(); ok {
		t.Fatal("notice should be dismissed after the TTL regardless of who set it")
	}
}

func TestTickKeepsYoungNotice(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "down", SetAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(100, 0) }

	updated, cmd := m.handleAgentTick(agentTickMsg{})
	m = updated
	if _, ok := m.state.Notice(); !ok {
		t.Fatal("young notice must be retained")
	}
	if cmd == nil {
		t.Fatal("tick must keep ticking while a notice is up")
	}
}

// A failed turn sets a provider/internal notice AND appends one compact
// transcript row — the banner is the live surface, the row is the record.
func TestAgentFinishedErrorSetsNoticeAndCompactRow(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.handleAgentFinished(agentFinishedMsg{err: errors.New("provider request timed out")})
	m = updated
	n, ok := m.state.Notice()
	if !ok {
		t.Fatal("Notice() ok = false after a failed turn")
	}
	if n.Category != session.NoticeInternal { // plain error, not provider-typed
		t.Fatalf("category = %v, want NoticeInternal", n.Category)
	}
	var found bool
	for _, msg := range m.state.Messages() {
		if msg.Role == session.RoleSystem && strings.Contains(msg.Content, "Turn failed: provider request timed out") {
			found = true
		}
	}
	if !found {
		t.Fatal("failed turn must append a compact 'Turn failed' transcript row")
	}
}

// Plan-author failure reports once: notice + compact row, no second
// verbose transcript message.
func TestPlanAuthorFailureDoesNotDoubleReport(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.handlePlanAuthorFinished(planAuthorFinishedMsg{err: errors.New("boom")})
	m = updated
	var rows int
	for _, msg := range m.state.Messages() {
		if msg.Role == session.RoleSystem && strings.Contains(msg.Content, "Plan authoring failed") {
			rows++
			if strings.Count(msg.Content, "\n") > 0 {
				t.Fatalf("plan failure row must be one line, got %q", msg.Content)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("plan failure transcript rows = %d, want exactly 1", rows)
	}
	if _, ok := m.state.Notice(); !ok {
		t.Fatal("plan failure must also set a notice")
	}
}

// Esc while idle dismisses the notice instead of falling through to
// cancelTurn (which is a no-op when idle anyway).
func TestEscDismissesNoticeWhenIdle(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeInternal, Message: "boom"})

	updated, cmd, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if !handled {
		t.Fatal("esc with a visible notice should be handled")
	}
	_ = cmd
	if _, ok := m.state.Notice(); ok {
		t.Fatal("esc should dismiss the notice")
	}
}

// Regression: the notice banner is rendered into the transcript, so
// dismissing it must repaint the viewport. Before the notice was folded
// into transcriptHash, refreshViewport early-returned on an unchanged hash
// and the banner stayed on screen after esc-dismiss (and after the TTL
// auto-dismiss) until an unrelated transcript change forced a repaint.
func TestEscDismissRepaintsBannerAway(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeInternal, Message: "boom"})
	m.refreshViewport()
	if plain := stripANSI(m.viewport.View()); !strings.Contains(plain, "boom") {
		t.Fatalf("banner should be visible before dismiss:\n%s", plain)
	}

	updated, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if !handled {
		t.Fatal("esc with a visible notice should be handled")
	}
	m.refreshViewport()
	if plain := stripANSI(m.viewport.View()); strings.Contains(plain, "boom") {
		t.Fatalf("banner must be repainted away after esc-dismiss:\n%s", plain)
	}
}

// Regression: the TTL auto-dismiss path in handleAgentTick must also
// repaint the banner away. The tick dismisses the notice and then exits
// through the !busy && !successPulse && !noticePending guard, so the
// repaint relies on the notice being folded into transcriptHash.
func TestTickDismissRepaintsBannerAway(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Severity: session.SeverityError, Message: "down", SetAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(100, 0) }
	m.refreshViewport()
	if plain := stripANSI(m.viewport.View()); !strings.Contains(plain, "down") {
		t.Fatalf("banner should be visible before TTL:\n%s", plain)
	}

	m.now = func() time.Time { return time.Unix(100+int64(noticeBannerDuration/time.Second)+1, 0) }
	updated, _ := m.handleAgentTick(agentTickMsg{})
	m = updated
	m.refreshViewport()
	if plain := stripANSI(m.viewport.View()); strings.Contains(plain, "down") {
		t.Fatalf("banner must be repainted away after TTL auto-dismiss:\n%s", plain)
	}
}
