package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// controlRecorder is a session whose turn and control calls are recorded
// before the wrapped session makes them. Every other method is the wrapped
// session's.
type controlRecorder struct {
	acpclient.Session
	steers       []string
	pops         int
	clears       int
	cancels      []int64
	runClears    int
	resumeTakes  int
	interactions []acpwire.InteractionPatch
	efforts      []string
	unlocks      []bool
}

func (r *controlRecorder) Steer(ctx context.Context, text string) error {
	r.steers = append(r.steers, text)
	return r.Session.Steer(ctx, text)
}

func (r *controlRecorder) PopSteering(ctx context.Context) (string, bool) {
	r.pops++
	return r.Session.PopSteering(ctx)
}

func (r *controlRecorder) ClearSteering(ctx context.Context) error {
	r.clears++
	return r.Session.ClearSteering(ctx)
}

func (r *controlRecorder) CancelSubagent(ctx context.Context, subagentID int64) bool {
	r.cancels = append(r.cancels, subagentID)
	return r.Session.CancelSubagent(ctx, subagentID)
}

func (r *controlRecorder) ClearFinishedRun(ctx context.Context) error {
	r.runClears++
	return r.Session.ClearFinishedRun(ctx)
}

func (r *controlRecorder) TakeWatchResume(ctx context.Context) (string, bool, bool) {
	r.resumeTakes++
	return r.Session.TakeWatchResume(ctx)
}

// testModelControlled builds a model over an adapter made from d whose
// session is rec, wrapped around the adapter's session.
func testModelControlled(d inproc.Deps, rec *controlRecorder, opts ...Option) Model {
	host, sess := inproc.New(d)
	rec.Session = sess
	all := append([]Option{WithLegacyState(d.State)}, opts...)
	return New(host, rec, all...)
}

// controlsState builds a state the way the TUI tests do.
func controlsState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

// controlsModel builds a sized model over that state whose session records its
// control calls.
func controlsModel(t *testing.T, rec *controlRecorder) Model {
	t.Helper()
	m := testModelControlled(inproc.Deps{State: controlsState(t)}, rec)
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

// TestQueuedInputSteersThroughTheSession pins that Enter while a turn runs
// queues the text through the session rather than the state.
func TestQueuedInputSteersThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	m := controlsModel(t, rec)
	m.busy = true
	m.input.SetValue("next")
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(rec.steers) != 1 || rec.steers[0] != "next" {
		t.Fatalf("steers = %v, want [next]", rec.steers)
	}
	if q := m.state.SteeringQueue(); len(q) != 1 || q[0] != "next" {
		t.Fatalf("queue = %v, want [next]", q)
	}
}

// TestCtrlXClearsTheQueueThroughTheSession pins that Ctrl+X empties the
// steering queue through the session.
func TestCtrlXClearsTheQueueThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	m := controlsModel(t, rec)
	m.busy = true
	m.state.PushSteering("a")
	m.state.PushSteering("b")
	m = sendKey(m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})

	if rec.clears != 1 {
		t.Fatalf("clears = %d, want 1", rec.clears)
	}
	if q := m.state.SteeringQueue(); len(q) != 0 {
		t.Fatalf("queue = %v, want empty", q)
	}
}

// TestADrilledCtrlXCancelsThroughTheSession pins that the drilled Ctrl+X stops
// the drilled view's subagent through the session, naming that ID.
func TestADrilledCtrlXCancelsThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	m := controlsModel(t, rec)
	m.busy = true

	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.state.SetSubagentCancel(view.ID, func() {})
	m.drillIntoSubagent(view.ID)

	m = sendKey(m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})

	if len(rec.cancels) != 1 || rec.cancels[0] != view.ID {
		t.Fatalf("cancels = %v, want [%d]", rec.cancels, view.ID)
	}
}

// TestAFollowUpPopsThroughTheSession pins that the follow-up path pops its
// queued text through the session.
func TestAFollowUpPopsThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	m := controlsModel(t, rec)
	m.runner = &fakeAgentRunner{called: make(chan string, 1)}
	m.state.PushSteering("deliver me after the turn")

	if cmd := m.flushQueuedSteering(); cmd == nil {
		t.Fatal("flushQueuedSteering returned nil with an entry queued")
	}
	if rec.pops != 1 {
		t.Fatalf("pops = %d, want 1", rec.pops)
	}
}

// TestTheNextTurnClearsTheFinishedRunThroughTheSession pins that the next
// turn's clear of a finished run goes through the session.
func TestTheNextTurnClearsTheFinishedRunThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	m := controlsModel(t, rec)
	m.state.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, TaskN: 1, Title: "a3f9e21"})
	m.state.FinishSDDRun(true, time.Now(), nil)

	m.clearFinishedRun()

	if rec.runClears != 1 {
		t.Fatalf("run clears = %d, want 1", rec.runClears)
	}
	if n := len(m.state.RunEvents()); n != 0 {
		t.Fatalf("run events = %d, want 0", n)
	}
}

// TestATurnEndTakesTheLatchThroughTheSession pins that the turn end takes the
// watch-resume latch through the session, and the resumed turn starts.
func TestATurnEndTakesTheLatchThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	m := controlsModel(t, rec)
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m.runner = runner
	m.state.SetWatchResume("build", false)
	m.state.AddMessage(session.RoleAssistant, "I'll wait for the build.", session.ContentTypePlain)

	m2, cmd := m.handleAgentFinished(agentFinishedMsg{})
	// Count before draining: draining the resumed turn's own completion takes
	// the (now empty) latch again, which is the ordinary per-turn-end path.
	takes := rec.resumeTakes
	drainCmds(m2, cmd)

	select {
	case <-runner.called:
	case <-time.After(2 * time.Second):
		t.Fatal("latch did not start a turn")
	}
	if takes != 1 {
		t.Fatalf("resume takes = %d, want 1", takes)
	}
}
