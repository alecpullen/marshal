package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// TestCtrlCInterruptsBeforeQuitting pins U-02: Ctrl+C is the universal "stop
// what you're doing" reflex, but it used to quit outright on the first press —
// so the moment a user most wanted to interrupt a slow turn was exactly the
// moment they lost the session.
func TestCtrlCInterruptsBeforeQuitting(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.busy = true
	cancelled := false
	m.agentCancel = func() { cancelled = true }

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(Model)

	if !cancelled {
		t.Error("first Ctrl+C did not cancel the in-flight turn")
	}
	if cmd != nil {
		t.Error("first Ctrl+C returned a command; it must not quit")
	}
	if !m.interruptArmed {
		t.Error("first Ctrl+C did not arm the quit")
	}

	// The user must be told what a second press does.
	var found bool
	for _, msg := range state.Messages() {
		if strings.Contains(msg.Content, "Ctrl+C again") {
			found = true
		}
	}
	if !found {
		t.Error("no notice explaining that a second Ctrl+C quits")
	}
}

// TestCtrlCArmDisarmsOnOtherKeys pins that the armed quit cannot outlive the
// keystroke that set it — otherwise a Ctrl+C minutes later, mentally separate
// from the interrupt, would quit unexpectedly.
func TestCtrlCArmDisarmsOnOtherKeys(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.busy = true
	m.agentCancel = func() {}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.interruptArmed {
		t.Fatal("expected armed after first Ctrl+C")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'x'})
	m = updated.(Model)
	if m.interruptArmed {
		t.Error("armed quit survived an unrelated keypress")
	}
}

// TestCtrlCQuitsImmediatelyWhenIdle pins that the interrupt step only applies
// while a turn is running. With nothing to interrupt, Ctrl+C should quit on the
// first press as it always has.
func TestCtrlCQuitsImmediatelyWhenIdle(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.busy = false

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Error("idle Ctrl+C did not quit")
	}
}

// TestInterruptArmedClearedByNonKeyMessage pins that a stale interruptArmed
// flag is cleared by any non-keypress message (e.g. WindowSizeMsg), so a
// Ctrl+C pressed after unrelated messages interrupts again rather than
// quitting.
func TestInterruptArmedClearedByNonKeyMessage(t *testing.T) {
	m := newTestModel(t)
	m.busy = true

	// First Ctrl+C interrupts.
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.interruptArmed {
		t.Fatal("interruptArmed should be true after first Ctrl+C")
	}

	// A non-keypress message (e.g. WindowSizeMsg) clears interruptArmed.
	mm, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)
	if m.interruptArmed {
		t.Fatal("interruptArmed should be cleared by non-keypress message")
	}

	// Second Ctrl+C should interrupt again, not quit.
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = mm.(Model)
	if !m.interruptArmed {
		t.Fatal("interruptArmed should be true after Ctrl+C with busy state")
	}
}

// TestRollbackRequiresConfirmation pins U-03: Ctrl+R is reverse-i-search in
// every readline shell, so it gets pressed reflexively. It used to rewrite the
// working tree on that single keystroke.
func TestRollbackRequiresConfirmation(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.StoreBackup([]session.BackupFile{{Path: "app.go", Content: "original", Exists: true}})
	m := New(state)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !state.HasBackup() {
		t.Fatal("first Ctrl+R rolled back without confirmation")
	}

	// An unrelated key must cancel the armed rollback.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'x'})
	m = updated.(Model)
	if m.rollbackArmed {
		t.Error("armed rollback survived an unrelated keypress")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !state.HasBackup() {
		t.Error("rollback fired on a re-armed first press after being cancelled")
	}
}
