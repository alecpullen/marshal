package tui

import (
	"context"
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

// Unwrap exposes the adapter this recorder wraps, so stateOf reaches the
// state behind it.
func (r *controlRecorder) Unwrap() acpclient.Session { return r.Session }

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

func (r *controlRecorder) SetInteraction(ctx context.Context, patch acpwire.InteractionPatch) (acpwire.Interaction, error) {
	r.interactions = append(r.interactions, patch)
	return r.Session.SetInteraction(ctx, patch)
}

func (r *controlRecorder) SetEffort(ctx context.Context, level string) (string, error) {
	r.efforts = append(r.efforts, level)
	return r.Session.SetEffort(ctx, level)
}

func (r *controlRecorder) SetSudoUnlock(ctx context.Context, on bool) error {
	r.unlocks = append(r.unlocks, on)
	return r.Session.SetSudoUnlock(ctx, on)
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

// controlsDispatchModel is controlsModel over an adapter made from d, with the
// real command registry so /mode, /effort and /system dispatch.
func controlsDispatchModel(t *testing.T, d inproc.Deps, rec *controlRecorder) Model {
	t.Helper()
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	d.Commands = reg
	m := testModelControlled(d, rec)
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
	if q := stateOf(m).SteeringQueue(); len(q) != 1 || q[0] != "next" {
		t.Fatalf("queue = %v, want [next]", q)
	}
}

// TestCtrlXClearsTheQueueThroughTheSession pins that Ctrl+X empties the
// steering queue through the session.
func TestCtrlXClearsTheQueueThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	m := controlsModel(t, rec)
	m.busy = true
	stateOf(m).PushSteering("a")
	stateOf(m).PushSteering("b")
	m = sendKey(m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})

	if rec.clears != 1 {
		t.Fatalf("clears = %d, want 1", rec.clears)
	}
	if q := stateOf(m).SteeringQueue(); len(q) != 0 {
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
	view := stateOf(m).RegisterSubagent("explore repo", child)
	stateOf(m).SetSubagentCancel(view.ID, func() {})
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
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := controlsDispatchModel(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)
	stateOf(m).PushSteering("deliver me after the turn")

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
	stateOf(m).AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, TaskN: 1, Title: "a3f9e21"})
	stateOf(m).FinishSDDRun(true, time.Now(), nil)

	m.clearFinishedRun()

	if rec.runClears != 1 {
		t.Fatalf("run clears = %d, want 1", rec.runClears)
	}
	if n := len(stateOf(m).RunEvents()); n != 0 {
		t.Fatalf("run events = %d, want 0", n)
	}
}

// TestATurnEndTakesTheLatchThroughTheSession pins that the turn end takes the
// watch-resume latch through the session, and the resumed turn starts.
func TestATurnEndTakesTheLatchThroughTheSession(t *testing.T) {
	rec := &controlRecorder{}
	runner := &fakeAgentRunner{called: make(chan string, 4)}
	m := controlsDispatchModel(t, inproc.Deps{State: controlsState(t), Runner: runnerOf(runner)}, rec)
	stateOf(m).SetWatchResume("build", false)
	stateOf(m).AddMessage(session.RoleAssistant, "I'll wait for the build.", session.ContentTypePlain)

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

// TestModeChangesGoThroughTheSession pins that /act submits its patch through
// the session and reports the committed mode.
func TestModeChangesGoThroughTheSession(t *testing.T) {
	state := controlsState(t)
	rec := &controlRecorder{}
	m := controlsDispatchModel(t, inproc.Deps{State: state, SetInteraction: state.ApplyInteraction}, rec)

	updated, _ := m.dispatchCommand("/act")
	m = asModel(t, updated)

	if len(rec.interactions) != 1 {
		t.Fatalf("patches = %+v, want one", rec.interactions)
	}
	patch := rec.interactions[0]
	if patch.Mode == nil || *patch.Mode != "act" {
		t.Fatalf("patch = %+v, want mode act", patch)
	}
	if got := m.sess.Status().Interaction.Mode; got != "act" {
		t.Fatalf("mode = %q, want act", got)
	}
}

// TestEffortGoesThroughTheSession pins that /effort hands the level to the
// session's setter and renders when the runtime says it applies.
func TestEffortGoesThroughTheSession(t *testing.T) {
	state := controlsState(t)
	rec := &controlRecorder{}
	m := controlsDispatchModel(t, inproc.Deps{
		State: state,
		SetEffort: func(string) (string, error) {
			return session.EffortAppliesNextTurn, nil
		},
	}, rec)

	updated, _ := m.dispatchCommand("/effort low")
	m = asModel(t, updated)

	if len(rec.efforts) != 1 || rec.efforts[0] != "low" {
		t.Fatalf("efforts = %v, want [low]", rec.efforts)
	}
	if note := lastMessageContent(stateOf(m).Messages()); !strings.Contains(note, "Effort set to low (from the next turn).") {
		t.Fatalf("transcript note = %q, want the confirmation", note)
	}
}

// TestSystemUnlockTogglesThroughTheSession pins that /system unlock toggles
// sudo's grace through the session and reaches Status.
func TestSystemUnlockTogglesThroughTheSession(t *testing.T) {
	state := controlsState(t)
	rec := &controlRecorder{}
	m := controlsDispatchModel(t, inproc.Deps{State: state}, rec)

	updated, _ := m.dispatchCommand("/system unlock")
	m = asModel(t, updated)

	if len(rec.unlocks) != 1 || !rec.unlocks[0] {
		t.Fatalf("unlocks = %v, want [true]", rec.unlocks)
	}
	if !m.sess.Status().SudoUnlock {
		t.Fatal("Status().SudoUnlock = false, want true")
	}
	if note := lastMessageContent(stateOf(m).Messages()); !strings.HasPrefix(note, "Privileged unlock enabled") {
		t.Fatalf("transcript note = %q, want the enabled note", note)
	}

	updated, _ = m.dispatchCommand("/system unlock")
	m = asModel(t, updated)

	if len(rec.unlocks) != 2 || rec.unlocks[1] {
		t.Fatalf("unlocks = %v, want [true false]", rec.unlocks)
	}
	if m.sess.Status().SudoUnlock {
		t.Fatal("Status().SudoUnlock = true, want false")
	}
}
