package tui

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/trustpanel"
	"marshal/internal/commands"
	"marshal/internal/trust"
)

// newTrustInPlaceState builds the session state the in-place cases run over,
// with the command registry /trust needs.
func newTrustInPlaceState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{SessionID: unnamedSessionID})
}

// trustInPlaceModel builds a model whose host serves a prompt the agent applies
// in place: apply stands in for the runtime's ApplyTrust.
func trustInPlaceModel(t *testing.T, state *session.State, apply func(context.Context, trust.Decision) error, opts ...Option) Model {
	t.Helper()
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	deps := service.Deps{
		State:       state,
		Commands:    reg,
		TrustPrompt: "/some/project",
		ApplyTrust:  apply,
	}
	all := append([]Option{WithHomeDir(t.TempDir()), WithWorkingDir("/some/project")}, opts...)
	return testModelWith(t, deps, all...)
}

// chooseTrustPanel presses key, feeds the panel's message back through Update,
// and settles the queued call it starts.
func chooseTrustPanel(t *testing.T, m Model, key rune) Model {
	t.Helper()
	mm, cmd := m.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
	m = asModel(t, mm)
	if cmd == nil {
		t.Fatalf("key %c produced no command from the panel", key)
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		t.Fatalf("key %c quit the program from an in-place panel", key)
	}
	decided, ok := msg.(trustpanel.DecidedMsg)
	if !ok {
		t.Fatalf("key %c produced %T, want trustpanel.DecidedMsg", key, msg)
	}
	mm, settle := m.Update(decided)
	m = asModel(t, mm)
	if m.dock.IsOpen() {
		t.Fatal("the dock should close as soon as the panel reports the choice")
	}
	return settleCalls(t, m, settle)
}

// TestAnInPlaceTrustKeepsTheProgram: the panel opens at start, a trust applies
// through the queued call, and the program carries on rather than quitting.
func TestAnInPlaceTrustKeepsTheProgram(t *testing.T) {
	state := newTrustInPlaceState(t)
	var (
		mu      sync.Mutex
		applied []trust.Decision
	)
	apply := func(_ context.Context, d trust.Decision) error {
		mu.Lock()
		applied = append(applied, d)
		mu.Unlock()
		state.SetTrusted(true)
		return nil
	}
	m := trustInPlaceModel(t, state, apply)
	if !m.dock.IsOpen() {
		t.Fatal("the trust panel should own the dock at startup")
	}
	if _, ok := m.dock.Panel().(*trustpanel.Panel); !ok {
		t.Fatalf("dock panel = %T, want the trust panel", m.dock.Panel())
	}

	m = chooseTrustPanel(t, m, '1')

	if m.dock.IsOpen() {
		t.Fatal("the dock reopened after the call settled")
	}
	if !state.Trusted() {
		t.Fatal("the status is not trusted after an in-place trust")
	}
	mu.Lock()
	got := append([]trust.Decision(nil), applied...)
	mu.Unlock()
	want := []trust.Decision{trust.DecisionTrustPermanent}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applied decisions = %v, want %v", got, want)
	}
}

// TestAnInPlaceTrustOpensTheConnectPanel: a trust frees the dock the startup
// path deferred the connect panel to.
func TestAnInPlaceTrustOpensTheConnectPanel(t *testing.T) {
	state := newTrustInPlaceState(t)
	apply := func(context.Context, trust.Decision) error {
		state.SetTrusted(true)
		return nil
	}
	m := trustInPlaceModel(t, state, apply, WithOpenConnectOnStart())
	if _, ok := m.dock.Panel().(connect.Panel); ok {
		t.Fatal("connect must not pre-empt the pending trust prompt")
	}

	m = chooseTrustPanel(t, m, '1')

	if _, ok := m.dock.Panel().(connect.Panel); !ok {
		t.Fatalf("dock panel = %T, want the connect panel after a trust", m.dock.Panel())
	}
}

// TestAnInPlaceDeclineClosesAndAsksAgain: a decline applies nothing and leaves
// the prompt pending, so /trust asks again.
func TestAnInPlaceDeclineClosesAndAsksAgain(t *testing.T) {
	state := newTrustInPlaceState(t)
	calls := 0
	apply := func(context.Context, trust.Decision) error {
		calls++
		return nil
	}
	m := trustInPlaceModel(t, state, apply)

	m = chooseTrustPanel(t, m, '3')

	if calls != 0 {
		t.Fatalf("a decline applied %d decision(s), want none", calls)
	}
	if state.Trusted() {
		t.Fatal("a decline left the status trusted")
	}
	m.dispatchCommand("/trust")
	m = settleCalls(t, m)
	if _, ok := m.dock.Panel().(*trustpanel.Panel); !ok {
		t.Fatalf("/trust should ask again after a decline, got %T", m.dock.Panel())
	}
}

// TestAFailedInPlaceTrustKeepsTheProgram: a failed apply leaves the program
// running with the dock closed and the status untrusted, and /trust asks
// again.
func TestAFailedInPlaceTrustKeepsTheProgram(t *testing.T) {
	state := newTrustInPlaceState(t)
	apply := func(context.Context, trust.Decision) error {
		return errors.New("the config does not load")
	}
	m := trustInPlaceModel(t, state, apply)

	m = chooseTrustPanel(t, m, '1')

	if m.dock.IsOpen() {
		t.Fatal("the dock reopened after a failed trust")
	}
	if state.Trusted() {
		t.Fatal("a failed trust left the status trusted")
	}
	m.dispatchCommand("/trust")
	m = settleCalls(t, m)
	if _, ok := m.dock.Panel().(*trustpanel.Panel); !ok {
		t.Fatalf("/trust should ask again after a failed trust, got %T", m.dock.Panel())
	}
}

// TestATrustedSessionAsksNoTrustQuestionAgain: the client keeps the host's
// first trust/prompt answer for the connection's life, so a settled trust
// still leaves the prompt's directory in the client's cache. /trust must read
// the status rather than that cache, or a project the user just trusted would
// show the panel again.
func TestATrustedSessionAsksNoTrustQuestionAgain(t *testing.T) {
	state := newTrustInPlaceState(t)
	apply := func(context.Context, trust.Decision) error {
		state.SetTrusted(true)
		return nil
	}
	m := trustInPlaceModel(t, state, apply)

	m = chooseTrustPanel(t, m, '1')
	if !state.Trusted() {
		t.Fatal("the in-place trust did not take")
	}
	if m.dock.IsOpen() {
		t.Fatal("the dock reopened after the trust")
	}

	m.dispatchCommand("/trust")
	m = settleCalls(t, m)
	if _, ok := m.dock.Panel().(*trustpanel.Panel); ok {
		t.Fatal("/trust reopened the trust prompt for a trusted session")
	}
}
