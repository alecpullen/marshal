package bridge

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContainerRunArgsCarryIsolationSettings(t *testing.T) {
	tr := newContainerTransport(ContainerConfig{
		Runtime:      "/usr/bin/docker",
		Image:        "marshal/agent:latest",
		Name:         "marshal-agent-7f3a",
		WorkspaceDir: "/srv/work/7f3a",
		SocketDir:    "/srv/sock/7f3a",
		CPUs:         2,
		MemoryMB:     4096,
	})
	args := tr.buildRunArgs()
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--name marshal-agent-7f3a",
		"--cpus 2",
		"--memory 4096m",
		"/srv/work/7f3a:/work",
		"/srv/sock/7f3a:/run/marshal",
		"marshal/agent:latest",
		"--listen unix:///run/marshal/agent.sock",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("run args missing %q\ngot: %s", want, joined)
		}
	}
}

func TestContainerRunArgsRefuseHostEscapes(t *testing.T) {
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", Image: "img", Name: "n",
		WorkspaceDir: "/w", SocketDir: "/s",
	})
	joined := strings.Join(tr.buildRunArgs(), " ")

	for _, forbidden := range []string{
		"--privileged",
		"--network host",
		"docker.sock",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("run args must never contain %q\ngot: %s", forbidden, joined)
		}
	}
}

func TestContainerEnvIsExplicitNotInherited(t *testing.T) {
	t.Setenv("MARSHAL_SECRET_TOKEN", "leaked")
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", Image: "img", Name: "n",
		WorkspaceDir: "/w", SocketDir: "/s",
		Env: map[string]string{"MARSHAL_ROLE": "agent"},
	})
	joined := strings.Join(tr.buildRunArgs(), " ")

	if strings.Contains(joined, "MARSHAL_SECRET_TOKEN") {
		t.Errorf("host environment leaked into container args: %s", joined)
	}
	if !strings.Contains(joined, "MARSHAL_ROLE=agent") {
		t.Errorf("explicit env missing from container args: %s", joined)
	}
}

func TestContainerNameForIsStableAndPrefixed(t *testing.T) {
	got := containerNameFor("7f3a")
	if got != "marshal-agent-7f3a" {
		t.Fatalf("containerNameFor(7f3a) = %q, want marshal-agent-7f3a", got)
	}
	if containerNameFor("7f3a") != got {
		t.Fatal("containerNameFor is not deterministic")
	}
}

func TestReattachFailsWhenSocketAbsent(t *testing.T) {
	dir := t.TempDir()
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", Image: "img",
		Name:      containerNameFor("missing"),
		SocketDir: dir,
	})
	tr.dialTimeout = 50 * time.Millisecond
	// No container is running, so no socket exists on the volume.
	if _, _, _, err := tr.Reattach(); err == nil {
		t.Fatal("Reattach to an absent socket succeeded, want error")
	}
}

func TestReattachHonoursInjectedTimeout(t *testing.T) {
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", Image: "img",
		Name:      containerNameFor("missing"),
		SocketDir: t.TempDir(),
	})
	tr.dialTimeout = 50 * time.Millisecond

	start := time.Now()
	if _, _, _, err := tr.Reattach(); err == nil {
		t.Fatal("Reattach to an absent socket succeeded, want error")
	}
	// Without the seam this waits the full reattachDialTimeout (3s).
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Reattach took %v; it ignored the injected dialTimeout", elapsed)
	}
}

func TestAgentIDRoundTripsThroughContainerName(t *testing.T) {
	for _, id := range []string{"7f3a", "abc-123", "x"} {
		got, ok := agentIDFromContainer(containerNameFor(id))
		if !ok || got != id {
			t.Errorf("round trip %q = (%q, %v), want (%q, true)", id, got, ok, id)
		}
	}
	if _, ok := agentIDFromContainer("some-other-container"); ok {
		t.Error("agentIDFromContainer claimed a foreign container")
	}
}

func TestContainerUsesInjectedRunner(t *testing.T) {
	var calls [][]string
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", Image: "img", Name: "marshal-agent-x",
		WorkspaceDir: "/w", SocketDir: t.TempDir(),
	})
	tr.run = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}

	if err := tr.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	got := strings.Join(calls[0], " ")
	if !strings.Contains(got, "rm -f marshal-agent-x") {
		t.Fatalf("Kill ran %q, want a force-remove of the container", got)
	}
}

func TestContainerReattachPreferredOverStart(t *testing.T) {
	// Use a short socket dir: unix socket paths are length-limited on
	// macOS (~104 bytes), and t.TempDir() paths are too long.
	dir, err := os.MkdirTemp("", "sock")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	defer os.RemoveAll(dir)
	// A listening socket stands in for a container that is already up.
	ln, err := net.Listen("unix", filepath.Join(dir, containerSocketName))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", Image: "img", Name: "marshal-agent-live",
		WorkspaceDir: "/w", SocketDir: dir,
	})
	var ran [][]string
	tr.run = func(name string, args ...string) ([]byte, error) {
		ran = append(ran, args)
		if len(args) > 0 && args[0] == "ps" {
			return []byte("marshal-agent-live\n"), nil
		}
		return nil, nil
	}

	stdin, stdout, _, err := tr.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = stdin.Close(); _ = stdout.Close() }()

	for _, args := range ran {
		if len(args) > 0 && args[0] == "run" {
			t.Fatal("Open started a new container when a live one existed; it must reattach")
		}
	}
}

func TestContainerKillsPartialStartOnDialFailure(t *testing.T) {
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", Image: "img", Name: "marshal-agent-dead",
		WorkspaceDir: "/w", SocketDir: t.TempDir(),
	})
	tr.dialTimeout = 50 * time.Millisecond // no socket will ever appear
	var killed bool
	tr.run = func(name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "rm" {
			killed = true
		}
		return nil, nil
	}

	if _, _, _, err := tr.Open(); err == nil {
		t.Fatal("Open succeeded with no agent listening, want error")
	}
	if !killed {
		t.Fatal("a container that never bound its socket was left running")
	}
}
