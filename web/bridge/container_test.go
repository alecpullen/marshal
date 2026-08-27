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
