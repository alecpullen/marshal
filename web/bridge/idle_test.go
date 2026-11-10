package bridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// idleSessionID is the session every idle-sweep test tracks on its project
// child.
const idleSessionID = "s1"

// idleProject is one project child with one tracked session, over a fake
// transport: the idle sweep's fixtures.
type idleProject struct {
	fleet  *Fleet
	pc     *projectChild
	fake   *fakeProjectChild
	timers *lingerKit
}

// newIdleProject builds a fleet whose project children are fakes, starts one
// project child, and tracks idleSessionID on it. It installs captureTimers
// before the child starts, so the linger the start arms is the first timer the
// kit holds.
func newIdleProject(t *testing.T, timeout time.Duration, configure func(*fakeProjectChild)) *idleProject {
	t.Helper()
	f, fakeFor := projectFleetWith(t, configure)
	shortenLinger(t, f)
	f.SetIdleSessionTimeout(timeout)
	timers := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the project child")
	}
	pc.reg.track(idleSessionID, AgentPath(root))
	return &idleProject{fleet: f, pc: pc, fake: fake, timers: timers}
}

// setIdleLastActive writes a tracked session's last activity through the
// registry's own map under its lock: there is no setter, and this is the only
// field a test moves.
func setIdleLastActive(t *testing.T, reg *Registry, id string, at time.Time) {
	t.Helper()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	info, ok := reg.sessions[id]
	if !ok {
		t.Fatalf("session %s is not tracked", id)
	}
	info.lastActive = at
}

// closedSessions returns the session ids the fake was asked to close.
func closedSessions(fake *fakeProjectChild) []string {
	var out []string
	for _, params := range fake.calls("session/close") {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal([]byte(params), &p) == nil && p.SessionID != "" {
			out = append(out, p.SessionID)
		}
	}
	return out
}

// TestAnIdleSessionIsClosed: a session with no viewer, idle an hour, and no
// run is closed through the child, dropped from the registry, and leaves the
// child's linger armed.
func TestAnIdleSessionIsClosed(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, nil)
	p.fake.results["session/status"] = map[string]any{}
	now := time.Now()
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Hour))
	// Resolving a child calls its linger off, so an armed one after the sweep
	// is the sweep's own re-arm rather than the start-up one.
	p.pc.cancelLinger()
	if n := p.timers.count(); n != 1 {
		t.Fatalf("armed timers before the sweep = %d, want only the start-up one", n)
	}

	closed := p.fleet.sweepIdleSessions(ctlContext(t), now)

	if len(closed) != 1 || closed[0] != idleSessionID {
		t.Fatalf("closed = %v, want [%s]", closed, idleSessionID)
	}
	if got := closedSessions(p.fake); len(got) != 1 || got[0] != idleSessionID {
		t.Fatalf("the fake saw session/close for %v, want [%s]", got, idleSessionID)
	}
	if p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("the closed session is still tracked")
	}
	if p.fake.wasStopped() {
		t.Fatal("closing an idle session stopped the child")
	}
	if n := p.timers.count(); n != 2 {
		t.Fatalf("armed timers = %d, want the linger re-armed for the empty child", n)
	}
	if timer := p.timers.at(1); timer == nil || timer.d != p.fleet.lingerDuration() {
		t.Fatalf("re-armed linger = %+v, want %v", timer, p.fleet.lingerDuration())
	}
}

// TestASessionWithAViewerIsKept: a viewer keeps the session alive, and the
// sweep does not even ask about it.
func TestASessionWithAViewerIsKept(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, nil)
	p.fake.results["session/status"] = map[string]any{}
	now := time.Now()
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Hour))
	if !p.pc.reg.attachViewer(idleSessionID) {
		t.Fatalf("session %s is not tracked", idleSessionID)
	}

	closed := p.fleet.sweepIdleSessions(ctlContext(t), now)

	if len(closed) != 0 {
		t.Fatalf("closed = %v, want nothing", closed)
	}
	if calls := p.fake.calls("session/status"); len(calls) != 0 {
		t.Fatalf("the sweep asked for the status of a session with a viewer: %v", calls)
	}
	if got := closedSessions(p.fake); len(got) != 0 {
		t.Fatalf("the sweep closed a session with a viewer: %v", got)
	}
	if !p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("the session with a viewer is no longer tracked")
	}
}

// TestARecentlyActiveSessionIsKept: a session active a minute ago is inside a
// 30-minute timeout, so the sweep never asks the child about it.
func TestARecentlyActiveSessionIsKept(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, nil)
	p.fake.results["session/status"] = map[string]any{}
	now := time.Now()
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Minute))

	closed := p.fleet.sweepIdleSessions(ctlContext(t), now)

	if len(closed) != 0 {
		t.Fatalf("closed = %v, want nothing", closed)
	}
	if calls := p.fake.calls("session/status"); len(calls) != 0 {
		t.Fatalf("the sweep asked for the status of a recently active session: %v", calls)
	}
	if !p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("the recently active session is no longer tracked")
	}
}

// TestARunningSessionIsKept: a session with a run in flight is never ended,
// however long its last relayed frame is ago.
func TestARunningSessionIsKept(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, nil)
	p.fake.results["session/status"] = map[string]any{
		"run": map[string]any{"kind": "turn", "startedAt": time.Now().UnixMilli()},
	}
	now := time.Now()
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Hour))

	closed := p.fleet.sweepIdleSessions(ctlContext(t), now)

	if len(closed) != 0 {
		t.Fatalf("closed = %v, want nothing", closed)
	}
	if calls := p.fake.calls("session/status"); len(calls) != 1 {
		t.Fatalf("session/status calls = %v, want one", calls)
	}
	if got := closedSessions(p.fake); len(got) != 0 {
		t.Fatalf("the sweep closed a running session: %v", got)
	}
	if !p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("the running session is no longer tracked")
	}
}

// TestARunThatEndedRecentlyIsKept: a run that ended a minute ago gives the
// session the full timeout again, so it is kept.
func TestARunThatEndedRecentlyIsKept(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, nil)
	now := time.Now()
	p.fake.results["session/status"] = map[string]any{
		"lastRun": map[string]any{"endedAt": now.Add(-time.Minute).UnixMilli()},
	}
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Hour))

	closed := p.fleet.sweepIdleSessions(ctlContext(t), now)

	if len(closed) != 0 {
		t.Fatalf("closed = %v, want nothing", closed)
	}
	if got := closedSessions(p.fake); len(got) != 0 {
		t.Fatalf("the sweep closed a session whose run just ended: %v", got)
	}
	if !p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("the session is no longer tracked")
	}
}

// TestAnUnreadableStatusKeepsTheSession: a status the child refuses to answer
// keeps the session, because the sweep never closes what it could not read.
func TestAnUnreadableStatusKeepsTheSession(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, func(fake *fakeProjectChild) {
		fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
			if method == "session/status" {
				return nil, &rpcError{Code: -32000, Message: "status unavailable"}, true
			}
			return nil, nil, false
		}
	})
	now := time.Now()
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Hour))

	closed := p.fleet.sweepIdleSessions(ctlContext(t), now)

	if len(closed) != 0 {
		t.Fatalf("closed = %v, want nothing", closed)
	}
	if calls := p.fake.calls("session/status"); len(calls) != 1 {
		t.Fatalf("session/status calls = %v, want one", calls)
	}
	if got := closedSessions(p.fake); len(got) != 0 {
		t.Fatalf("the sweep closed a session whose status it could not read: %v", got)
	}
	if !p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("the unreadable session is no longer tracked")
	}
}

// TestCloseIfIdleRechecks: a viewer that arrives between the candidate list
// and the close keeps the session, because closeIfIdle rechecks under the
// registry's lock.
func TestCloseIfIdleRechecks(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, nil)
	now := time.Now()
	cutoff := now.Add(-30 * time.Minute)
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Hour))

	candidates := p.pc.reg.idleCandidates(cutoff)
	if len(candidates) != 1 || candidates[0] != idleSessionID {
		t.Fatalf("idle candidates = %v, want [%s]", candidates, idleSessionID)
	}
	// The attach that lands after the list was taken.
	if !p.pc.reg.attachViewer(idleSessionID) {
		t.Fatalf("session %s is not tracked", idleSessionID)
	}

	closed, err := p.pc.reg.closeIfIdle(ctlContext(t), idleSessionID, cutoff)
	if err != nil {
		t.Fatalf("closeIfIdle: %v", err)
	}
	if closed {
		t.Fatal("closeIfIdle closed a session a viewer attached to after the candidate list")
	}
	if got := closedSessions(p.fake); len(got) != 0 {
		t.Fatalf("the recheck still sent session/close for %v", got)
	}
	if !p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("the session with a viewer is no longer tracked")
	}
}

// TestAZeroTimeoutNeverSweeps: a zero timeout ends no session, and RunIdleSweep
// returns at once rather than sweeping on a ticker.
func TestAZeroTimeoutNeverSweeps(t *testing.T) {
	p := newIdleProject(t, 0, nil)
	p.fake.results["session/status"] = map[string]any{}
	now := time.Now()
	setIdleLastActive(t, p.pc.reg, idleSessionID, now.Add(-time.Hour))
	ctx := ctlContext(t)

	if closed := p.fleet.sweepIdleSessions(ctx, now); len(closed) != 0 {
		t.Fatalf("a zero timeout closed %v", closed)
	}
	if calls := p.fake.calls("session/status"); len(calls) != 0 {
		t.Fatalf("a zero timeout asked for a status: %v", calls)
	}
	if !p.pc.reg.sessionTracked(idleSessionID) {
		t.Fatal("a zero timeout ended the session")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.fleet.RunIdleSweep(ctx)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunIdleSweep did not return at once for a zero timeout")
	}
}

// TestTheSweepStopsWithTheFleet: RunIdleSweep ends when the fleet closes, not
// only when its context does.
func TestTheSweepStopsWithTheFleet(t *testing.T) {
	p := newIdleProject(t, 30*time.Minute, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.fleet.RunIdleSweep(context.Background())
	}()
	// Let the sweep arm its ticker before the fleet goes away, so this proves
	// the done channel ends it rather than the entry check.
	time.Sleep(50 * time.Millisecond)
	p.fleet.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunIdleSweep did not return after Fleet.Close")
	}
}
