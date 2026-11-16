package daemon_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/launch"
)

// TestTheFleetOpensAnAgentAndLeavesItRunning is W4 against a real bridge: the
// fleet screen opens a spawned agent's session, quitting that session screen
// leaves the agent's run in flight, and the fleet comes back on the agent's
// row.
func TestTheFleetOpensAnAgentAndLeavesItRunning(t *testing.T) {
	// The agent's one turn holds on the gate, so its run is still in flight
	// when the fleet loop returns.
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{{Text: "the agent's reply", gate: gate}})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	agentID, _ := spawnFleetAgent(t, h, ctx)
	agent, ok := h.ws.Agent(agentID)
	if !ok {
		t.Fatalf("agent %s has no record", agentID)
	}
	// A decoy row lands before the agent's, so the fleet's default cursor sits
	// somewhere other than the agent: "the cursor came back to the agent's row"
	// is then told apart from "the cursor never moved off row one".
	if decoyID, _ := spawnFleetAgent(t, h, ctx); decoyID == agentID {
		t.Fatalf("the decoy spawned as %q, the agent's own id", agentID)
	}

	var (
		fleetPasses int
		viewer      *attachment
		runID       string
		promptErr   = make(chan error, 1)
	)

	runner := func(_ context.Context, m tea.Model, _ io.Writer) launch.ProgramResult {
		switch model := m.(type) {
		case fleet.Model:
			fleetPasses++
			if fleetPasses == 1 {
				// The decoy was spawned second, and lands on the earlier row
				// through the inbox's ordering, so a first pass already on
				// the agent's row means the roster put no row before it and
				// the restored cursor below would prove nothing.
				if got := fleetSelected(t, model); got == agentID {
					t.Fatalf("the first fleet pass selected the agent %q already: the roster has no row before it", agentID)
				}
				// The operator opens the agent's row: this is the target the
				// screen's key produces for it.
				return launch.ProgramResult{Open: fleet.Target{
					AgentID:   agentID,
					AgentName: agent.Name,
					Root:      h.root,
				}}
			}
			// Back on the fleet, the agent is the selected row.
			if got := fleetSelected(t, model); got != agentID {
				t.Errorf("the fleet came back with %q selected, want the agent %q", got, agentID)
			}
			return launch.ProgramResult{}

		case tui.Model:
			if got := model.AgentSession(); got != agentID {
				t.Errorf("the session screen is agent %q's, want %q", got, agentID)
			}
			// A second viewer starts the agent's turn: the reply is gated, so
			// the run outlives the session screen below.
			viewer = attachAgent(t, h, agentID)
			t.Cleanup(viewer.close)
			promptCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			t.Cleanup(cancel)
			opened := viewer
			go func() {
				_, err := opened.session.Prompt(promptCtx, acpwire.PromptParams{Text: "work while the fleet watches"})
				promptErr <- err
			}()
			waitFor(t, func() error {
				run := viewer.session.Status().Run
				if run == nil {
					return errNoRun
				}
				runID = run.ID
				return nil
			})
			// The quit: the run stays where it is.
			return launch.ProgramResult{}
		}
		t.Fatalf("the fleet loop ran a %T", m)
		return launch.ProgramResult{}
	}

	if err := launch.Fleet(ctx, io.Discard, h.socket, launch.WithProgramRunner(runner)); err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	if fleetPasses != 2 {
		t.Fatalf("the fleet ran %d times, want 2", fleetPasses)
	}

	// The agent's run is still in flight, as the second viewer shows. The
	// fleet's own `running` is not checked: a viewer's turn does not reach the
	// bridge's connection (F5), so the bridge lists the agent as it was.
	if viewer == nil {
		t.Fatal("the session screen never opened a viewer")
	}
	run := viewer.session.Status().Run
	if run == nil {
		t.Fatal("the agent's run ended with the fleet loop, want it in flight")
	}
	if run.ID != runID {
		t.Fatalf("the run in flight is %q, want %q", run.ID, runID)
	}

	// Opening the gate serves the reply, which ends the run as done.
	close(gate)
	waitOutcome(t, viewer, runID, acpwire.RunDone)
	waitPrompt(t, promptErr)
}

// errNoRun reports a session with no run in flight yet.
var errNoRun = errors.New("the agent's run has not started")

// fleetSelected is the row the fleet screen's cursor is on. The start
// selection lands the first time the screen lays its rows out, which a filter
// opened and closed does without moving the cursor or opening anything.
func fleetSelected(t *testing.T, m fleet.Model) string {
	t.Helper()
	for _, key := range []tea.KeyPressMsg{
		{Code: '/', Text: "/"}, // open the filter
		{Code: tea.KeyEsc},     // clear it, and lay the rows out again
	} {
		next, _ := m.Update(key)
		got, ok := next.(fleet.Model)
		if !ok {
			t.Fatalf("the fleet model updated to %T, want fleet.Model", next)
		}
		m = got
	}
	return m.Selected()
}
