package daemon_test

// The session screen's needs-you count (W10) against a bridge that is really
// there: two fleet agents on the project child, a real fleet client on the
// daemon's socket, and a model showing the first agent's session. The second
// agent parks on an approval, which is what the count is for, and the bridge's
// answer to it is what takes the segment away.
//
// Nothing here is faked: the queue the screen counts is the bridge's own, read
// over the stream, and the frame is the one Bubble Tea would paint.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui"
	"marshal/internal/fleetclient"
	"marshal/web/bridge"
	"marshal/web/bridge/contract"
)

// spawnNamedAgent starts a fleet agent on the harness's project and returns its
// record: the agent's ID is what the screen names, its session ID is what the
// harness prompts. A name rather than spawnFleetAgent's fixed one, so two
// agents on the one project read distinctly in a failure.
func spawnNamedAgent(t *testing.T, h *daemonHarness, ctx context.Context, name string) bridge.Agent {
	t.Helper()
	agentID, err := h.fleet.Spawn(ctx, h.root, bridge.SpawnOptions{Name: name})
	if err != nil {
		t.Fatalf("Spawn %q: %v", name, err)
	}
	agent, ok := h.ws.Agent(agentID)
	if !ok || agent.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	return agent
}

// sessionFrame is the model's screen at a usual terminal size, stripped of its
// styling: what the user reads off the status line.
func sessionFrame(m tui.Model) string {
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return ansi.Strip(updated.View().Content)
}

// TestTheCountFollowsARealFleet: the screen counts another agent parking on an
// approval, and stops counting once the bridge answers it.
func TestTheCountFollowsARealFleet(t *testing.T) {
	// The parking agent's one prompted turn asks for the approval first and
	// answers the prompt after it: the rise and the fall of the count.
	fixture := newProviderFixture(t, []scriptedTurn{
		{ToolName: "shell.run", ToolArgs: mustJSON(t, map[string]string{"command": approvalCommand})},
		{Text: "done"},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	// The project's confirmation rule is what makes the shell call ask, and the
	// trust record is what makes the project's own config apply. Both land
	// before the child starts, which the first spawn does.
	writeApprovalPolicy(t, h.root)
	trustProject(t, h.root)
	ctx := callCtx(t)

	// The real client on the daemon's socket: the fleet state the screen counts.
	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(ctx)
	defer client.Close()

	// Two agents on the project child: the one whose session the operator has
	// open, and the one that parks.
	watched := spawnNamedAgent(t, h, ctx, "watched agent")
	parking := spawnNamedAgent(t, h, ctx, "parking agent")

	// The screen: the first agent's session, the real client, and no watch (a
	// plain `marshal` counts without notifying).
	viewer := attachAgent(t, h, watched.ID)
	defer viewer.close()
	m := tui.New(viewer.host, viewer.session,
		tui.WithFleet(client, nil),
		tui.WithAgentSession(watched.ID, watched.Name))

	// 1. Nothing waits yet, so the line carries no segment at all.
	if frame := sessionFrame(m); strings.Contains(frame, "needs you") {
		t.Fatalf("the screen counts something before any agent parked:\n%s", frame)
	}

	// 2. The second agent's turn parks on the approval.
	promptErr := promptAgent(t, h, ctx, parking.SessionID, "run the command")
	pending := waitBridgeApproval(t, h, parking.ID, 30*time.Second)

	// 3. The status line counts it.
	waitFor(t, func() error {
		if frame := sessionFrame(m); !strings.Contains(frame, "needs you 1") {
			return fmt.Errorf("the status line does not count the parked agent:\n%s", frame)
		}
		return nil
	})

	// 4. The answer goes the way the fleet screen's does: through the bridge,
	// over the same client this screen counts with.
	if err := client.AnswerPermission(ctx, pending.ID, contract.Decision{Approved: true}); err != nil {
		t.Fatalf("answer the approval through the bridge: %v", err)
	}

	// 5. The segment goes, and the turn it unblocked finishes.
	waitFor(t, func() error {
		if frame := sessionFrame(m); strings.Contains(frame, "needs you") {
			return fmt.Errorf("the status line still counts after the answer:\n%s", frame)
		}
		return nil
	})
	waitPrompt(t, promptErr)
	if got := fixture.Completions(); got != 2 {
		t.Fatalf("the provider served %d completions, want 2 (the tool call and the reply)", got)
	}
}
