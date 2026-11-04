package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
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

// testModelWith builds a model over an adapter made from d, as testModel
// builds one over d.State alone.
func testModelWith(d inproc.Deps, opts ...Option) Model {
	host, sess := inproc.New(d)
	all := append([]Option{WithLegacyState(d.State)}, opts...)
	return New(host, sess, all...)
}

// testModelCached builds a model whose host caches discovered model lists
// under dir, as production wires the runtime's data directory.
func testModelCached(t *testing.T, state *session.State, dir string, opts ...Option) Model {
	t.Helper()
	return testModelWith(inproc.Deps{State: state, ModelCacheDir: dir, Auth: newMemAuth()}, opts...)
}

// runnerOf returns a Deps.Runner that always returns r.
func runnerOf(r inproc.Runner) func() inproc.Runner {
	return func() inproc.Runner { return r }
}

// withPromptRunner replaces m's session with one over m's state whose runner
// is r.
func withPromptRunner(m Model, r inproc.Runner) Model {
	_, sess := inproc.New(inproc.Deps{State: m.state, Runner: runnerOf(r)})
	m.sess = sess
	return m
}

// stubSession wraps the adapter's session for tests that need a value only
// events or a call produce: patch adjusts every Status read, onSendNow sees
// each SendNow request before the adapter does, onRefreshSheet sees each
// RefreshSheet request, onAddNote sees each Note before the adapter does, and
// sheetPatch adjusts every Sheet read.
type stubSession struct {
	acpclient.Session
	patch          func(*acpwire.SessionStatus)
	onSendNow      func(acpwire.SendNowRequest)
	onRefreshSheet func(acpwire.SheetRefresh)
	onAddNote      func(acpwire.Note)
	onDismissNote  func()
	sheetPatch     func(*acpwire.SessionSheet)
}

func (s *stubSession) Status() acpwire.SessionStatus {
	st := s.Session.Status()
	if s.patch != nil {
		s.patch(&st)
	}
	return st
}

func (s *stubSession) SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error) {
	if s.onSendNow != nil {
		s.onSendNow(req)
	}
	return s.Session.SendNow(ctx, req)
}

func (s *stubSession) RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error {
	if s.onRefreshSheet != nil {
		s.onRefreshSheet(req)
	}
	return s.Session.RefreshSheet(ctx, req)
}

func (s *stubSession) AddNote(ctx context.Context, note acpwire.Note) error {
	if s.onAddNote != nil {
		s.onAddNote(note)
	}
	return s.Session.AddNote(ctx, note)
}

func (s *stubSession) DismissNotice(ctx context.Context) error {
	if s.onDismissNote != nil {
		s.onDismissNote()
	}
	return s.Session.DismissNotice(ctx)
}

func (s *stubSession) Sheet(subagentID int64) acpwire.SessionSheet {
	sheet := s.Session.Sheet(subagentID)
	if s.sheetPatch != nil {
		s.sheetPatch(&sheet)
	}
	return sheet
}

// testModelStub builds a model whose session is stub, wrapped around an
// adapter session over state.
func testModelStub(state *session.State, stub *stubSession, opts ...Option) Model {
	return testModelStubWith(inproc.Deps{State: state}, stub, opts...)
}

// testModelStubWith is testModelStub over an adapter made from d.
func testModelStubWith(d inproc.Deps, stub *stubSession, opts ...Option) Model {
	host, sess := inproc.New(d)
	stub.Session = sess
	all := append([]Option{WithLegacyState(d.State)}, opts...)
	return New(host, stub, all...)
}

// withCommands replaces m's session with one over m's state whose registry is
// reg, as withPromptRunner does for the runner.
func withCommands(m Model, reg *commands.Registry) Model {
	_, sess := inproc.New(inproc.Deps{State: m.state, Commands: reg})
	m.sess = sess
	return m
}

// batchResults runs cmd and returns the messages it produces. The commands of
// a batch run concurrently under one deadline, because the change pump blocks
// until the next signal and a sequential run would wait on it.
//
// A blocking pump member never delivers, so the deadline is expected to fire on
// any batch that contains one. Tests that assert something is *absent* must
// therefore not lean on this collection alone: assert the state that issues the
// command too, or a slow command could hide behind the deadline.
func batchResults(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := runCmdOnce(cmd, time.Second)
	if _, timedOut := msg.(cmdTimeout); timedOut {
		return nil
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	}
	out := make(chan tea.Msg, len(batch))
	started := 0
	for _, c := range batch {
		if c == nil {
			continue
		}
		started++
		go func(c tea.Cmd) { out <- c() }(c)
	}
	var msgs []tea.Msg
	deadline := time.After(time.Second)
	for received := 0; received < started; {
		select {
		case m := <-out:
			received++
			if m != nil {
				msgs = append(msgs, m)
			}
		case <-deadline:
			return msgs
		}
	}
	return msgs
}

// fakeHost is an acpclient.Host whose SharedFilesystem is the test's choice.
// The embedded interface supplies the methods these tests never call.
type fakeHost struct {
	acpclient.Host
	shared bool
}

func (h fakeHost) SharedFilesystem() bool { return h.shared }

// NewSession refuses: the tests drive one session built by testSession.
func (h fakeHost) NewSession(context.Context, string) (acpclient.Session, error) {
	return nil, errors.New("fakeHost creates no sessions")
}

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

	f := &fakeNewSession{newState: newState}
	model := testModelReplacing(t, oldState, f, &endTestController{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	if got := model.sess.ID(); got != "old" {
		t.Fatalf("session ID before the swap = %q, want old", got)
	}

	model.input.SetValue("/new")
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := asModel(t, updated)
	// The swap follows the settled end.
	m, _ = runEndCmd(t, m, cmd)

	if f.called == 0 {
		t.Fatal("expected the host to create a new session")
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
