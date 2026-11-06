package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/activeworkpanel"
)

// fakeNewSession is the runtime's session factory: it records each request and
// hands back the state the test configured.
type fakeNewSession struct {
	newState *session.State
	called   int
	lastName string
	err      error
}

// newSession is the Deps.NewSession the host calls for each replacement.
func (f *fakeNewSession) newSession(name string) (*session.State, error) {
	f.called++
	f.lastName = name
	if f.err != nil {
		return nil, f.err
	}
	return f.newState, nil
}

// testModelReplacing builds a model whose host creates sessions through f and
// whose end service is end, as app.go wires them.
func testModelReplacing(t *testing.T, oldState *session.State, f *fakeNewSession, end inproc.EndService, opts ...Option) Model {
	t.Helper()
	host, sess := inproc.New(inproc.Deps{State: oldState, NewSession: f.newSession, End: end, Commands: setupCmdReg(t)})
	return New(host, sess, opts...)
}

// TestANewSessionLeavesTheEndIdle pins decision 10: a completed replacement is
// not an ending, so the model returns to idle and the next prompt starts a turn
// on the new session instead of being refused as ending.
func TestANewSessionLeavesTheEndIdle(t *testing.T) {
	f := &fakeNewSession{newState: postmortemTestState(t, "sess-new")}
	m := testModelReplacing(t, postmortemTestState(t, "sess-old"), f, &endTestController{})
	m.resize(80, 24)

	updated, cmd := m.dispatchCommand("/new")
	m = asModel(t, updated)
	m, _ = runEndCmd(t, m, cmd)

	if f.called == 0 {
		t.Fatal("the replacement never asked the host for a session")
	}
	if m.endStage != endIdle {
		t.Fatalf("endStage = %v after /new, want idle", m.endStage)
	}

	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m = withPromptRunner(m, runner)
	updated, cmd = m.startPrompt("hello after the swap", false)
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("a prompt after /new was refused")
	}
	if hasSystemLine(stateOf(m), "The conversation is ending; this request was not started.") {
		t.Fatal("the model still believes the conversation is ending after /new")
	}
	m = drainCmds(m, cmd)
	select {
	case goal := <-runner.called:
		if goal != "hello after the swap" {
			t.Fatalf("runner goal = %q, want the prompt text", goal)
		}
	case <-time.After(time.Second):
		t.Fatal("the prompt after /new never reached the runner")
	}
}

// TestASecondNewSessionStartsAnother pins that the replacement is repeatable:
// the first one leaves the model idle, so a second /new runs the whole path
// again rather than being treated as a repeated ending.
func TestASecondNewSessionStartsAnother(t *testing.T) {
	f := &fakeNewSession{newState: postmortemTestState(t, "sess-new")}
	m := testModelReplacing(t, postmortemTestState(t, "sess-old"), f, &endTestController{})
	m.resize(80, 24)

	for i := 0; i < 2; i++ {
		updated, cmd := m.dispatchCommand("/new")
		m = asModel(t, updated)
		m, cmd = runEndCmd(t, m, cmd)
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); ok {
				t.Fatalf("/new #%d quit the program instead of replacing the session", i+1)
			}
		}
	}

	if f.called != 2 {
		t.Fatalf("NewSession calls = %d, want 2", f.called)
	}
}

// TestExitAfterANewSessionStillWarns pins that the replacement session is the
// live one: an exit reads its membership, so the warning still opens.
func TestExitAfterANewSessionStillWarns(t *testing.T) {
	ctrl := &endTestController{}
	f := &fakeNewSession{newState: postmortemTestState(t, "sess-new")}
	m := testModelReplacing(t, postmortemTestState(t, "sess-old"), f, ctrl)
	m.resize(80, 24)

	updated, cmd := m.dispatchCommand("/new")
	m = asModel(t, updated)
	m, _ = runEndCmd(t, m, cmd)

	// The end service now holds one item, as the live session would see it.
	ctrl.work = workSnapshot("job-1")
	// The replacement itself went through the end service, so the exit is
	// judged by what it adds, not by the history.
	accepted := len(ctrl.accepted)

	updated, cmd = m.dispatchCommand("/exit")
	m = asModel(t, updated)

	if cmd != nil {
		t.Fatalf("/exit after /new returned a cmd (%T), want nil so the warning can be answered", cmd)
	}
	if _, ok := m.dock.Panel().(*activeworkpanel.Panel); !ok {
		t.Fatalf("dock panel = %T, want the active-work warning", m.dock.Panel())
	}
	if len(ctrl.accepted) != accepted {
		t.Fatal("/exit accepted the ending before the user decided")
	}
}

// TestResumeAfterANewSessionRecordsTheTarget pins that a resume still works
// after a replacement: it is a quit that carries the target.
func TestResumeAfterANewSessionRecordsTheTarget(t *testing.T) {
	f := &fakeNewSession{newState: postmortemTestState(t, "sess-new")}
	m := testModelReplacing(t, postmortemTestState(t, "sess-old"), f, &endTestController{})
	m.resize(80, 24)

	updated, cmd := m.dispatchCommand("/new")
	m = asModel(t, updated)
	m, _ = runEndCmd(t, m, cmd)

	updated, cmd = m.beginResume("sess-x")
	m = asModel(t, updated)
	m, cmd = runEndCmd(t, m, cmd)

	if cmd == nil {
		t.Fatal("the resume after /new did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
	if got := m.ResumeSession(); got != "sess-x" {
		t.Fatalf("ResumeSession() = %q, want sess-x", got)
	}
}

// TestAFailedNewSessionCanBeRetried pins the failure path: the note is the same
// one, the old session stays current, the end is idle, and a later /new
// succeeds.
func TestAFailedNewSessionCanBeRetried(t *testing.T) {
	newState := postmortemTestState(t, "sess-new")
	f := &fakeNewSession{newState: newState, err: errors.New("boom")}
	m := testModelReplacing(t, postmortemTestState(t, "sess-old"), f, &endTestController{})
	m.resize(80, 24)

	updated, cmd := m.dispatchCommand("/new")
	m = asModel(t, updated)
	m, _ = runEndCmd(t, m, cmd)

	if !hasSystemLine(stateOf(m), "Failed to start new session: boom") {
		t.Fatal("the failed replacement wrote no failure note")
	}
	if got := m.sess.ID(); got != "sess-old" {
		t.Fatalf("session ID after a failed /new = %q, want the old session", got)
	}
	if m.endStage != endIdle {
		t.Fatalf("endStage = %v after a failed /new, want idle", m.endStage)
	}

	f.err = nil
	updated, cmd = m.dispatchCommand("/new")
	m = asModel(t, updated)
	m, _ = runEndCmd(t, m, cmd)

	if stateOf(m) != newState {
		t.Fatal("the retried /new did not swap in the new state")
	}
	if f.called != 2 {
		t.Fatalf("NewSession calls = %d, want 2", f.called)
	}
}
