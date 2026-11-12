package bridge

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// fleetRegistry returns a project child's registry, which carries plan 7's
// registry-wide client-trust mark, and the fake behind it.
func fleetRegistry(t *testing.T) (*Registry, *fakeProjectChild) {
	t.Helper()
	f, fakeFor := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()
	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(filepath.Clean(root))
	if fake == nil {
		t.Fatal("no fake was built for the project child")
	}
	return pc.reg, fake
}

// backdate makes id's last activity old enough that the idle sweep sees it.
func backdate(t *testing.T, reg *Registry, id string) {
	t.Helper()
	reg.mu.Lock()
	if info, ok := reg.sessions[id]; ok {
		info.lastActive = time.Now().Add(-2 * time.Hour)
	}
	reg.mu.Unlock()
}

// TestTheIdleSweepLeavesFleetSessionsAlone: a fleet agent's session is not a
// candidate however long it sits idle, and closeIfIdle refuses it.
func TestTheIdleSweepLeavesFleetSessionsAlone(t *testing.T) {
	reg, fake := fleetRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	clientID, err := reg.New(ctx, AgentPath("/home/u/repo"), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	reg.TrackFleet("fleet-1", AgentPath("/home/u/repo"))
	backdate(t, reg, clientID)
	backdate(t, reg, "fleet-1")

	cutoff := time.Now().Add(-time.Hour)
	candidates := reg.idleCandidates(cutoff)
	if len(candidates) != 1 || candidates[0] != clientID {
		t.Fatalf("idleCandidates = %v, want only the client's %s", candidates, clientID)
	}

	closed, err := reg.closeIfIdle(ctx, "fleet-1", cutoff)
	if err != nil {
		t.Fatalf("closeIfIdle: %v", err)
	}
	if closed {
		t.Fatal("closeIfIdle closed a fleet agent's session")
	}
	if !reg.IsFleet("fleet-1") {
		t.Fatal("the fleet session is no longer tracked as the fleet's")
	}
	for _, params := range fake.calls("session/close") {
		if got := paramsOf(t, params)["sessionId"]; got == "fleet-1" {
			t.Fatalf("session/close was sent for the fleet session: %s", params)
		}
	}
}

// TestTheDaemonListingLeavesFleetSessionsOut: the daemon's listing offers the
// client's session only, but the linger still counts the fleet's.
func TestTheDaemonListingLeavesFleetSessionsOut(t *testing.T) {
	reg, _ := fleetRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	clientID, err := reg.New(ctx, AgentPath("/home/u/repo"), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	reg.TrackFleet("fleet-1", AgentPath("/home/u/repo"))

	summaries := reg.summaries()
	if len(summaries) != 1 || summaries[0].ID != clientID {
		t.Fatalf("summaries = %+v, want only the client's %s", summaries, clientID)
	}
	if got := reg.count(); got != 2 {
		t.Fatalf("count = %d, want 2: a child with an agent on it must not linger", got)
	}
}

// TestARestartResumesAFleetSessionUnmarked: the mark is per session, so a
// restart's resumes carry it for a client's session and omit it for a fleet
// agent's (decision 1).
func TestARestartResumesAFleetSessionUnmarked(t *testing.T) {
	reg, fake := fleetRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	clientID, err := reg.New(ctx, AgentPath("/home/u/repo"), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	reg.TrackFleet("fleet-1", AgentPath("/home/u/repo"))

	fake.die()

	deadline := time.Now().Add(10 * time.Second)
	for {
		resumed := resumedSessions(fake)
		if resumed[clientID] && resumed["fleet-1"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no resume of both sessions after the restart; resumed %v", resumed)
		}
		time.Sleep(10 * time.Millisecond)
	}

	seen := map[string]map[string]any{}
	for _, params := range fake.calls("session/resume") {
		p := paramsOf(t, params)
		id, _ := p["sessionId"].(string)
		seen[id] = p
	}
	if got := seen[clientID]["clientTrust"]; got != true {
		t.Errorf("the client's session/resume clientTrust = %v, want true (params %v)", got, seen[clientID])
	}
	if _, ok := seen["fleet-1"]["clientTrust"]; ok {
		t.Errorf("the fleet session's session/resume carried clientTrust, want it omitted: %v", seen["fleet-1"])
	}
}

// TestLoadFleetSendsNoMark: a fleet agent's restore is session/load without
// the registry-wide mark, tracked as the fleet's.
func TestLoadFleetSendsNoMark(t *testing.T) {
	reg, fake := fleetRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	if err := reg.LoadFleet(ctx, AgentPath("/home/u/repo"), "stored-1"); err != nil {
		t.Fatalf("LoadFleet: %v", err)
	}
	calls := fake.calls("session/load")
	if len(calls) != 1 {
		t.Fatalf("session/load sent %d times, want 1", len(calls))
	}
	if got, ok := paramsOf(t, calls[0])["clientTrust"]; ok {
		t.Errorf("session/load carried clientTrust = %v, want the field omitted: %s", got, calls[0])
	}
	if !reg.IsFleet("stored-1") {
		t.Fatal("LoadFleet did not track the session as the fleet's")
	}
	if reg.tracksClient("stored-1") {
		t.Fatal("LoadFleet tracked the session as a client's")
	}
}

// TestForgettingUntracksWithoutAClose: a close that failed still leaves the
// session untracked and its waits drained, without asking the child again
// (decision 5).
func TestForgettingUntracksWithoutAClose(t *testing.T) {
	reg, fake := fleetRegistry(t)
	reg.track("fleet-1", AgentPath("/home/u/repo"))

	params, err := json.Marshal(map[string]string{"sessionId": "fleet-1", "toolCallId": "tc-9"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := reg.awaitPermission(params)
		done <- err
	}()
	waitFor(t, 2*time.Second, "pending permission", func() bool {
		reg.permMu.Lock()
		defer reg.permMu.Unlock()
		_, ok := reg.permissions["tc-9"]
		return ok
	})

	before := len(fake.calls("session/close"))
	reg.forget("fleet-1")
	if _, ok := reg.lookup("fleet-1"); ok {
		t.Fatal("forget left the session tracked")
	}
	if got := len(fake.calls("session/close")); got != before {
		t.Fatalf("forget sent session/close: %d call(s) before, %d after", before, got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the pending wait returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forget left a pending permission wait blocked")
	}
}
