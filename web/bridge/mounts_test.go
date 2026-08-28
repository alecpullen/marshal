package bridge

import (
	"strings"
	"testing"
)

func TestVolumeMountUsesDockerSyntax(t *testing.T) {
	got := strings.Join(volumeMount("docker", "marshal-state", "/work", "work/a1"), " ")
	if !strings.Contains(got, "type=volume") || !strings.Contains(got, "source=marshal-state") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "volume-subpath=work/a1") {
		t.Fatalf("docker spells it volume-subpath=; got %q", got)
	}
}

// Podman documents the same feature as `subpath=` (podman-run(1)).
func TestVolumeMountUsesPodmanSyntax(t *testing.T) {
	got := strings.Join(volumeMount("podman", "marshal-state", "/work", "work/a1"), " ")
	if !strings.Contains(got, "subpath=work/a1") {
		t.Fatalf("podman spells it subpath=; got %q", got)
	}
	if strings.Contains(got, "volume-subpath=") {
		t.Fatalf("docker syntax leaked into the podman path: %q", got)
	}
}

func TestBuildRunArgsUsesVolumesNotBindMounts(t *testing.T) {
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", RuntimeName: "docker", Image: "img", Name: "n",
		StateVolume: "marshal-state", WorkSubpath: "work/a1", SocketSubpath: "sockets/a1",
	})
	joined := strings.Join(tr.buildRunArgs(), " ")

	if strings.Contains(joined, "-v /") {
		t.Fatalf("a host bind mount survived; a containerized bridge cannot use one:\n%s", joined)
	}
	for _, want := range []string{"volume-subpath=work/a1", "volume-subpath=sockets/a1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

// The isolation property that replaces filesystem separation.
func TestAgentSubpathsAreDistinct(t *testing.T) {
	a := strings.Join(volumeMount("docker", "v", "/work", "work/a1"), " ")
	b := strings.Join(volumeMount("docker", "v", "/work", "work/a2"), " ")
	if a == b {
		t.Fatal("two agents produced identical mount arguments")
	}
}

func TestTranslateToHostMapsADeclaredRoot(t *testing.T) {
	m := []ProjectMount{{Host: "/Users/you/code", Container: "/host-projects"}}
	got, err := TranslateToHost(m, "/host-projects/marshal")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/you/code/marshal" {
		t.Fatalf("got %q, want the HOST path", got)
	}
}

func TestTranslateToHostRefusesAnUndeclaredPath(t *testing.T) {
	m := []ProjectMount{{Host: "/Users/you/code", Container: "/host-projects"}}
	_, err := TranslateToHost(m, "/somewhere/else")
	if err == nil {
		t.Fatal("an undeclared path was accepted; the spawn would fail later with mounts denied")
	}
	if !strings.Contains(err.Error(), "project-mount") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

// A prefix match must respect path boundaries.
func TestTranslateToHostDoesNotMatchAPartialSegment(t *testing.T) {
	m := []ProjectMount{{Host: "/h", Container: "/host-projects"}}
	if _, err := TranslateToHost(m, "/host-projects-evil/x"); err == nil {
		t.Fatal("matched a path that merely shares a prefix with the declared root")
	}
}

func TestLocalPathAgentBindMountsTheHostPath(t *testing.T) {
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", RuntimeName: "docker", Image: "img", Name: "n",
		StateVolume: "marshal-state", SocketSubpath: "sockets/a1",
		LocalMount: "/Users/you/code/marshal",
	})
	joined := strings.Join(tr.buildRunArgs(), " ")

	if !strings.Contains(joined, "-v /Users/you/code/marshal:"+containerWorkDir) {
		t.Fatalf("the host checkout was not bind-mounted at /work:\n%s", joined)
	}
	// The socket still comes from the volume even for a local agent.
	if !strings.Contains(joined, "volume-subpath=sockets/a1") {
		t.Fatalf("the socket subpath is missing:\n%s", joined)
	}
	// And no workspace subpath: a local agent works on the checkout.
	if strings.Contains(joined, "volume-subpath=work/") {
		t.Fatalf("a local agent was given a workspace subpath as well:\n%s", joined)
	}
}

func TestLocalAndGitAgentsDifferOnlyInTheWorkspaceMount(t *testing.T) {
	base := ContainerConfig{
		Runtime: "/usr/bin/docker", RuntimeName: "docker", Image: "img", Name: "n",
		StateVolume: "marshal-state", SocketSubpath: "sockets/a1",
	}
	gitCfg := base
	gitCfg.WorkSubpath = "work/a1"
	localCfg := base
	localCfg.LocalMount = "/host/checkout"

	gitArgs := strings.Join(newContainerTransport(gitCfg).buildRunArgs(), " ")
	localArgs := strings.Join(newContainerTransport(localCfg).buildRunArgs(), " ")

	for _, shared := range []string{"--name n", "volume-subpath=sockets/a1", "img"} {
		if !strings.Contains(gitArgs, shared) || !strings.Contains(localArgs, shared) {
			t.Errorf("%q should appear in both argument vectors", shared)
		}
	}
}
