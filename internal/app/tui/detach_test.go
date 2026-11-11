package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// detachableModel is clockedModel with detaching on, as launch builds the model
// in daemon mode, over a command registry so /detach and /exit resolve.
func detachableModel(t *testing.T) (Model, *time.Time) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(1000, 0), session.Persistence{SessionID: unnamedSessionID})
	m := testModelWith(t, service.Deps{State: state, Commands: setupCmdReg(t)}, WithDetachOnQuit())
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	return m, &now
}

func TestTheQuitKeyDetachesAnIdleDaemonSession(t *testing.T) {
	m, now := detachableModel(t)
	wantID := m.sess.ID()

	m, _ = press(m, ctrlC)
	if m.detached {
		t.Fatal("the first press must only arm")
	}
	assertNotShutDown(t, m)

	*now = now.Add(time.Second)
	m, cmd := press(m, ctrlC)
	if cmd == nil {
		t.Fatal("the second press returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("detaching must quit the program")
	}
	if got := m.Detached(); got != wantID {
		t.Fatalf("Detached() = %q, want the session %q", got, wantID)
	}
	// A detach leaves the session running: the connection's close ends the
	// client's call, not the session.
	assertNotShutDown(t, m)
	if m.endStage != endIdle {
		t.Fatalf("a detach must not open the end flow (stage %v)", m.endStage)
	}
}

func TestTheQuitKeyStillStopsARunFirst(t *testing.T) {
	m, now := detachableModel(t)
	m.busy = true
	cancelled := false
	m.agentCancel = func() { cancelled = true }

	m, _ = press(m, ctrlC)
	if m.ctrlCArmed() != ctrlCStop {
		t.Fatal("a run in flight must arm the stop, not the detach")
	}
	*now = now.Add(time.Second)
	m, _ = press(m, ctrlC)
	if !cancelled {
		t.Fatal("the second press must stop the run")
	}
	if m.detached || m.Detached() != "" {
		t.Fatal("stopping a run must not detach")
	}
	assertNotShutDown(t, m)
}

func TestDetachLeavesARunGoing(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(1000, 0), session.Persistence{SessionID: unnamedSessionID})
	runner := &blockingAgentRunner{block: make(chan struct{}), started: make(chan struct{})}
	m := testModelWith(t, service.Deps{State: state, Commands: setupCmdReg(t), Runner: runnerOf(runner)}, WithDetachOnQuit())

	go func() {
		_, _ = m.sess.Prompt(context.Background(), acpwire.PromptParams{Text: "long"})
	}()
	<-runner.started
	waitRunStart(t, m)

	updated, cmd := m.dispatchCommand("/detach")
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("/detach returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("/detach must quit the program")
	}
	if m.Detached() == "" {
		t.Fatal("/detach did not record the detached session")
	}
	if m.sess.Status().Run == nil {
		t.Fatal("the run must still be in flight after a detach")
	}

	close(runner.block)
	waitRunClear(t, m)
}

func TestDetachIsRefusedInEmbeddedMode(t *testing.T) {
	m, _ := clockedModel(t)

	updated, cmd := m.dispatchCommand("/detach")
	m = asModel(t, updated)
	if cmd != nil {
		t.Fatal("/detach in embedded mode returned a command")
	}
	if m.detached || m.Detached() != "" {
		t.Fatal("/detach in embedded mode must not detach")
	}
	msgs := stateOf(m).Messages()
	if n := countContaining(msgs, "Only a daemon session can be detached"); n != 1 {
		t.Fatalf("got %d refusal notes, want exactly one", n)
	}
}

func TestExitStillEndsADaemonSession(t *testing.T) {
	m, _ := detachableModel(t)

	updated, _ := m.dispatchCommand("/exit")
	m = asModel(t, updated)
	if m.endStage == endIdle {
		t.Fatal("/exit must open the end flow in daemon mode too")
	}
	if m.detached || m.Detached() != "" {
		t.Fatal("/exit must end the session, not detach it")
	}
}

func TestTheArmedHintSaysDetach(t *testing.T) {
	m, _ := detachableModel(t)
	m, _ = press(m, ctrlC)
	if line := m.renderStatusLine(120); !strings.Contains(line, "Ctrl+C again to detach") {
		t.Fatalf("status line %q missing the detach arm", line)
	}

	embedded, _ := clockedModel(t)
	embedded, _ = press(embedded, ctrlC)
	if line := embedded.renderStatusLine(120); !strings.Contains(line, "Ctrl+C again to quit") {
		t.Fatalf("status line %q missing the quit arm", line)
	}
}

// waitRunStart polls until the session reports a run in flight.
func waitRunStart(t *testing.T, m Model) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for m.sess.Status().Run == nil {
		if time.Now().After(deadline) {
			t.Fatal("the run never started")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
