package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/daemon"
)

// attachAgent opens agentID's session as a viewer: the daemon's agent attach,
// the client's connection over it, and the session it attached to, as
// (*daemonHarness).attach does for a project.
func attachAgent(t *testing.T, h *daemonHarness, agentID string) *attachment {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := h.client.AttachAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("attach to agent %q: %v", agentID, err)
	}
	host, err := rpc.Dial(ctx, rpc.NewStream(conn.Conn, conn.Conn, rpc.MaxFrameBytes), rpc.DialOptions{})
	if err != nil {
		_ = conn.Conn.Close()
		t.Fatalf("dial the agent's attached stream: %v", err)
	}
	session, err := host.Attach(ctx, conn.SessionID)
	if err != nil {
		_ = host.Close()
		t.Fatalf("attach the agent's session %s: %v", conn.SessionID, err)
	}
	return &attachment{conn: conn, host: host, session: session}
}

// TestAViewerCannotEndAnAgentsSession is W5: a viewer opened on an agent's
// session cannot close it or replace it, and the run in flight on it finishes
// anyway (decision 1).
func TestAViewerCannotEndAnAgentsSession(t *testing.T) {
	// The agent's one turn holds on the gate, so the run stays in flight while
	// the viewer tries to end it.
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{{Text: "the agent's reply", gate: gate}})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	agentID, agentSID := spawnFleetAgent(t, h, ctx)
	agent, ok := h.ws.Agent(agentID)
	if !ok {
		t.Fatalf("agent %s has no record", agentID)
	}
	// Decision 3's sentence, naming the agent as the fleet names it.
	sentence := "This is agent " + agent.Name + "'s session: finish it from the fleet screen."

	promptErr := promptAgent(t, h, ctx, agentSID, "work while I watch")
	a := attachAgent(t, h, agentID)
	defer a.close()
	if got := a.conn.AgentID; got != agentID {
		t.Fatalf("the attach named agent %q, want %q", got, agentID)
	}
	if got := a.conn.SessionID; got != agentSID {
		t.Fatalf("the attach named session %q, want the agent's %q", got, agentSID)
	}
	waitFor(t, func() error {
		if got := fixture.Completions(); got < 1 {
			return fmt.Errorf("the agent's turn has not reached the model (%d completions)", got)
		}
		return nil
	})

	// 1. A close naming the agent's session is refused with the sentence.
	if err := a.session.Close(ctx); err == nil {
		t.Fatal("a viewer closed an agent's session")
	} else if !strings.Contains(err.Error(), sentence) {
		t.Fatalf("the close's refusal = %q, want the sentence %q", err, sentence)
	}

	// 2. A replacement is refused the same way and leaves the session as it
	// was.
	if _, err := a.host.NewSession(ctx, "a replacement"); err == nil {
		t.Fatal("a viewer replaced an agent's session")
	} else if !strings.Contains(err.Error(), sentence) {
		t.Fatalf("the replacement's refusal = %q, want the sentence %q", err, sentence)
	}

	// 3. The agent's run is still in flight: the prompt has not returned, so
	// opening the gate is what lets the turn finish, and the fleet still lists
	// the agent.
	select {
	case err := <-promptErr:
		t.Fatalf("the agent's turn ended before the gate opened: %v", err)
	default:
	}
	close(gate)
	waitPrompt(t, promptErr)
	if _, ok := agentStatus(h, agentID); !ok {
		t.Fatalf("the fleet no longer lists agent %s", agentID)
	}
}

// TestAPausedAgentsSessionIsRefusedEverywhere is W6: once an agent is paused,
// neither attach opens the session its record still names, and the agent attach
// does not resume it behind the fleet's back.
func TestAPausedAgentsSessionIsRefusedEverywhere(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	agentID, agentSID := spawnFleetAgent(t, h, ctx)
	if err := h.fleet.Pause(agentID); err != nil {
		t.Fatalf("Pause(%s): %v", agentID, err)
	}

	// 1. The project attach refuses the session the record still names.
	att, err := h.client.Attach(ctx, h.root, agentSID)
	assertAttachRefused(t, att, err, http.StatusConflict)

	// 2. So does the agent attach: a paused agent is resumed from the fleet.
	att, err = h.client.AttachAgent(ctx, agentID)
	assertAttachRefused(t, att, err, http.StatusConflict)
}

// TestAnUnknownAgentIsNotFound: an agent attach for an ID the fleet does not
// know is a refusal, not a stream.
func TestAnUnknownAgentIsNotFound(t *testing.T) {
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	att, err := h.client.AttachAgent(ctx, "nope")
	assertAttachRefused(t, att, err, http.StatusNotFound)
}

// assertAttachRefused fails unless att is nil and err is an *AttachError with
// want status, closing a stream the daemon should not have opened.
func assertAttachRefused(t *testing.T, att *daemon.Attachment, err error, want int) {
	t.Helper()
	if att != nil {
		_ = att.Conn.Close()
	}
	var refusal *daemon.AttachError
	if !errors.As(err, &refusal) {
		t.Fatalf("attach = %+v, %v, want a daemon.AttachError", att, err)
	}
	if refusal.Status != want {
		t.Fatalf("attach refusal = %d (%s), want %d", refusal.Status, refusal.Message, want)
	}
}
