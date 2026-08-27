package bridge

import (
	"strings"
	"testing"
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
	// No container is running, so no socket exists on the volume.
	if _, _, _, err := tr.Reattach(); err == nil {
		t.Fatal("Reattach to an absent socket succeeded, want error")
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
