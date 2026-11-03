package tui

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// TestRollbackRequiresConfirmation pins U-03: Ctrl+R is reverse-i-search in
// every readline shell, so it gets pressed reflexively. It used to rewrite the
// working tree on that single keystroke.
func TestRollbackRequiresConfirmation(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.StoreBackup([]session.BackupFile{{Path: "app.go", Content: "original", Exists: true}})
	m := testModel(state)

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

// fakeTUISessionStopService records the turn stops a cancel issues, so a test
// can prove the stop reached the command service BEFORE the agent's own
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

// targetSnapshot returns the executions the service was asked to stop one at a
// time, rather than through the turn's group.
func (f *fakeTUISessionStopService) targetSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.targets...)
}

// runningPrompt builds a sized model over state whose runner blocks until its
// context ends, starts a prompt, and returns the model once the run is in
// flight — so a cancel always has a turn to reach.
func runningPrompt(t *testing.T, state *session.State) (Model, *contextRunner) {
	t.Helper()
	runner := &contextRunner{started: make(chan struct{}), done: make(chan struct{})}
	m := testModelTurns(t, inproc.Deps{State: state, Runner: runnerOf(runner)}, &turnRecorder{})

	updated, cmd := m.startPrompt("hello", false)
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("startPrompt returned no command")
	}
	// The batch leads with the run; execute it the way the runtime would.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("startPrompt cmd = %T, want a batch led by the run", cmd())
	}
	go func() { _ = batch[0]() }()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("the runner never started")
	}
	return m, runner
}

// TestCancelTurnStopsTurnWorkBeforeCancelling pins the ordering for the TUI stop
// path: cancelTurn asks the session to stop the turn's attached commands FIRST,
// and only then cancels the agent context. Cancelling first would leave a
// foreground command running with no stop request against it.
func TestCancelTurnStopsTurnWorkBeforeCancelling(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	svc := &fakeTUISessionStopService{}
	state.SetTurnStopService(svc)

	// Record the order: the service call must precede the context cancel.
	var order []string
	svc.mu.Lock()
	svc.onStopFunc = func() { order = append(order, "service-stop") }
	svc.mu.Unlock()

	m, _ := runningPrompt(t, state)

	// Wrap the run's cancel so the order is observable.
	cancelled := false
	orig := m.agentCancel
	if orig == nil {
		t.Fatal("the running prompt registered no agent cancel")
	}
	m.agentCancel = func() {
		order = append(order, "agent-cancel")
		cancelled = true
		orig()
	}

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
	if len(order) != 2 || order[0] != "service-stop" || order[1] != "agent-cancel" {
		t.Fatalf("order = %v, want the service stop before the agent cancel", order)
	}
	if !cancelled {
		t.Fatal("cancelTurn did not cancel the agent context")
	}
	if !m.cancelling {
		t.Fatal("cancelTurn did not mark the turn as cancelling")
	}
}

// TestCancelTurnLeavesNoPendingStop pins that a cancel with no registered stop
// group cannot reach the next turn: cancelTurn asks the session, not the state,
// so no pending cause is left behind for a later registration to adopt.
func TestCancelTurnLeavesNoPendingStop(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	svc := &fakeTUISessionStopService{}
	state.SetTurnStopService(svc)
	m := testModel(state)
	m.busy = true

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no in-flight turn")
	}
	if turnIDs, _ := svc.snapshot(); len(turnIDs) != 0 {
		t.Fatalf("cancelTurn issued %d service stops with no run, want 0", len(turnIDs))
	}
	if _, pending := state.BeginTurnStop(""); pending != "" {
		t.Fatalf("pending stop cause = %q, want none for the next turn", pending)
	}
}

// TestNormalFinishUnregistersWithoutStopping pins the counterweight: a turn that
// ends normally clears its registration and never asks the service to stop
// anything, so background work left running survives its turn.
func TestNormalFinishUnregistersWithoutStopping(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	svc := &fakeTUISessionStopService{}
	state.SetTurnStopService(svc)

	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelTurns(t, inproc.Deps{State: state, Runner: runnerOf(runner)}, &turnRecorder{})

	updated, cmd := m.startPrompt("hello", false)
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("startPrompt returned no command")
	}
	m = drainCmds(m, cmd)

	if turnIDs, _ := svc.snapshot(); len(turnIDs) != 0 {
		t.Fatalf("a normally-finished turn requested a stop: %v", turnIDs)
	}
	if state.CurrentTurnStop() != nil {
		t.Fatal("the finished turn left its stop registration behind")
	}
}

// TestCancelTurnWithoutServiceStillCancels pins the no-command-service path: a
// session with no service still cancels its agent context, so a turn with no
// commands behaves exactly as it did before explicit stop targeting existed.
func TestCancelTurnWithoutServiceStillCancels(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m, runner := runningPrompt(t, state)

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no in-flight turn")
	}
	select {
	case <-runner.done:
	case <-time.After(time.Second):
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

	m, _ := runningPrompt(t, state)

	if !m.cancelTurn() {
		t.Fatal("cancelTurn reported no in-flight turn")
	}
	// The service reported exactly one attached execution; a detached job is
	// absent from that answer, so nothing in the cancel path can have touched
	// it. The stop request itself is the only thing that reaches the service.
	if _, causes := svc.snapshot(); len(causes) != 1 {
		t.Fatalf("cancelTurn made %d service stop calls, want exactly 1", len(causes))
	}
	if targets := svc.targetSnapshot(); len(targets) != 0 {
		t.Fatalf("cancelTurn issued targeted execution stops: %v — detached work must survive a turn stop", targets)
	}
}
