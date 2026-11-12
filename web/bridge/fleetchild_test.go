package bridge

import (
	"testing"
	"time"
)

// setFakeCaps replaces the capabilities the fake's initialize advertises,
// under the mutex its reply path reads them with.
func setFakeCaps(fake *fakeProjectChild, caps map[string]any) {
	fake.fakeAgent.mu.Lock()
	fake.fakeAgent.caps = caps
	fake.fakeAgent.mu.Unlock()
}

// childCaps reads the capabilities a project child recorded, under its lock.
func childCaps(pc *projectChild) map[string]bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	out := make(map[string]bool, len(pc.caps))
	for k, v := range pc.caps {
		out[k] = v
	}
	return out
}

// waitLogged waits until the log holds at least one event for the session.
func waitLogged(t *testing.T, l *EventLog, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(l.Tail(sessionID)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("session %s was never logged", sessionID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAProjectChildLogsTheFleetsSessionsOnly: the child's own log holds the
// fleet's session and a session nothing has tracked yet, and not a client's
// session, which belongs to the client's own log (P4 D10 decision 3).
func TestAProjectChildLogsTheFleetsSessionsOnly(t *testing.T) {
	f, fakeFor := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}
	if pc.log == nil {
		t.Fatal("the project child has no event log")
	}

	clientSID, err := pc.reg.New(ctx, AgentPath(root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	pc.reg.TrackFleet("fleet-1", AgentPath(root))
	// Untracked: a fleet session that Spawn has not reached yet. Its events
	// must reach the log from its first notification.
	const untracked = "untracked-1"

	for _, sid := range []string{clientSID, "fleet-1", untracked} {
		fake.notify("session/update", map[string]any{
			"sessionId": sid,
			"update":    map[string]any{"kind": "agent_message", "text": "hello"},
		})
	}

	waitLogged(t, pc.log, "fleet-1")
	waitLogged(t, pc.log, untracked)

	if got := pc.log.Tail(clientSID); len(got) != 0 {
		t.Fatalf("the client's session %s was logged on the project child: %d events", clientSID, len(got))
	}
}

// TestAProjectChildClassifiesByAgent: the child's classifier finds the agent
// through the fleet's session map, so a fleet session's notification reaches
// the emitted deltas under the agent's id, never the session's (P4 D10
// decision 4).
func TestAProjectChildClassifiesByAgent(t *testing.T) {
	f, fakeFor := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}

	pc.reg.TrackFleet("fleet-1", AgentPath(root))
	f.mu.Lock()
	f.sessionAgent["fleet-1"] = "agent-a"
	f.mu.Unlock()

	fake.notify("session/update", map[string]any{
		"sessionId": "fleet-1",
		"update":    map[string]any{"kind": "tool_call", "toolName": "file.read"},
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		got := activityDeltas(f)
		if got["agent-a"] == "file.read" {
			if _, keyed := got["fleet-1"]; keyed {
				t.Fatalf("the delta was keyed by the raw session id: %v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity deltas = %v, want one under agent-a", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTheHandshakeRecordsTheChildsCapabilities: the capabilities the child
// advertises are on the child after it starts, and again after a restart,
// because they belong to the generation that answered (P4 D10).
func TestTheHandshakeRecordsTheChildsCapabilities(t *testing.T) {
	f, fakeFor := projectFleetWith(t, func(fake *fakeProjectChild) {
		setFakeCaps(fake, map[string]any{
			"configAccess":    map[string]any{},
			"sessionHolds":    map[string]any{},
			"sessionControls": map[string]any{},
		})
	})
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}

	caps := childCaps(pc)
	for _, want := range []string{"configAccess", "sessionHolds", "sessionControls"} {
		if !caps[want] {
			t.Errorf("caps after the first handshake = %v, want %q", caps, want)
		}
	}

	// A restart re-handshakes, and the next generation's capabilities replace
	// the previous generation's rather than being merged into them.
	setFakeCaps(fake, map[string]any{"watchAccess": map[string]any{}})
	fake.die()

	deadline := time.Now().Add(10 * time.Second)
	for {
		caps := childCaps(pc)
		if caps["watchAccess"] && !caps["sessionHolds"] && !caps["configAccess"] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("caps after the restart = %v, want the restarted generation's only", caps)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
