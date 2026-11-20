package daemon_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/launch"
	"marshal/web/bridge"
)

// hostTunnel serves the harness's bridge again on a second unix socket, as a
// tunnel to a host's daemon presents it (one-daemon D6): the same fleet, the
// same project, and none of it on a filesystem the client is reading.
func hostTunnel(t *testing.T, h *daemonHarness) string {
	t.Helper()
	// A unix socket path is capped near 104 bytes on macOS, so the directory is
	// short rather than t.TempDir()'s.
	dir, err := os.MkdirTemp("", "p4h")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	socket := filepath.Join(dir, "box.sock")
	ln, err := bridge.ListenLocal(socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	srv := &http.Server{Handler: h.srv.Handler}
	go func() {
		_ = srv.Serve(bridge.PeerChecked(ln, os.Getuid(), slog.New(slog.NewTextHandler(io.Discard, nil)))) //nolint:errcheck // the socket closing is how a test ends
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return socket
}

// TestAHostFleetOpensAnAgentAndComesBack is the host's loop: launch.Fleet on a
// tunnel socket, with launch.WithHost naming the machine it leads to, opens an
// agent's session through the tunnel and comes back to the fleet with the
// agent's row selected.
func TestAHostFleetOpensAnAgentAndComesBack(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	agentID, _ := spawnFleetAgent(t, h, ctx)
	agent, ok := h.ws.Agent(agentID)
	if !ok {
		t.Fatalf("agent %s has no record", agentID)
	}
	socket := hostTunnel(t, h)

	var fleetPasses int
	runner := func(_ context.Context, m tea.Model, _ io.Writer) launch.ProgramResult {
		switch model := m.(type) {
		case fleet.Model:
			fleetPasses++
			if fleetPasses == 1 {
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
			return launch.ProgramResult{}
		}
		t.Fatalf("the fleet loop ran a %T", m)
		return launch.ProgramResult{}
	}

	err := launch.Fleet(ctx, io.Discard, socket,
		launch.WithHost("box"),
		launch.WithNotify(false),
		launch.WithProgramRunner(runner),
	)
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	if fleetPasses != 2 {
		t.Fatalf("the fleet ran %d times, want 2", fleetPasses)
	}
}
