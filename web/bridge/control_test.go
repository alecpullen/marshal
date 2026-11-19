package bridge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestControlStartsOnceAndReuses(t *testing.T) {
	agent := newFakeAgent()
	f := ctlFleet(t, agent)
	ctx := ctlContext(t)

	if _, err := f.controlCall(ctx, "config/get", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.controlCall(ctx, "session/skills_list", map[string]any{"scope": "global"}, true); err != nil {
		t.Fatal(err)
	}
	if n := len(agent.calls("initialize")); n != 1 {
		t.Fatalf("initialize sent %d times, want 1", n)
	}
	if n := len(agent.calls("session/new")); n != 1 {
		t.Fatalf("control session opened %d times, want 1", n)
	}
	if got := agent.calls("session/skills_list"); len(got) != 1 || !strings.Contains(got[0], `"sessionId":"s-1"`) {
		t.Fatalf("session call lacked the control session id: %v", got)
	}
	if got := agent.calls("config/get"); len(got) != 1 || strings.Contains(got[0], "sessionId") {
		t.Fatalf("a sessionless call carried a session id: %v", got)
	}
}

func TestControlRecordsCapabilities(t *testing.T) {
	agent := newFakeAgent()
	agent.caps = map[string]any{"configAccess": map[string]any{}}
	f := ctlFleet(t, agent)
	c, err := f.control(ctlContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if !f.controlHas(c, "configAccess") || f.controlHas(c, "watchAccess") {
		t.Fatalf("caps = %v", c.caps)
	}
}

func TestControlRestartsAfterTheAgentDies(t *testing.T) {
	old := restartBackoff
	restartBackoff = 10 * time.Millisecond
	t.Cleanup(func() { restartBackoff = old })

	agent := newFakeAgent()
	f := ctlFleet(t, agent)
	ctx := ctlContext(t)
	if _, err := f.controlCall(ctx, "config/get", nil, false); err != nil {
		t.Fatal(err)
	}
	agent.die()
	waitFor(t, 5*time.Second, "control agent to respawn", func() bool { return agent.generations() == 2 })
	// The restarted agent knows no sessions, so the next call must redo
	// the handshake before it is served.
	waitFor(t, 5*time.Second, "control to be marked not ready", func() bool {
		f.ctl.mu.Lock()
		defer f.ctl.mu.Unlock()
		return !f.ctl.ready
	})
	if _, err := f.controlCall(ctx, "session/skills_list", nil, true); err != nil {
		t.Fatal(err)
	}
	if n := len(agent.calls("initialize")); n != 2 {
		t.Fatalf("initialize sent %d times across a restart, want 2", n)
	}
	if n := len(agent.calls("session/new")); n != 2 {
		t.Fatalf("control session opened %d times across a restart, want 2", n)
	}
}

func TestControlMapsMethodNotFoundToUnsupported(t *testing.T) {
	agent := newFakeAgent()
	agent.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
		if method == "session/skills_list" {
			return nil, &rpcError{Code: -32601, Message: "method not found"}, true
		}
		return nil, nil, false
	}
	f := ctlFleet(t, agent)
	_, err := f.controlCall(ctlContext(t), "session/skills_list", nil, true)
	var un ErrUnsupported
	if !errors.As(err, &un) || un.Feature != "skills_list" {
		t.Fatalf("err = %v, want ErrUnsupported{skills_list}", err)
	}
}

// TestTheControlAgentIsAHostProcess: the control agent has no workspace, and
// a workspace is what makes a container, so even a fleet whose probe answers
// Docker builds it as a host process.
func TestTheControlAgentIsAHostProcess(t *testing.T) {
	f := testFleetWithRunner(t, func(string, ...string) ([]byte, error) { return nil, nil })
	f.probe = dockerProbe

	child, err := f.buildControlChild()
	if err != nil {
		t.Fatalf("buildControlChild: %v", err)
	}
	if child.Containerized {
		t.Fatal("the control agent's child is containerized; it is a host process")
	}
	if child.Transport != nil {
		t.Fatalf("the control agent's child has a %T transport, want the host-process default", child.Transport)
	}
	if child.MarshalBin != f.marshalBin {
		t.Fatalf("MarshalBin = %q, want %q", child.MarshalBin, f.marshalBin)
	}
}

func TestProjectSessionIsCachedPerRoot(t *testing.T) {
	agent := newFakeAgent()
	f := ctlFleet(t, agent)
	ctx := ctlContext(t)
	a1, err := f.projectSession(ctx, "/home/u/a")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := f.projectSession(ctx, "/home/u/a/")
	b, _ := f.projectSession(ctx, "/home/u/b")
	if a1 != a2 || a1 == b {
		t.Fatalf("sessions: a=%s a'=%s b=%s", a1, a2, b)
	}
	// Control session plus two project sessions.
	if n := len(agent.calls("session/new")); n != 3 {
		t.Fatalf("session/new sent %d times, want 3", n)
	}
	if got := agent.calls("session/new")[1]; !strings.Contains(got, `"cwd":"/home/u/a"`) {
		t.Fatalf("project session cwd wrong: %s", got)
	}
}
