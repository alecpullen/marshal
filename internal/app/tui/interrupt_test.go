package tui

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

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

// fakeTUISessionStopService records the turn stops a TUI cancel issues, so a
// test can prove the stop reached the command service BEFORE the agent's own
// context was cancelled.
type fakeTUISessionStopService struct {
	mu         sync.Mutex
	turnIDs    []string
	causes     []string
	targets    []string
	onStopFunc func()
}

func (f *fakeTUISessionStopService) StopAttached(turnID, cause string) ([]string, error) {
	f.mu.Lock()
	f.turnIDs = append(f.turnIDs, turnID)
	f.causes = append(f.causes, cause)
	fn := f.onStopFunc
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	return []string{"job-1"}, nil
}

func (f *fakeTUISessionStopService) RequestStop(executionID, cause string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, executionID)
	return nil
}

func (f *fakeTUISessionStopService) snapshot() (turnIDs, causes []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.turnIDs...), append([]string(nil), f.causes...)
}

// TestCancelTurnStopsTurnWorkBeforeCancelling pins the ordering for the TUI stop
// path: cancelTurn asks the command service to stop the turn's attached
// commands FIRST, and only then cancels the agent context. Cancelling first
// would leave a foreground command running with no stop request against it.
func TestCancelTurnStopsTurnWorkBeforeCancelling(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	svc := &fakeTUISessionStopService{}
	state.SetTurnStopService(svc)

	// Record the order: the service call must precede the context cancel.
	var order []string
	svc.mu.Lock()
	svc.onStopFunc = func() { order = append(order, "service-stop") }
	svc.mu.Unlock()

	m := New(state)
	m.busy = true
	m.agentCancel = func() {
		order = append(order, "agent-cancel")
	}
	m.beginTurnStopRegistration()

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no in-flight turn")
	}
	turnIDs, causes := svc.snapshot()
	if len(turnIDs) != 1 {
		t.Fatalf("cancelTurn issued %d service stops, want exactly 1", len(turnIDs))
	}
	if len(causes) != 1 || causes[0] != "turn stopped" {
		t.Fatalf("stop cause = %v, want %q", causes, "turn stopped")
	}
	if len(order) != 2 || order[0] != "service-stop" {
		t.Fatalf("order = %v, want the service stop before the agent cancel", order)
	}
	if !m.cancelling {
		t.Fatal("cancelTurn did not mark the turn as cancelling")
	}
}

// TestNormalFinishUnregistersWithoutStopping pins the counterweight: a turn that
// ends normally clears its registration and never asks the service to stop
// anything, so background work left running survives its turn.
func TestNormalFinishUnregistersWithoutStopping(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	svc := &fakeTUISessionStopService{}
	state.SetTurnStopService(svc)

	m := New(state)
	m.busy = true
	m.beginTurnStopRegistration()
	if state.CurrentTurnStop() == nil {
		t.Fatal("no stop registration was created for the turn")
	}

	updated, _ := m.Update(agentFinishedMsg{err: nil})
	m = updated.(Model)

	if turnIDs, _ := svc.snapshot(); len(turnIDs) != 0 {
		t.Fatalf("a normally-finished turn requested a stop: %v", turnIDs)
	}
	if state.CurrentTurnStop() != nil {
		t.Fatal("the finished turn left its stop registration behind")
	}
}

// TestTurnStopSurvivesReplacementRelease pins the callback-replacement race at
// the TUI boundary: an older turn finishing must not unregister the newer
// turn's stop target.
func TestTurnStopSurvivesReplacementRelease(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	svc := &fakeTUISessionStopService{}
	state.SetTurnStopService(svc)

	m := New(state)
	firstRelease := m.beginTurnStopRegistration()
	first := state.CurrentTurnStop()

	// A newer turn replaces the registration.
	secondRelease := m.beginTurnStopRegistration()
	second := state.CurrentTurnStop()
	if first == second {
		t.Fatal("the newer turn reused the older turn's registration")
	}

	// The older turn finishing must leave the newer registration alone.
	firstRelease()
	if state.CurrentTurnStop() != second {
		t.Fatal("the older turn's release unregistered the newer turn")
	}
	secondRelease()
	if state.CurrentTurnStop() != nil {
		t.Fatal("the current registration survived its own release")
	}
}

// TestCancelTurnWithoutServiceStillCancels pins the no-command-service path: a
// session with no service still cancels its agent context, so a turn with no
// commands behaves exactly as it did before explicit stop targeting existed.
func TestCancelTurnWithoutServiceStillCancels(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.busy = true
	cancelled := false
	m.agentCancel = func() { cancelled = true }
	m.beginTurnStopRegistration()

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no in-flight turn")
	}
	if !cancelled {
		t.Fatal("the agent context was not cancelled when no command service was wired")
	}
}

// TestCancelTurnLeavesDetachedWorkRunning pins that a turn stop targets only the
// work ATTACHED to that turn: a background job that already detached — or one
// belonging to another turn — is still alive after the stop, and the TUI's
// projected job list keeps showing it.
func TestCancelTurnLeavesDetachedWorkRunning(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	svc := &fakeTUISessionStopService{}
	state.SetTurnStopService(svc)

	m := New(state)
	m.busy = true
	m.agentCancel = func() {}
	m.beginTurnStopRegistration()

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no in-flight turn")
	}
	// The service reported exactly one attached execution; a detached job is
	// absent from that answer, so nothing in the cancel path can have touched
	// it. The stop request itself is the only thing that reaches the service.
	if _, causes := svc.snapshot(); len(causes) != 1 {
		t.Fatalf("cancelTurn made %d service stop calls, want exactly 1", len(causes))
	}
	if len(svc.targets) != 0 {
		t.Fatalf("cancelTurn issued targeted execution stops: %v — detached work must survive a turn stop", svc.targets)
	}
}
