package daemon_test

import (
	"errors"
	"strings"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/daemon"
	"marshal/web/bridge"
)

// daemonChildFor returns the daemon's listing for root, if it lists one.
func daemonChildFor(info daemon.Info, root string) (daemon.ChildInfo, bool) {
	for _, child := range info.Children {
		if child.Root == root {
			return child, true
		}
	}
	return daemon.ChildInfo{}, false
}

// TestAFleetAgentSharesTheProjectsChild is W13: under the local profile a fleet
// agent on a local project runs on that project's child, beside the TUI's
// session, and leaving closes its session rather than the child
// (P4 D10).
func TestAFleetAgentSharesTheProjectsChild(t *testing.T) {
	const reply = "the fleet agent's reply"
	fixture := newProviderFixture(t, []scriptedTurn{{Text: reply}})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// 1. A client attaches to a new session, as a TUI on the project does.
	a := h.attach("")
	defer a.close()
	clientSID := a.conn.SessionID

	// 2. A fleet agent on the same project: its session is the child's second.
	agentID, err := h.fleet.Spawn(ctx, h.root, bridge.SpawnOptions{Name: "fleet agent"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	agent, ok := h.ws.Agent(agentID)
	if !ok || agent.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	agentSID := agent.SessionID
	if agentSID == clientSID {
		t.Fatalf("the agent was given the client's session %s", clientSID)
	}

	// 3. One child for the project, and its sessions are the client's only.
	waitFor(t, func() error {
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		if len(info.Children) != 1 {
			return errors.New("the daemon does not list exactly one child")
		}
		child, ok := daemonChildFor(info, h.root)
		if !ok {
			return errors.New("the project's child is not listed")
		}
		if len(child.Sessions) != 1 || child.Sessions[0].ID != clientSID {
			return errors.New("the listing does not name the client's session alone")
		}
		return nil
	})

	// 4. A prompt on the agent's session reaches the child and its reply lands
	// in the agent's own event log.
	reg, err := h.fleet.RegistryForSession(agentSID)
	if err != nil {
		t.Fatalf("RegistryForSession: %v", err)
	}
	if err := reg.Prompt(ctx, agentSID, "say something"); err != nil {
		t.Fatalf("prompt the agent: %v", err)
	}
	log, err := h.fleet.LogForSession(agentSID)
	if err != nil {
		t.Fatalf("LogForSession: %v", err)
	}
	waitFor(t, func() error {
		for _, e := range log.Tail(agentSID) {
			if strings.Contains(string(e.Data), reply) {
				return nil
			}
		}
		return errors.New("the agent's log does not carry the scripted reply")
	})

	// 5. An attach that names the agent's session is refused.
	_, err = h.client.Attach(ctx, h.root, agentSID)
	var refusal *daemon.AttachError
	if !errors.As(err, &refusal) {
		t.Fatalf("attach to the agent's session = %v, want a daemon.AttachError", err)
	}
	if refusal.Status != 409 {
		t.Fatalf("attach refusal status = %d, want 409", refusal.Status)
	}

	// 6. Pausing the agent closes its session and leaves the child serving the
	// client's.
	if err := h.fleet.Pause(agentID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitFor(t, func() error {
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		if len(info.Children) != 1 {
			return errors.New("the child went away with the agent")
		}
		child, ok := daemonChildFor(info, h.root)
		if !ok {
			return errors.New("the project's child is not listed after the agent paused")
		}
		if len(child.Sessions) != 1 || child.Sessions[0].ID != clientSID {
			return errors.New("the client's session is not the child's only one after the agent paused")
		}
		return nil
	})
	if _, err := a.session.Messages(ctx, acpwire.MessageQuery{}); err != nil {
		t.Fatalf("the client's session stopped answering after the agent paused: %v", err)
	}
}
