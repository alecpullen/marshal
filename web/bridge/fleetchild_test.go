package bridge

import (
	"fmt"
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

// hostOnlyProjectFleet is projectFleet made host-only, with newRuntime failing
// the test: a local agent on a project child must never build a child of its
// own (P4 D10 decision 2).
func hostOnlyProjectFleet(t *testing.T) (*Fleet, func(root string) *fakeProjectChild) {
	t.Helper()
	f, fakeFor := projectFleet(t)
	f.SetHostProcessesOnly()
	f.newRuntime = func(a Agent) (*Child, error) {
		t.Errorf("newRuntime was called for agent %s on project %s; a host-only local agent runs on its project's child", a.ID, a.Project)
		return nil, fmt.Errorf("newRuntime must not be called")
	}
	return f, fakeFor
}

// TestALocalAgentRunsOnItsProjectsChild: Spawn starts one project child and
// records the agent's session/new on its stdio, with no client-trust mark, and
// the session is tracked as the fleet's on that child's registry (P4 D10).
func TestALocalAgentRunsOnItsProjectsChild(t *testing.T) {
	f, fakeFor := hostOnlyProjectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	id, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	fake := fakeFor(root)
	if fake == nil {
		t.Fatal("no project child was built for the root")
	}
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("generations = %d, want exactly one project child", gens)
	}

	newCalls := fake.calls("session/new")
	if len(newCalls) != 1 {
		t.Fatalf("session/new calls = %v, want exactly one on the project child", newCalls)
	}
	if got := paramsOf(t, newCalls[0])["clientTrust"]; got != nil {
		t.Errorf("session/new clientTrust = %v, want none: an agent's session is not a client's", got)
	}
	for _, rec := range fake.record() {
		if rec.frame.method == "session/new" && rec.conn != stdioConn {
			t.Fatalf("session/new arrived on connection %d, want the child's stdio", rec.conn)
		}
	}

	pc, ok := projectEntry(f, root)
	if !ok {
		t.Fatal("the project child is not in the fleet's map")
	}
	a, ok := f.ws.Agent(id)
	if !ok {
		t.Fatalf("agent %s was not recorded", id)
	}
	if !pc.reg.IsFleet(a.SessionID) {
		t.Fatalf("session %s is not tracked as the fleet's on the project child", a.SessionID)
	}
}

// TestAnAgentAndAClientShareTheChild: an agent and a client session on one
// project share its child, and the daemon's listing leaves the agent's session
// out (P4 D10 decision 1).
func TestAnAgentAndAClientShareTheChild(t *testing.T) {
	f, fakeFor := hostOnlyProjectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	clientSID, err := pc.reg.New(ctx, AgentPath(root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	if _, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	fake := fakeFor(root)
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("generations = %d, want one child shared by both sessions", gens)
	}
	if got := pc.reg.count(); got != 2 {
		t.Fatalf("the child tracks %d sessions, want two", got)
	}

	var listed []string
	for _, s := range pc.reg.summaries() {
		listed = append(listed, s.ID)
	}
	if len(listed) != 1 || listed[0] != clientSID {
		t.Fatalf("the daemon's listing = %v, want only the client's session", listed)
	}
}

// TestOnlyPlainLocalAgentsMove: every agent that is not a plain local host
// agent keeps a child of its own (P4 D10 decision 2).
func TestOnlyPlainLocalAgentsMove(t *testing.T) {
	f, _ := projectFleet(t)
	local := Agent{ID: "a1", Project: t.TempDir(), SourceKind: "local", Profile: DefaultRuntimeProfile()}

	if f.onProjectChild(local) {
		t.Error("a fleet that is not host-only moved a local agent onto a project child")
	}

	f.SetHostProcessesOnly()
	if !f.onProjectChild(local) {
		t.Error("a plain local agent did not move onto its project's child")
	}

	git := local
	git.SourceKind = "git"
	if f.onProjectChild(git) {
		t.Error("a git-sourced agent moved onto a project child")
	}

	workspace := local
	workspace.Workspace = &AgentWorkspace{Name: "w1", Source: "studio"}
	if f.onProjectChild(workspace) {
		t.Error("a workspace agent moved onto a project child")
	}

	f.egressMu.Lock()
	f.egress = &egressHost{}
	f.egressMu.Unlock()
	t.Cleanup(func() {
		f.egressMu.Lock()
		f.egress = nil
		f.egressMu.Unlock()
	})
	if f.onProjectChild(local) {
		t.Error("an agent moved onto a project child while the egress proxy runs")
	}
}

// TestAnAgentsEventsReachItsOwnLog: an agent on a project child reads the
// child's log and the fleet's deltas under its own id (P4 D10 decision 3).
func TestAnAgentsEventsReachItsOwnLog(t *testing.T) {
	f, fakeFor := hostOnlyProjectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	id, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	a, ok := f.ws.Agent(id)
	if !ok || a.SessionID == "" {
		t.Fatalf("agent %s has no recorded session", id)
	}
	fake := fakeFor(root)

	log, err := f.LogForSession(a.SessionID)
	if err != nil {
		t.Fatalf("LogForSession: %v", err)
	}
	pc, _ := projectEntry(f, root)
	if log != pc.log {
		t.Fatal("the agent's log is not the project child's log")
	}

	fake.notify("session/update", map[string]any{
		"sessionId": a.SessionID,
		"update":    map[string]any{"kind": "tool_call", "toolName": "file.read"},
	})
	waitLogged(t, log, a.SessionID)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := activityDeltas(f); got[id] == "file.read" {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("activity deltas = %v, want one under agent %s", got, id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTheAgentTakesTheChildsVersion: an agent on a project child carries the
// child's version skew warning, which is checkAgentVersion's rule (P4 D10).
func TestTheAgentTakesTheChildsVersion(t *testing.T) {
	f, fakeFor := projectFleetWith(t, func(fake *fakeProjectChild) {
		fake.fakeAgent.mu.Lock()
		fake.fakeAgent.results["initialize"] = map[string]any{
			"protocolVersion": 1,
			"agentInfo":       map[string]any{"version": "v2.0.0"},
		}
		fake.fakeAgent.mu.Unlock()
	})
	f.SetHostProcessesOnly()
	f.buildVersion = "v1.0.0"
	ctx := ctlContext(t)
	root := t.TempDir()

	id, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if fakeFor(root) == nil {
		t.Fatal("no project child was built for the root")
	}

	f.mu.Lock()
	rt := f.runtimes[id]
	f.mu.Unlock()
	if rt == nil {
		t.Fatalf("agent %s has no runtime", id)
	}
	if !rt.versionWarning {
		t.Fatalf("versionWarning = false, want true: the child reports v2.0.0 and the bridge v1.0.0")
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
