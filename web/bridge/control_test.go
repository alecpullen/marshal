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

func TestControlContainerConfigUsesControlName(t *testing.T) {
	f := testFleetWithProjectMounts(t, []ProjectMount{{Host: "/host/projects", Container: "/projects"}})
	cfg, bound := f.controlContainerConfig("/usr/bin/docker", "docker")
	tr := newContainerTransport(cfg)
	joined := strings.Join(tr.buildRunArgs(), " ")
	for _, want := range []string{
		"--name marshal-control",
		"target=/marshal/config,volume-subpath=home/config --mount",
		"-v /host/projects:/projects",
		"volume-subpath=control",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("run args missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "home/config,readonly") {
		t.Errorf("control config home must be writable:\n%s", joined)
	}
	if len(bound) != 1 || bound[0] != "/projects" {
		t.Errorf("bound = %v", bound)
	}
	if strings.HasPrefix(controlContainerName, containerNamePrefix) {
		t.Error("control container name must not match the agent prefix")
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

func TestProjectSessionRefusesAnUnmountedRootInContainerMode(t *testing.T) {
	f := ctlFleet(t, newFakeAgent())
	if _, err := f.control(ctlContext(t)); err != nil {
		t.Fatal(err)
	}
	f.ctl.mu.Lock()
	f.ctl.containerized, f.ctl.boundRoots = true, []string{"/projects"}
	f.ctl.mu.Unlock()

	if _, err := f.projectSession(ctlContext(t), "/projects/a"); err != nil {
		t.Fatalf("a mounted root was refused: %v", err)
	}
	_, err := f.projectSession(ctlContext(t), "/projects-evil/a")
	var un ErrUnsupported
	if !errors.As(err, &un) || un.Feature != "project_library" {
		t.Fatalf("err = %v, want ErrUnsupported{project_library}", err)
	}
}
