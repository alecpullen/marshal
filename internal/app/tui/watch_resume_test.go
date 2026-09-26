package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/picker"
	"marshal/internal/watch"
)

// assertNoWake fails when the runner is invoked inside the window. It is the
// shared negative-case assertion for the suppressed wake paths: the runner's
// Run pushes onto a buffered channel, so an empty read means no turn started.
func assertNoWake(t *testing.T, runner *fakeAgentRunner) {
	t.Helper()
	select {
	case goal := <-runner.called:
		t.Fatalf("auto-resume started a turn that should have been suppressed: %q", goal)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestWatchResumeWakeOnIdleFire: idle model, gate on, runner wired, event
// with Resume=true -> a turn starts with the wrapper goal.
func TestWatchResumeWakeOnIdleFire(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.AddMessage(session.RoleAssistant, "I'll wait for the build to finish.", session.ContentTypePlain)
	m2, cmd := m.handleWatchMsg(watchMsg{event: resumeWatchEvent("w1", "build", watch.ModeOnce)})
	if !m2.busy {
		t.Fatal("auto-resume did not set the busy flag")
	}
	if cmd == nil {
		t.Fatal("auto-resume returned no cmd")
	}
	drainCmds(m2, cmd)
	select {
	case goal := <-runner.called:
		if !strings.Contains(goal, `Watch "build" fired`) || !strings.Contains(goal, "I'll wait for the build to finish.") {
			t.Fatalf("resume goal = %q", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto-resume did not start a turn")
	}
}

// TestWatchResumeGoalQuoteCapped: a long pre-idle statement is truncated in
// the wrapper goal rather than pasted whole.
func TestWatchResumeGoalQuoteCapped(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.AddMessage(session.RoleAssistant, strings.Repeat("y", 500), session.ContentTypePlain)
	m2, cmd := m.handleWatchMsg(watchMsg{event: resumeWatchEvent("w1", "build", watch.ModeRepeat)})
	drainCmds(m2, cmd)
	select {
	case goal := <-runner.called:
		if !strings.Contains(goal, strings.Repeat("y", 400)) {
			t.Fatalf("goal did not quote the assistant text: %q", goal)
		}
		if strings.Contains(goal, strings.Repeat("y", 401)) {
			t.Fatalf("goal quote was not capped: %q", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto-resume did not start a turn")
	}
}

// TestWatchResumeBusyFireNoWake: a busy session is never woken by a fire --
// that case belongs to the turn-end latch instead.
func TestWatchResumeBusyFireNoWake(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.busy = true
	m.state.AddMessage(session.RoleAssistant, "working", session.ContentTypePlain)
	m2, _ := m.handleWatchMsg(watchMsg{event: resumeWatchEvent("w1", "build", watch.ModeOnce)})
	if !m2.busy {
		t.Fatal("a refused wake must leave the session busy")
	}
	assertNoWake(t, runner)
}

// TestWatchResumeGateOffNoWake: the [watch] resume_enabled master switch
// suppresses the idle wake.
func TestWatchResumeGateOffNoWake(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.Config.Watch.ResumeEnabled = false
	m.state.AddMessage(session.RoleAssistant, "waiting", session.ContentTypePlain)
	m2, _ := m.handleWatchMsg(watchMsg{event: resumeWatchEvent("w1", "build", watch.ModeOnce)})
	if m2.busy {
		t.Fatal("gate-off wake set the busy flag")
	}
	assertNoWake(t, runner)
}

// TestWatchResumeDockOpenNoWake: "never wake over an open panel".
func TestWatchResumeDockOpenNoWake(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.AddMessage(session.RoleAssistant, "waiting", session.ContentTypePlain)
	m.dock.Open(picker.New("Pick", "", []picker.Item{{Label: "one"}}))
	m2, _ := m.handleWatchMsg(watchMsg{event: resumeWatchEvent("w1", "build", watch.ModeOnce)})
	if m2.busy {
		t.Fatal("dock-open wake set the busy flag")
	}
	assertNoWake(t, runner)
}

// TestWatchResumeEventNotBearingNoWake: a plain (report-less) event never
// wakes, even on an idle session.
func TestWatchResumeEventNotBearingNoWake(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.AddMessage(session.RoleAssistant, "waiting", session.ContentTypePlain)
	m2, _ := m.handleWatchMsg(watchMsg{event: watchEvent("w1", "build", watch.KindCommand, watch.StateWatching)})
	if m2.busy {
		t.Fatal("non-bearing event set the busy flag")
	}
	assertNoWake(t, runner)
}

// TestWatchResumeNoAssistantQuote: with no assistant message the wrapper goal
// omits the quote sentence entirely.
func TestWatchResumeNoAssistantQuote(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m2, cmd := m.handleWatchMsg(watchMsg{event: resumeWatchEvent("w1", "build", watch.ModeOnce)})
	drainCmds(m2, cmd)
	select {
	case goal := <-runner.called:
		if strings.Contains(goal, "You last said") {
			t.Fatalf("goal quoted a nonexistent assistant message: %q", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto-resume did not start a turn")
	}
}

// TestHandleAgentFinishedConsumesLatch: the latch a turn-end residual armed
// is consumed at the idle boundary and starts the resumed turn.
func TestHandleAgentFinishedConsumesLatch(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.SetWatchResume("build", false)
	m.state.AddMessage(session.RoleAssistant, "I'll wait for the build.", session.ContentTypePlain)
	m2, cmd := m.handleAgentFinished(agentFinishedMsg{})
	drainCmds(m2, cmd)
	select {
	case goal := <-runner.called:
		if !strings.Contains(goal, `Watch "build" fired`) || !strings.Contains(goal, "I'll wait for the build.") {
			t.Fatalf("resume goal = %q", goal)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("latch did not start a turn")
	}
	if _, _, ok := m.state.TakeWatchResume(); ok {
		t.Fatal("latch was not cleared by the idle boundary")
	}
}

// TestHandleAgentFinishedLatchSuppressedByDock: a suppressed wake drops the
// consumed intent -- it is not re-queued.
func TestHandleAgentFinishedLatchSuppressedByDock(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.SetWatchResume("build", false)
	m.state.AddMessage(session.RoleAssistant, "waiting", session.ContentTypePlain)
	m.dock.Open(picker.New("Pick", "", []picker.Item{{Label: "one"}}))
	m2, _ := m.handleAgentFinished(agentFinishedMsg{})
	if m2.busy {
		t.Fatal("dock-open latch wake set the busy flag")
	}
	assertNoWake(t, runner)
	if _, _, ok := m.state.TakeWatchResume(); ok {
		t.Fatal("suppressed latch must be cleared, not re-queued")
	}
}

// TestHandleAgentFinishedGateOff: the config gate suppresses the latch wake
// too, and the consumed intent is dropped.
func TestHandleAgentFinishedGateOff(t *testing.T) {
	m := newTestModel(t)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.Config.Watch.ResumeEnabled = false
	m.state.SetWatchResume("build", false)
	m.state.AddMessage(session.RoleAssistant, "waiting", session.ContentTypePlain)
	m2, _ := m.handleAgentFinished(agentFinishedMsg{})
	if m2.busy {
		t.Fatal("gate-off latch wake set the busy flag")
	}
	assertNoWake(t, runner)
	if _, _, ok := m.state.TakeWatchResume(); ok {
		t.Fatal("gate-off latch must be cleared, not re-queued")
	}
}
