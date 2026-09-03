package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/connect"
)

// The provider notice must clear on every recovery path, not only on a
// successful turn. A successful /connect is the banner's own suggested
// remedy, so leaving the notice set afterwards is contradictory.
func TestApplyConnectDoneSuccessClearsProviderNotice(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Severity: session.SeverityError, Message: "provider request timed out"})

	m.applyConnectDone(connect.DoneMsg{Provider: "openai", Model: "gpt-4o"})

	if _, ok := m.state.Notice(); ok {
		t.Fatal("notice should be cleared after successful /connect")
	}
}

// A successful save+reload rebuilt the provider runtime, which proves
// provider construction works with the new config — the stale notice must
// be cleared.
func TestPersistAndReloadSuccessClearsProviderNotice(t *testing.T) {
	m := newTestModel(t)
	m.configReloader = func(config.Config) error { return nil }
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "provider request timed out"})

	saveErr, reloadErr := m.persistAndReload(m.state.Config)
	if saveErr != nil || reloadErr != nil {
		t.Fatalf("persistAndReload = (%v, %v), want (nil, nil)", saveErr, reloadErr)
	}
	if _, ok := m.state.Notice(); ok {
		t.Fatal("notice should be cleared after successful reload")
	}
}

// A failed reload proves nothing about the provider — the notice must
// stay so the user keeps seeing why the agent is unavailable.
func TestPersistAndReloadFailureRetainsProviderNotice(t *testing.T) {
	m := newTestModel(t)
	m.configReloader = func(config.Config) error { return errors.New("cleanup failed") }
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "provider request timed out"})

	_, reloadErr := m.persistAndReload(m.state.Config)
	if reloadErr == nil {
		t.Fatal("persistAndReload reloadErr = nil, want the reload error")
	}
	if _, ok := m.state.Notice(); !ok {
		t.Fatal("notice = none, want the original notice retained after failed reload")
	}
}

// When the TUI started without a runner (startup provider-build failure),
// a successful config reload rebuilt one in the runtime — the model must
// adopt it, or every submission keeps saying "Agent runner is not
// available." and the session can never recover.
func TestSuccessfulReloadAdoptsMissingRunner(t *testing.T) {
	m := newTestModel(t)
	if m.runner != nil {
		t.Fatal("test precondition: newTestModel must start without a runner")
	}
	runner := &testAgentRunner{}
	m.runnerSource = func() (context.Context, AgentRunner) {
		return context.Background(), runner
	}
	m.configReloader = func(config.Config) error { return nil }

	saveErr, reloadErr := m.persistAndReload(m.state.Config)
	if saveErr != nil || reloadErr != nil {
		t.Fatalf("persistAndReload = (%v, %v), want (nil, nil)", saveErr, reloadErr)
	}
	if m.runner == nil {
		t.Fatal("runner = nil after successful reload, want the rebuilt runner adopted")
	}
}

// An existing runner must never be replaced by the source: reload mutates
// it in place via CopyFrom, and swapping pointers would orphan in-flight
// cancellation wiring.
func TestSuccessfulReloadKeepsExistingRunner(t *testing.T) {
	m := newTestModel(t)
	existing := &testAgentRunner{}
	m.runner = existing
	m.runnerSource = func() (context.Context, AgentRunner) {
		return context.Background(), &testAgentRunner{}
	}
	m.configReloader = func(config.Config) error { return nil }

	if _, reloadErr := m.persistAndReload(m.state.Config); reloadErr != nil {
		t.Fatalf("persistAndReload reloadErr = %v, want nil", reloadErr)
	}
	if m.runner != AgentRunner(existing) {
		t.Fatal("runner was replaced despite already being set")
	}
}

// A new turn optimistically clears a stale provider notice: the user
// submitting another prompt is trying again, and if this turn also fails
// the finished handler re-sets it.
func TestStartAgentRunClearsProviderNotice(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "provider unreachable"})

	_, cmd := m.startAgentRun(&testAgentRunner{}, "retry")
	if cmd == nil {
		t.Fatal("startAgentRun returned no command")
	}
	if _, ok := m.state.Notice(); ok {
		t.Fatal("provider notice should be cleared on new turn start")
	}
}

// ClearNotice is category-scoped: a new turn must NOT clear an unrelated
// internal notice.
func TestStartAgentRunKeepsUnrelatedNotice(t *testing.T) {
	m := newTestModel(t)
	m.state.SetNotice(session.Notice{Category: session.NoticeInternal, Message: "command registration failed"})

	_, cmd := m.startAgentRun(&testAgentRunner{}, "retry")
	if cmd == nil {
		t.Fatal("startAgentRun returned no command")
	}
	n, ok := m.state.Notice()
	if !ok || n.Category != session.NoticeInternal {
		t.Fatal("internal notice must survive a new turn start")
	}
}

// A degraded-decoding notice uses NoticeConfig so a successful turn cannot
// wipe it: the TUI clears NoticeProvider on the success path (a completed
// turn proves the provider is reachable), but a degraded configuration is
// not fixed by a successful turn — it persists until the config changes.
// NoticeConfig is cleared on runtime reload and model switch, the moments
// the condition can genuinely change.
func TestSuccessfulTurnKeepsDecodingNotice(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.state.SetNotice(session.Notice{Category: session.NoticeConfig, Message: "provider advertises no structured-output support"})

	mm, _ := m.handleAgentFinished(agentFinishedMsg{err: nil})
	m = mm
	n, ok := m.state.Notice()
	if !ok {
		t.Fatal("NoticeConfig decoding notice must survive a successful turn")
	}
	if n.Category != session.NoticeConfig {
		t.Fatalf("notice category = %v, want NoticeConfig", n.Category)
	}
}

// A cancelled turn clears a stale provider notice (the user retried);
// the cancellation itself never sets one.
func TestCancelledTurnClearsProviderNotice(t *testing.T) {
	m := newTestModel(t)
	m.busy = true
	m.cancelling = true
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "stale"})

	updated, _ := m.handleAgentFinished(agentFinishedMsg{err: errors.New("stream aborted")})
	m = updated
	if _, ok := m.state.Notice(); ok {
		t.Fatal("cancelled turn should clear the stale provider notice, not set a new one")
	}
	var found bool
	for _, msg := range m.state.Messages() {
		if strings.Contains(msg.Content, "cancelled") {
			found = true
		}
	}
	if !found {
		t.Fatal("cancelled turn should still add the 'Agent turn cancelled.' message")
	}
}
