package bridge

import (
	"context"
	"encoding/json"
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

// lingerArmed reports whether the child has a linger timer armed.
func lingerArmed(pc *projectChild) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.linger != nil
}

// closeCalls returns the session ids the fake was asked to close.
func closeCalls(t *testing.T, fake *fakeProjectChild) []string {
	t.Helper()
	var out []string
	for _, params := range fake.calls("session/close") {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			t.Fatalf("decode session/close params %q: %v", params, err)
		}
		out = append(out, p.SessionID)
	}
	return out
}

// TestRetiringAnAgentLeavesTheChild: retiring an agent closes its session on
// the project's child and leaves the child serving the client's session, with
// no stop and no detach (P4 D10 decision 5).
func TestRetiringAnAgentLeavesTheChild(t *testing.T) {
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
	id, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	fake := fakeFor(root)
	a, ok := f.ws.Agent(id)
	if !ok || a.SessionID == "" {
		t.Fatalf("agent %s has no recorded session", id)
	}

	f.stopAgent(id)

	closed := closeCalls(t, fake)
	if len(closed) != 1 || closed[0] != a.SessionID {
		t.Fatalf("session/close = %v, want only the agent's session %s", closed, a.SessionID)
	}
	if fake.wasStopped() {
		t.Fatal("retiring an agent stopped the project child")
	}
	if !pc.reg.tracksClient(clientSID) {
		t.Fatalf("the client's session %s was dropped with the agent", clientSID)
	}
}

// TestTheLastAgentLeavingArmsTheLinger: with no client session, retiring the
// agent leaves the child with nothing tracked and a linger that stops it.
func TestTheLastAgentLeavingArmsTheLinger(t *testing.T) {
	f, _ := hostOnlyProjectFleet(t)
	kit := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	id, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	pc, ok := projectEntry(f, root)
	if !ok {
		t.Fatal("the project child is not in the fleet's map")
	}

	f.stopAgent(id)

	if !lingerArmed(pc) {
		t.Fatal("the released child has no linger armed")
	}
	if kit.count() == 0 {
		t.Fatal("no linger timer was captured")
	}
	kit.at(kit.count() - 1).fire()

	if _, still := projectEntry(f, root); still {
		t.Fatal("the child is still in the fleet's map after its linger fired")
	}
}

// TestPausingAndResumingReloadsOnTheChild: Pause closes the agent's session;
// Resume loads it again on the same child, mapped before the load and without
// the client-trust mark (P4 D10 decisions 4 and 5).
func TestPausingAndResumingReloadsOnTheChild(t *testing.T) {
	var fleet *Fleet
	f, fakeFor := projectFleetWith(t, func(fake *fakeProjectChild) {
		fake.fakeAgent.mu.Lock()
		fake.fakeAgent.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
			if method != "session/load" {
				return nil, nil, false
			}
			var p struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, nil, false
			}
			if _, mapped := fleet.agentForSession(p.SessionID); !mapped {
				return nil, &rpcError{Code: -32000, Message: "session loaded before it was mapped"}, true
			}
			return nil, nil, false
		}
		fake.fakeAgent.mu.Unlock()
	})
	fleet = f
	f.SetHostProcessesOnly()
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
	pc, _ := projectEntry(f, root)

	if err := f.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if closed := closeCalls(t, fake); len(closed) != 1 || closed[0] != a.SessionID {
		t.Fatalf("session/close after Pause = %v, want %s", closed, a.SessionID)
	}

	if err := f.Resume(ctx, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("generations = %d, want the same child", gens)
	}
	loads := fake.calls("session/load")
	if len(loads) != 1 {
		t.Fatalf("session/load calls = %v, want exactly one", loads)
	}
	params := paramsOf(t, loads[0])
	if got := params["sessionId"]; got != a.SessionID {
		t.Fatalf("session/load sessionId = %v, want %s", got, a.SessionID)
	}
	if got := params["clientTrust"]; got != nil {
		t.Errorf("session/load clientTrust = %v, want none for a fleet session", got)
	}
	if !pc.reg.IsFleet(a.SessionID) {
		t.Fatalf("session %s is not tracked as the fleet's after Resume", a.SessionID)
	}
}

// TestAFailedCloseStillUntracks: a session/close the child refuses still
// untracks the session, and the child is left with its linger (decision 5).
func TestAFailedCloseStillUntracks(t *testing.T) {
	f, fakeFor := projectFleetWith(t, func(fake *fakeProjectChild) {
		fake.fakeAgent.mu.Lock()
		fake.fakeAgent.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
			if method != "session/close" {
				return nil, nil, false
			}
			return nil, &rpcError{Code: -32000, Message: "no such session"}, true
		}
		fake.fakeAgent.mu.Unlock()
	})
	f.SetHostProcessesOnly()
	shortenLinger(t, f)
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
	pc, _ := projectEntry(f, root)

	f.stopAgent(id)

	if pc.reg.IsFleet(a.SessionID) {
		t.Fatalf("session %s is still tracked after the close failed", a.SessionID)
	}
	if !lingerArmed(pc) {
		t.Fatal("the linger was not armed after the close failed")
	}
	if fake.wasStopped() {
		t.Fatal("the project child was stopped by the failed close")
	}
}

// TestTheFleetsCloseStopsTheSharedChildOnce: Close stops the child an agent
// shares with a client, and does not detach it (P4 D10).
func TestTheFleetsCloseStopsTheSharedChildOnce(t *testing.T) {
	f, fakeFor := hostOnlyProjectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	if _, err := pc.reg.New(ctx, AgentPath(root), ""); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if _, err := f.Spawn(ctx, root, SpawnOptions{Name: "one"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	fake := fakeFor(root)

	f.Close()

	fake.waitStopped(t, 5*time.Second)
	if _, still := projectEntry(f, root); still {
		t.Fatal("the child is still in the fleet's map after Close")
	}
	// One stop, not a stop and a retry: the shared child is stopped by
	// stopProjectChildren and by nothing else.
	if got := fake.signalsSeen(); got != 1 {
		t.Fatalf("the shared child got %d stop signals, want exactly 1", got)
	}
}

// TestReattachAllLoadsOnTheProjectChild: a persisted local agent is reattached
// with session/load on its project's child, after the session is mapped
// (P4 D10 decisions 4 and 5).
func TestReattachAllLoadsOnTheProjectChild(t *testing.T) {
	f, fakeFor := hostOnlyProjectFleet(t)
	root := t.TempDir()

	if err := f.ws.PutAgent(Agent{
		ID: "a1", Project: root, OwnerID: DefaultOwnerID,
		SourceKind: "local", Profile: DefaultRuntimeProfile(),
		SessionID: "sess-1", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("PutAgent: %v", err)
	}

	if errs := f.ReattachAll(context.Background()); len(errs) != 0 {
		t.Fatalf("ReattachAll: %v", errs)
	}
	fake := fakeFor(root)
	if fake == nil {
		t.Fatal("no project child was built for the root")
	}

	loads := fake.calls("session/load")
	if len(loads) != 1 {
		t.Fatalf("session/load calls = %v, want exactly one on the project child", loads)
	}
	params := paramsOf(t, loads[0])
	if got := params["sessionId"]; got != "sess-1" {
		t.Fatalf("session/load sessionId = %v, want sess-1", got)
	}
	if got := params["clientTrust"]; got != nil {
		t.Errorf("session/load clientTrust = %v, want none for a fleet session", got)
	}
	pc, _ := projectEntry(f, root)
	if !pc.reg.IsFleet("sess-1") {
		t.Fatal("the reattached session is not tracked as the fleet's")
	}
}

// TestTheAgentsRuntimeTakesTheChildsCapabilities: an agent's runtime receives
// the shared child's capabilities, copied under the child's lock, so a
// capability-gated feature (watches) works for a fleet agent on a project
// child (P4 D10).
func TestTheAgentsRuntimeTakesTheChildsCapabilities(t *testing.T) {
	f, fakeFor := projectFleetWith(t, func(fake *fakeProjectChild) {
		setFakeCaps(fake, map[string]any{
			"configAccess": map[string]any{},
			"watchAccess":  map[string]any{},
		})
	})
	f.SetHostProcessesOnly()
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
	if !rt.caps["configAccess"] || !rt.caps["watchAccess"] {
		t.Fatalf("the runtime's caps = %v, want the child's advertised set", rt.caps)
	}
	if rt.caps["sessionHolds"] {
		t.Fatalf("the runtime's caps = %v, want only what the child advertised", rt.caps)
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
