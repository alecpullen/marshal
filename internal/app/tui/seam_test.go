package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// testSession returns the client session the in-process adapter builds over
// state.
func testSession(state *session.State) acpclient.Session {
	_, sess := inproc.New(inproc.Deps{State: state})
	return sess
}

// testModel is the one place tests build a model: it wires the in-process
// adapter and hands the state in through WithLegacyState, so P3 changes only
// this helper.
func testModel(state *session.State, opts ...Option) Model {
	host, sess := inproc.New(inproc.Deps{State: state})
	all := append([]Option{WithLegacyState(state)}, opts...)
	return New(host, sess, all...)
}

// fakeHost is an acpclient.Host whose SharedFilesystem is the test's choice.
type fakeHost struct{ shared bool }

func (h fakeHost) SharedFilesystem() bool { return h.shared }

// seamState builds a state for the seam tests.
func seamState(t *testing.T, id string) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: id})
}

// assertPanics runs fn and fails unless it panics with want.
func assertPanics(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		got := recover()
		if got == nil {
			t.Fatalf("expected panic %q, got none", want)
		}
		if msg, ok := got.(string); !ok || msg != want {
			t.Fatalf("panic = %v, want %q", got, want)
		}
	}()
	fn()
}

func TestNewRequiresTheLegacyStateUntilPlan9(t *testing.T) {
	state := seamState(t, "s1")
	assertPanics(t, "tui.New: WithLegacyState is required until P2 plan 9", func() {
		New(fakeHost{shared: true}, testSession(state))
	})
	assertPanics(t, "tui.New: nil client host or session", func() {
		New(nil, testSession(state), WithLegacyState(state))
	})
}

func TestOpenFileNeedsASharedFilesystem(t *testing.T) {
	t.Setenv("EDITOR", "vi")
	state := seamState(t, "s1")
	m := New(fakeHost{shared: false}, testSession(state), WithLegacyState(state))

	if cmd := m.openFile("main.go", 1); cmd == nil {
		t.Fatal("openFile returned no command")
	}
	if want := "$EDITOR is unavailable: the files are on the agent's machine"; m.flash != want {
		t.Fatalf("flash = %q, want %q", m.flash, want)
	}
}

func TestOpenFileWithASharedFilesystemReachesTheEditorChecks(t *testing.T) {
	t.Setenv("EDITOR", "")
	m := testModel(seamState(t, "s1"))

	m.openFile("main.go", 1)
	if want := "$EDITOR is not set"; m.flash != want {
		t.Fatalf("flash = %q, want %q", m.flash, want)
	}
}

func TestNewSessionSwapsTheClientSession(t *testing.T) {
	oldState := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{SessionID: "old"})
	newState := session.New(config.Default(), "/repo", time.Unix(200, 0), session.Persistence{SessionID: "new"})

	cmdReg := setupCmdReg(t)
	swapper := &fakeSessionSwapper{newState: newState}
	model := testModel(oldState, WithCommandRegistry(cmdReg), WithSessionSwapper(swapper))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	if got := model.sess.ID(); got != "old" {
		t.Fatalf("session ID before the swap = %q, want old", got)
	}

	model.input.SetValue("/new")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := updated.(*Model)

	if !swapper.called {
		t.Fatal("expected session swapper to be called")
	}
	if m.state != newState {
		t.Fatal("expected model state to be swapped to the new session")
	}
	if got := m.sess.ID(); got != "new" {
		t.Fatalf("session ID after the swap = %q, want new", got)
	}
	if !strings.Contains(m.state.Messages()[0].Content, "Started new conversation") {
		t.Errorf("expected the new-session system message, got: %s", m.state.Messages()[0].Content)
	}
}

// stateOnlySwapper swaps the legacy state but reports no client session, the
// shape a future swapper implementation can produce.
type stateOnlySwapper struct{ state *session.State }

func (s stateOnlySwapper) NewSession(string) (SessionSwapResult, error) {
	return SessionSwapResult{State: s.state}, nil
}

func TestNewSessionKeepsTheSeamSessionWhenTheSwapperOmitsIt(t *testing.T) {
	oldState := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{SessionID: "old"})
	newState := session.New(config.Default(), "/repo", time.Unix(200, 0), session.Persistence{SessionID: "new"})

	cmdReg := setupCmdReg(t)
	model := testModel(oldState, WithCommandRegistry(cmdReg), WithSessionSwapper(stateOnlySwapper{state: newState}))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/new")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := updated.(*Model)

	if m.state != newState {
		t.Fatal("expected model state to be swapped to the new session")
	}
	if m.sess == nil {
		t.Fatal("expected the seam session to survive a swapper that omits it")
	}
	if got := m.sess.ID(); got != "old" {
		t.Fatalf("session ID after the swap = %q, want old", got)
	}
}
