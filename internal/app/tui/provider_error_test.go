package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
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

// A runner the runtime builds after startup is used without adoption: the
// session reads the runtime's current runner on each call, so a model that
// started without one recovers as soon as the runtime has one.
func TestARunnerBuiltAfterStartupIsUsedWithoutAdoption(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	var built inproc.Runner
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelWith(inproc.Deps{State: state, Runner: func() inproc.Runner { return built }})
	m.resize(80, 24)

	// No runner is built yet: the text echoes locally and no turn starts.
	m.input.SetValue("first")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, updated)
	if m.busy {
		t.Fatal("a submission with no runner started a turn")
	}
	if msgs := state.Messages(); len(msgs) == 0 || msgs[len(msgs)-1].Content != "first" {
		t.Fatalf("messages = %#v, want the user's text echoed", msgs)
	}

	// The runtime builds a runner (a reload, say). The next submission uses it
	// with no adoption step on the model.
	built = runner
	m.input.SetValue("second")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, updated)
	if !m.busy {
		t.Fatal("busy = false, want the second submission to start a turn")
	}
	drainCmds(m, cmd)
	select {
	case goal := <-runner.called:
		if goal != "second" {
			t.Fatalf("runner goal = %q, want the second submission", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the rebuilt runner was never invoked")
	}
}

// A new turn optimistically clears a stale provider notice: the user
// submitting another prompt is trying again, and if this turn also fails
// the finished handler re-sets it.
func TestStartPromptClearsProviderNotice(t *testing.T) {
	m := testModelWith(inproc.Deps{State: modelTestState(t), Runner: runnerOf(&testAgentRunner{})})
	m.resize(80, 24)
	m.state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "provider unreachable"})

	_, cmd := m.startPrompt("retry", false)
	if cmd == nil {
		t.Fatal("startPrompt returned no command")
	}
	if _, ok := m.state.Notice(); ok {
		t.Fatal("provider notice should be cleared on new turn start")
	}
}

// ClearNotice is category-scoped: a new turn must NOT clear an unrelated
// internal notice.
func TestStartPromptKeepsUnrelatedNotice(t *testing.T) {
	m := testModelWith(inproc.Deps{State: modelTestState(t), Runner: runnerOf(&testAgentRunner{})})
	m.resize(80, 24)
	m.state.SetNotice(session.Notice{Category: session.NoticeInternal, Message: "command registration failed"})

	_, cmd := m.startPrompt("retry", false)
	if cmd == nil {
		t.Fatal("startPrompt returned no command")
	}
	n, ok := m.state.Notice()
	if !ok || n.Category != session.NoticeInternal {
		t.Fatal("internal notice must survive a new turn start")
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
