package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/activeworkpanel"
)

var ctrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

// clockedModel returns a busy-capable test model whose clock the test steps.
func clockedModel(t *testing.T) (Model, *time.Time) {
	t.Helper()
	m := newTestModel(t)
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	return m, &now
}

// assertNotShutDown fails if the model began shutting the session down.
func assertNotShutDown(t *testing.T, m Model) {
	t.Helper()
	select {
	case <-m.state.Done():
		t.Fatal("session was shut down")
	default:
	}
}

func press(m Model, msg tea.Msg) (Model, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(Model), cmd
}

func busyModel(t *testing.T) (Model, *time.Time, *bool) {
	m, now := clockedModel(t)
	m.busy = true
	cancelled := false
	m.agentCancel = func() { cancelled = true }
	return m, now, &cancelled
}

func TestCtrlCBusyFirstPressOnlyArms(t *testing.T) {
	m, _, cancelled := busyModel(t)
	m, _ = press(m, ctrlC)
	if *cancelled || !m.busy {
		t.Fatal("first Ctrl+C must not cancel the turn")
	}
	assertNotShutDown(t, m)
	if !strings.Contains(m.renderStatusLine(120), "Ctrl+C again to stop the turn") {
		t.Fatalf("status line missing armed text: %q", m.renderStatusLine(120))
	}
}

func TestCtrlCBusySecondPressStops(t *testing.T) {
	m, now, cancelled := busyModel(t)
	m, _ = press(m, ctrlC)
	*now = now.Add(2 * time.Second)
	m, _ = press(m, ctrlC)
	if !*cancelled {
		t.Fatal("second Ctrl+C within the window did not cancel")
	}
	assertNotShutDown(t, m)
	if m.ctrlCArmed() != ctrlCNone {
		t.Fatal("firing must disarm")
	}
}

func TestCtrlCBusyExpiredReArms(t *testing.T) {
	m, now, cancelled := busyModel(t)
	m, _ = press(m, ctrlC)
	*now = now.Add(3100 * time.Millisecond)
	if strings.Contains(m.renderStatusLine(120), "Ctrl+C again") {
		t.Fatal("stale arm must render as disarmed")
	}
	m, _ = press(m, ctrlC)
	if *cancelled {
		t.Fatal("press after the window must re-arm, not cancel")
	}
	if m.ctrlCArmed() != ctrlCStop {
		t.Fatal("expected re-armed for stop")
	}
}

func TestCtrlCIdleNeedsTwoPresses(t *testing.T) {
	m, now := clockedModel(t)
	m, _ = press(m, ctrlC)
	assertNotShutDown(t, m)
	if !strings.Contains(m.renderStatusLine(120), "Ctrl+C again to quit") {
		t.Fatal("status line missing quit arm text")
	}
	*now = now.Add(time.Second)
	if _, cmd := press(m, ctrlC); cmd == nil {
		t.Fatal("second idle Ctrl+C within the window did not quit")
	}
}

func TestCtrlCTurnEndingBetweenPressesNeverQuits(t *testing.T) {
	m, _, _ := busyModel(t)
	m, _ = press(m, ctrlC)
	m.busy = false
	m.agentCancel = nil
	m, _ = press(m, ctrlC)
	assertNotShutDown(t, m)
	if m.ctrlCArmed() != ctrlCQuit {
		t.Fatal("expected re-arm for quit")
	}
}

func TestCtrlCOtherKeyDisarms(t *testing.T) {
	m, _, cancelled := busyModel(t)
	m, _ = press(m, ctrlC)
	m, _ = press(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.ctrlCArmed() != ctrlCNone {
		t.Fatal("unrelated key must disarm")
	}
	m, _ = press(m, ctrlC)
	if *cancelled {
		t.Fatal("Ctrl+C after a disarming key must re-arm, not fire")
	}
}

func TestCtrlCArmSurvivesBackgroundMessages(t *testing.T) {
	m, _, cancelled := busyModel(t)
	m, _ = press(m, ctrlC)
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = press(m, ctrlC)
	if !*cancelled {
		t.Fatal("non-key messages must not disarm")
	}
}

func TestEscBusyNothingOpenDoesNotCancel(t *testing.T) {
	m, _, cancelled := busyModel(t)
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if *cancelled || !m.busy {
		t.Fatal("Esc must not cancel a busy turn")
	}
}

func TestEscBusyDismissesNotice(t *testing.T) {
	m, _, cancelled := busyModel(t)
	m.state.SetNotice(session.Notice{Severity: session.SeverityWarn, Message: "heads up"})
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if _, ok := m.state.Notice(); ok {
		t.Fatal("Esc should dismiss the notice while busy")
	}
	if *cancelled {
		t.Fatal("dismissing a notice must not cancel")
	}
}

func TestEscBusyDismissesSuggestion(t *testing.T) {
	m, _, cancelled := busyModel(t)
	m.suggestion = "yes"
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.suggestionDismissed || *cancelled {
		t.Fatal("busy Esc should dismiss the suggestion without cancelling")
	}
}

func TestEscBusyDrilledPopsDrillWithoutCancel(t *testing.T) {
	m, _, cancelled := busyModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore repo", child)
	m.drillIntoSubagent(view)
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if len(m.viewStack) != 0 {
		t.Fatal("Esc should pop the drill")
	}
	if *cancelled {
		t.Fatal("popping a drill must not cancel the turn")
	}
}

// A turn that ignores cancellation stays busy; a second double press must
// then quit rather than "stop" again.
func TestCtrlCQuitsWhenCancelledTurnIsStillWindingDown(t *testing.T) {
	m, _, cancelled := busyModel(t)
	m, _ = press(m, ctrlC)
	m, _ = press(m, ctrlC)
	if !*cancelled || !m.cancelling || !m.busy {
		t.Fatal("setup: expected a cancelled turn that is still busy")
	}
	m, _ = press(m, ctrlC)
	if m.ctrlCArmed() != ctrlCQuit {
		t.Fatalf("armed = %v, want quit while the cancelled turn winds down", m.ctrlCArmed())
	}
	if _, cmd := press(m, ctrlC); cmd == nil {
		t.Fatal("second press must quit")
	}
}

func TestCtrlCArmReturnsRepaintTick(t *testing.T) {
	m, _ := clockedModel(t)
	if _, cmd := press(m, ctrlC); cmd == nil {
		t.Fatal("arming must schedule a repaint for when the window lapses")
	}
}

// ── Task 10: Ctrl+C during an ending ────────────────────────────────────

// TestCtrlCQuitsOnlyAfterTheWarningIsAnswered pins the interaction between the
// double-press quit and the active-work warning: the second press opens the
// warning instead of exiting, and the program does not quit until the user has
// answered it.
func TestCtrlCQuitsOnlyAfterTheWarningIsAnswered(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(1000, 0), session.Persistence{})
	state.Config.Postmortem.OnExit = "never"
	ctrl := &endTestController{work: workSnapshot("job-1")}
	m := New(state, WithEndController(ctrl))
	m.resize(80, 24)
	m.now = func() time.Time { return time.Unix(1000, 0) }

	m, _ = press(m, ctrlC)
	m, cmd := press(m, ctrlC)

	if cmd != nil {
		t.Fatalf("the firing press returned a cmd (%T), want nil while the warning is up", cmd)
	}
	if _, ok := m.dock.Panel().(*activeworkpanel.Panel); !ok {
		t.Fatalf("dock panel = %T, want the warning", m.dock.Panel())
	}
	assertNotShutDown(t, m)

	// Ctrl+C while the warning is up must not exit either: it is the same
	// ending, which has not been decided.
	m, cmd = press(m, ctrlC)
	if cmd != nil {
		t.Fatalf("Ctrl+C with the warning up returned a cmd (%T), want nil", cmd)
	}
	assertNotShutDown(t, m)
	if len(ctrl.accepted) != 0 {
		t.Fatal("Ctrl+C accepted the ending without a decision")
	}
}

// TestCtrlCStopsTheRightThingBeforeTheWarning pins that a busy turn is still
// stopped by the first firing Ctrl+C, and only the second firing reaches the
// warning: the ending path must not swallow the turn stop.
func TestCtrlCStopsTheRightThingBeforeTheWarning(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(1000, 0), session.Persistence{})
	state.Config.Postmortem.OnExit = "never"
	ctrl := &endTestController{work: workSnapshot("job-1")}
	m := New(state, WithEndController(ctrl))
	m.resize(80, 24)
	m.now = func() time.Time { return time.Unix(1000, 0) }
	cancelled := false
	m.busy = true
	m.agentCancel = func() { cancelled = true }

	m, _ = press(m, ctrlC)
	m, _ = press(m, ctrlC)

	if !cancelled {
		t.Fatal("the firing Ctrl+C did not stop the busy turn")
	}
	if m.dock.IsOpen() {
		t.Fatal("a busy Ctrl+C opened the ending warning instead of stopping the turn")
	}
	if len(ctrl.accepted) != 0 {
		t.Fatal("a busy Ctrl+C accepted an ending")
	}
	assertNotShutDown(t, m)
}
