package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
)

// noticeForError takes the notice the agent attached to a failed run's error
// (C9). Provider and login classification lives agent side, where the typed
// errors are; the service's runError pins those rules.
func TestNoticeForErrorTakesTheAgentsNotice(t *testing.T) {
	agentNotice := acpwire.Notice{
		Category: acpwire.NoticeProvider,
		Severity: acpwire.NoticeError,
		Message:  `provider "openai": 500`,
		Hint:     "Run /connect to review the provider, or /models to pick another model.",
	}
	data, err := json.Marshal(acpwire.ErrorData{Notice: &agentNotice})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	coded := fmt.Errorf("turn failed: %w", &acpwire.Error{
		Code:    acpwire.CodeProviderError,
		Message: `provider "openai": 500`,
		Data:    data,
	})

	n := noticeForError(coded, "turn")
	if n.Category != agentNotice.Category || n.Severity != agentNotice.Severity {
		t.Fatalf("notice = %+v, want the agent's category and severity", n)
	}
	if n.Hint != agentNotice.Hint {
		t.Errorf("hint = %q, want %q", n.Hint, agentNotice.Hint)
	}
	if n.Source != "turn" {
		t.Errorf("source = %q, want %q", n.Source, "turn")
	}
}

func TestNoticeForErrorDefaultsToInternal(t *testing.T) {
	n := noticeForError(errors.New("something weird"), "turn")
	if n.Category != acpwire.NoticeInternal {
		t.Fatalf("category = %v, want NoticeInternal", n.Category)
	}
}

func TestRenderNoticeShowsCategoryMessageHint(t *testing.T) {
	n := acpwire.Notice{
		Category: acpwire.NoticeProvider,
		Severity: acpwire.NoticeError,
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
	n := acpwire.Notice{
		Category: acpwire.NoticeInternal,
		Severity: acpwire.NoticeError,
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
	stateOf(m).SetNotice(session.Notice{Category: session.NoticeProvider, Severity: session.SeverityError, Message: "down", SetAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(100+int64(noticeBannerDuration/time.Second)+1, 0) }

	updated, _ := m.handleAgentTick(agentTickMsg{})
	m = updated
	if _, ok := stateOf(m).Notice(); ok {
		t.Fatal("notice should be dismissed after the TTL regardless of who set it")
	}
}

func TestTickKeepsYoungNotice(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).SetNotice(session.Notice{Category: session.NoticeProvider, Message: "down", SetAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(100, 0) }

	updated, cmd := m.handleAgentTick(agentTickMsg{})
	m = updated
	if _, ok := stateOf(m).Notice(); !ok {
		t.Fatal("young notice must be retained")
	}
	if cmd == nil {
		t.Fatal("tick must keep ticking while a notice is up")
	}
}

// A failed turn sets a provider/internal notice. The transcript row is the
// agent's (P4 D6), so this handler asserts only the banner; the row is pinned
// against a real failing run in TestAFailedTurnWritesOneCompactRow.
func TestAgentFinishedErrorSetsTheNotice(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.handleAgentFinished(agentFinishedMsg{err: errors.New("provider request timed out")})
	m = updated
	n, ok := stateOf(m).Notice()
	if !ok {
		t.Fatal("Notice() ok = false after a failed turn")
	}
	if n.Category != session.NoticeInternal { // plain error, not provider-typed
		t.Fatalf("category = %v, want NoticeInternal", n.Category)
	}
}

// A failing turn appends exactly one compact transcript row, written by the
// agent: the TUI adding its own would double it (P4 D6).
func TestAFailedTurnWritesOneCompactRow(t *testing.T) {
	r := &fakeAgentRunner{
		called: make(chan string, 1),
		err:    &provider.ProviderError{Provider: "openai", StatusCode: 500, Body: "boom"},
	}
	state := modelTestState(t)
	m := testModelWith(t, service.Deps{State: state, Runner: runnerOf(r)})
	m.resize(80, 24)

	m.input.SetValue("hello")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, updated)
	m = drainCmds(m, cmd)

	var rows int
	for _, msg := range state.Messages() {
		if msg.Role == session.RoleSystem && strings.Contains(msg.Content, "Turn failed") {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("\"Turn failed\" transcript rows = %d, want exactly 1", rows)
	}
}

// Plan-author failure reports once: a notice and one compact agent-written
// row, driven through a real failing authoring run.
func TestPlanAuthorFailureDoesNotDoubleReport(t *testing.T) {
	m := testModelTurns(t, service.Deps{
		State:  controlsState(t),
		Runner: runnerOf(&testAgentRunner{}),
		AuthorPlan: func(context.Context, acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
			return acpwire.AuthoredPlan{}, errors.New("boom")
		},
		Commands: setupCmdReg(t),
	}, &turnRecorder{})

	updated, cmd := m.dispatchCommand("/sdd new x")
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("/sdd new returned no command")
	}
	m = runAuthoringCmd(t, m, cmd)

	var rows int
	for _, msg := range stateOf(m).Messages() {
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
	if _, ok := stateOf(m).Notice(); !ok {
		t.Fatal("plan failure must also set a notice")
	}
}

// Esc while idle dismisses the notice instead of falling through to
// cancelTurn (which is a no-op when idle anyway).
func TestEscDismissesNoticeWhenIdle(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).SetNotice(session.Notice{Category: session.NoticeInternal, Message: "boom"})

	updated, cmd, handled := m.handleKeypress(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if !handled {
		t.Fatal("esc with a visible notice should be handled")
	}
	_ = cmd
	if _, ok := stateOf(m).Notice(); ok {
		t.Fatal("esc should dismiss the notice")
	}
}

// Regression: the notice banner is rendered into the transcript, so
// dismissing it must repaint the viewport. Before the notice was folded
// into the content signature, refreshViewport early-returned on an unchanged hash
// and the banner stayed on screen after esc-dismiss (and after the TTL
// auto-dismiss) until an unrelated transcript change forced a repaint.
func TestEscDismissRepaintsBannerAway(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).SetNotice(session.Notice{Category: session.NoticeInternal, Message: "boom"})
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
// repaint relies on the notice being folded into the content signature.
func TestTickDismissRepaintsBannerAway(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).SetNotice(session.Notice{Category: session.NoticeProvider, Severity: session.SeverityError, Message: "down", SetAt: time.Unix(100, 0)})
	m.now = func() time.Time { return time.Unix(100, 0) }
	m.refreshViewport()
	if plain := stripANSI(m.viewport.View()); !strings.Contains(plain, "down") {
		t.Fatalf("banner should be visible before TTL:\n%s", plain)
	}

	m.now = func() time.Time { return time.Unix(100+int64(noticeBannerDuration/time.Second)+1, 0) }
	updated, _ := m.Update(agentTickMsg{})
	m = updated.(Model)
	if plain := stripANSI(m.viewport.View()); strings.Contains(plain, "down") {
		t.Fatalf("banner must be repainted away after TTL auto-dismiss:\n%s", plain)
	}
}

// A warning-severity notice renders with the warning glyph and style,
// and the status bar renders the "⚠ warning" segment. This covers the
// otherwise-dead warn branches in renderNotice and statusRightSegment.
func TestRenderWarnNotice(t *testing.T) {
	m := newTestModel(t)
	stateOf(m).SetNotice(session.Notice{Category: session.NoticeConfig, Severity: session.SeverityWarn, Message: "old config key ignored"})
	m.refreshViewport()
	plain := stripANSI(m.viewport.View())
	if !strings.Contains(plain, "old config key ignored") {
		t.Fatalf("warn banner message missing:\n%s", plain)
	}
	status := stripANSI(m.statusRightSegment())
	if !strings.Contains(status, "warning") {
		t.Fatalf("status segment should show warning, got %q", status)
	}
}
