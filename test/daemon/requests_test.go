package daemon_test

// The three tests here are the end-to-end proof of the request brokering: an
// approval the agent asks for reaches the bridge, a viewer's answer settles the
// bridge's request, and the bridge's answer settles the viewer's mirror. Each
// spawns a real fleet agent on the harness's project, so the requests travel
// the whole path — agent, project child, viewer socket, bridge registry.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/web/bridge"
	"marshal/web/bridge/contract"
)

// viewerSocketPath recomputes web/bridge/projectchild.go's viewerSocketPath:
// <stateDir>/v/<first 16 hex digits of sha256(root)>.sock. That function is
// unexported and this package is outside it, so the path is computed here.
func viewerSocketPath(stateDir, root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(stateDir, "v", hex.EncodeToString(sum[:8])+".sock")
}

// viewerOf attaches to the agent's session over the project child's own viewer
// socket, bypassing the daemon's attach (which refuses a fleet agent's session).
// It dials and attaches as daemonHarness.attach does after its upgrade.
//
// Plan 4b's agent attach replaces it in its own tests; it exists because that
// route does not exist yet. The attachment carries no daemon.Attachment, so
// callers name the session with a.session.ID().
func viewerOf(t *testing.T, h *daemonHarness, sessionID string) *attachment {
	t.Helper()
	sock := viewerSocketPath(h.stateDir, h.root)
	ctx := callCtx(t)

	// The socket appears with the child, which the spawn starts, so a caller
	// that arrives early waits for both the file and the listener behind it.
	var conn net.Conn
	waitFor(t, func() error {
		c, err := net.Dial("unix", sock)
		if err != nil {
			return fmt.Errorf("dial the project child's viewer socket %s: %w", sock, err)
		}
		conn = c
		return nil
	})
	t.Cleanup(func() { _ = conn.Close() })

	host, err := rpc.Dial(ctx, rpc.NewStream(conn, conn, rpc.MaxFrameBytes), rpc.DialOptions{})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("dial the viewer stream: %v", err)
	}
	session, err := host.Attach(ctx, sessionID)
	if err != nil {
		_ = host.Close()
		_ = conn.Close()
		t.Fatalf("attach session %s over the viewer socket: %v", sessionID, err)
	}
	return &attachment{host: host, session: session}
}

// spawnFleetAgent starts a fleet agent on the harness's project and returns the
// agent's ID and the session ID the bridge gave it.
func spawnFleetAgent(t *testing.T, h *daemonHarness, ctx context.Context) (agentID, sessionID string) {
	t.Helper()
	agentID, err := h.fleet.Spawn(ctx, h.root, bridge.SpawnOptions{Name: "fleet agent"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	agent, ok := h.ws.Agent(agentID)
	if !ok || agent.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	return agentID, agent.SessionID
}

// promptAgent prompts the agent's session on its own goroutine: Registry.Prompt
// blocks until the turn ends, and the test needs to answer the request while
// the turn is parked on it.
func promptAgent(t *testing.T, h *daemonHarness, ctx context.Context, sessionID, text string) <-chan error {
	t.Helper()
	reg, err := h.fleet.RegistryForSession(sessionID)
	if err != nil {
		t.Fatalf("RegistryForSession(%s): %v", sessionID, err)
	}
	promptErr := make(chan error, 1)
	go func() { promptErr <- reg.Prompt(ctx, sessionID, text) }()
	return promptErr
}

// waitPrompt waits for the agent's turn to end and fails on a failed turn.
func waitPrompt(t *testing.T, promptErr <-chan error) {
	t.Helper()
	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("the agent's turn failed: %v", err)
		}
	case <-time.After(2 * time.Minute):
		t.Fatal("the agent's turn did not finish")
	}
}

// agentStatus finds an agent in the fleet's snapshot.
func agentStatus(h *daemonHarness, agentID string) (bridge.AgentStatus, bool) {
	for _, st := range h.fleet.Snapshot() {
		if st.ID == agentID {
			return st, true
		}
	}
	return bridge.AgentStatus{}, false
}

// waitBridgeApproval waits until the bridge lists the agent's approval and
// returns it. It reports what each poll found when the budget elapses: no
// pending request means the approval never reached the bridge, which is the
// failure this proves against.
func waitBridgeApproval(t *testing.T, h *daemonHarness, agentID string, budget time.Duration) bridge.PendingRequest {
	t.Helper()
	var pending bridge.PendingRequest
	waitWithin(t, budget, "the agent's approval reaching the bridge", func() error {
		st, ok := agentStatus(h, agentID)
		if !ok {
			return fmt.Errorf("agent %s is not in the fleet snapshot", agentID)
		}
		if st.Pending == nil {
			return fmt.Errorf("the agent has nothing pending (status %q)", st.Status)
		}
		if st.Pending.Kind != contract.PendingApproval {
			return fmt.Errorf("the agent's pending request is %q, want an approval", st.Pending.Kind)
		}
		pending = *st.Pending
		return nil
	})
	return pending
}

// waitWithin polls cond until it holds or the budget elapses.
func waitWithin(t *testing.T, budget time.Duration, what string, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last error
	for time.Now().Before(deadline) {
		if last = cond(); last == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s: %v", what, budget, last)
}

// TestAFleetAgentsApprovalReachesTheBridge is the probe this plan's finding
// came from, made a test: the agent numbers its outbound requests as strings,
// and the bridge used to drop them, so a real agent's approval never reached
// the bridge's pending list.
func TestAFleetAgentsApprovalReachesTheBridge(t *testing.T) {
	fixture := newProviderFixture(t, []scriptedTurn{
		{ToolName: "shell.run", ToolArgs: mustJSON(t, map[string]string{"command": approvalCommand})},
		{Text: "done"},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	// The project's confirmation rule is what makes the shell call ask, and the
	// trust record is what makes the project's own config apply. Both land
	// before the child starts, which the spawn does.
	writeApprovalPolicy(t, h.root)
	trustProject(t, h.root)
	ctx := callCtx(t)

	agentID, agentSID := spawnFleetAgent(t, h, ctx)
	promptErr := promptAgent(t, h, ctx, agentSID, "run the command")

	pending := waitBridgeApproval(t, h, agentID, 30*time.Second)
	// The ID is the tool call's, not the session's approval key: this is what a
	// dashboard POSTs its decision to.
	if err := h.fleet.ResolvePermission(pending.ID, bridge.Decision{Approved: true}); err != nil {
		t.Fatalf("resolve the approval at the bridge: %v", err)
	}

	waitPrompt(t, promptErr)
	if got := fixture.Completions(); got != 2 {
		t.Fatalf("the provider served %d completions, want 2 (the tool call and the reply)", got)
	}
}

// TestAViewersAnswerSettlesTheFleetsRequest is the first direction: a viewer
// answers first, and the bridge's request goes with it — the agent ends its
// forwarded RPC, sends $/cancel_request, and the bridge drops its entry.
func TestAViewersAnswerSettlesTheFleetsRequest(t *testing.T) {
	fixture := newProviderFixture(t, []scriptedTurn{
		{ToolName: "shell.run", ToolArgs: mustJSON(t, map[string]string{"command": approvalCommand})},
		{Text: "done"},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	writeApprovalPolicy(t, h.root)
	trustProject(t, h.root)
	ctx := callCtx(t)

	agentID, agentSID := spawnFleetAgent(t, h, ctx)
	promptErr := promptAgent(t, h, ctx, agentSID, "run the command")
	pending := waitBridgeApproval(t, h, agentID, 2*time.Minute)

	viewer := viewerOf(t, h, agentSID)
	defer viewer.close()
	// The viewer's key is the session's own approval key, not the bridge's
	// tool call ID.
	key := waitApproval(t, ctx, viewer, fixture)
	if err := viewer.session.AnswerApproval(ctx, key, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); err != nil {
		t.Fatalf("answer the approval from the viewer: %v", err)
	}

	waitWithin(t, 3*time.Second, "the bridge dropping the approval the viewer answered", func() error {
		st, ok := agentStatus(h, agentID)
		if !ok {
			return fmt.Errorf("agent %s left the fleet snapshot", agentID)
		}
		if st.Pending != nil {
			return fmt.Errorf("the agent still shows a pending %s (%s)", st.Pending.Kind, st.Pending.ID)
		}
		for _, id := range h.fleet.Inbox().Groups.NeedsYou {
			if id == agentID {
				return fmt.Errorf("the inbox still needs %s", agentID)
			}
		}
		return nil
	})
	if err := h.fleet.ResolvePermission(pending.ID, bridge.Decision{Approved: true}); !errors.Is(err, bridge.ErrGone) {
		t.Fatalf("resolving the settled approval = %v, want bridge.ErrGone", err)
	}

	waitPrompt(t, promptErr)
}

// TestTheFleetsAnswerSettlesTheViewersRequest is the other direction: the
// bridge answers first, its answer reaches the agent, and the viewer's mirror
// drops the request it was showing.
func TestTheFleetsAnswerSettlesTheViewersRequest(t *testing.T) {
	fixture := newProviderFixture(t, []scriptedTurn{
		{ToolName: "shell.run", ToolArgs: mustJSON(t, map[string]string{"command": approvalCommand})},
		{Text: "done"},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	writeApprovalPolicy(t, h.root)
	trustProject(t, h.root)
	ctx := callCtx(t)

	agentID, agentSID := spawnFleetAgent(t, h, ctx)
	viewer := viewerOf(t, h, agentSID)
	defer viewer.close()
	promptErr := promptAgent(t, h, ctx, agentSID, "run the command")

	key := waitApproval(t, ctx, viewer, fixture)
	pending := waitBridgeApproval(t, h, agentID, 2*time.Minute)
	if err := h.fleet.ResolvePermission(pending.ID, bridge.Decision{Approved: true}); err != nil {
		t.Fatalf("resolve the approval at the bridge: %v", err)
	}

	waitWithin(t, 3*time.Second, "the viewer's mirror dropping the answered approval", func() error {
		if approvals := viewer.session.Pending().Approvals; len(approvals) != 0 {
			return fmt.Errorf("the viewer still mirrors %d approvals", len(approvals))
		}
		return nil
	})
	// The key the viewer saw no longer names a live request, so a second answer
	// is refused as settled rather than accepted or silently dropped.
	if err := viewer.session.AnswerApproval(ctx, key, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Fatalf("answering the settled approval = %v, want %s", err, acpwire.CodeRequestSettled)
	}

	waitPrompt(t, promptErr)
}
