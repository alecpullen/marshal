package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
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
	m, cmd := press(m, ctrlC)
	if *cancelled || !m.busy {
		t.Fatal("first Ctrl+C must not cancel the turn")
	}
	if cmd != nil {
		t.Fatal("first Ctrl+C must not quit")
	}
	if !strings.Contains(m.renderStatusLine(120), "Ctrl+C again to stop the turn") {
		t.Fatalf("status line missing armed text: %q", m.renderStatusLine(120))
	}
}

func TestCtrlCBusySecondPressStops(t *testing.T) {
	m, now, cancelled := busyModel(t)
	m, _ = press(m, ctrlC)
	*now = now.Add(2 * time.Second)
	m, cmd := press(m, ctrlC)
	if !*cancelled {
		t.Fatal("second Ctrl+C within the window did not cancel")
	}
	if cmd != nil {
		t.Fatal("stopping a turn must not quit")
	}
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
	m, cmd := press(m, ctrlC)
	if cmd != nil {
		t.Fatal("first idle Ctrl+C must not quit")
	}
	if !strings.Contains(m.renderStatusLine(120), "Ctrl+C again to quit") {
		t.Fatal("status line missing quit arm text")
	}
	*now = now.Add(time.Second)
	if _, cmd = press(m, ctrlC); cmd == nil {
		t.Fatal("second idle Ctrl+C within the window did not quit")
	}
}

func TestCtrlCTurnEndingBetweenPressesNeverQuits(t *testing.T) {
	m, _, _ := busyModel(t)
	m, _ = press(m, ctrlC)
	m.busy = false
	m.agentCancel = nil
	m, cmd := press(m, ctrlC)
	if cmd != nil {
		t.Fatal("a stop arm must not turn into a quit when the turn ends")
	}
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
