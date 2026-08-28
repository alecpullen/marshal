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
