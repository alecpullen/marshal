package bridge

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBindMountSpellsTheSourceAndTarget(t *testing.T) {
	got := bindMount("/host/state", "/marshal/config", false)
	want := []string{"--mount", "type=bind,source=/host/state,target=/marshal/config"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %q, want %q", got, want)
	}
	ro := bindMount("/host/state", "/marshal/config", true)
	if len(ro) != 2 || !strings.HasSuffix(ro[1], ",readonly") {
		t.Fatalf("a readonly bind lacks the suffix: %q", ro)
	}
}

func TestStateMountBindsUnderTheStateDir(t *testing.T) {
	const stateDir = "/host/state"
	got := strings.Join(stateMount(stateDir, "/work", "work/a1", false), " ")
	want := "type=bind,source=" + filepath.Join(stateDir, "work/a1") + ",target=/work"
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want it to carry %q", got, want)
	}
	if strings.Contains(got, "type=volume") {
		t.Fatalf("a state mount named a volume: %q", got)
	}
}

func TestStateMountIsReadOnlyWhenAsked(t *testing.T) {
	got := strings.Join(stateMount("/host/state", "/marshal/config", "home/config", true), " ")
	if !strings.HasSuffix(got, ",readonly") {
		t.Fatalf("a readonly state mount lacks the suffix: %q", got)
	}
}

func TestBuildRunArgsBindsTheState(t *testing.T) {
	const stateDir = "/host/state"
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", RuntimeName: "docker", Image: "img", Name: "n",
		StateDir: stateDir, WorkSubpath: "work/a1", SocketSubpath: "sockets/a1",
	})
	joined := strings.Join(tr.buildRunArgs(), " ")

	for _, want := range []string{
		"type=bind,source=" + filepath.Join(stateDir, "work/a1") + ",target=" + containerWorkDir,
		"type=bind,source=" + filepath.Join(stateDir, "sockets/a1") + ",target=" + containerSocketDir,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

// The isolation property that replaces filesystem separation.
func TestAgentSubpathsAreDistinct(t *testing.T) {
	a := strings.Join(stateMount("/host/state", "/work", "work/a1", false), " ")
	b := strings.Join(stateMount("/host/state", "/work", "work/a2", false), " ")
	if a == b {
		t.Fatal("two agents produced identical mount arguments")
	}
}

func TestLocalPathAgentBindMountsTheHostPath(t *testing.T) {
	stateDir := t.TempDir()
	tr := newContainerTransport(ContainerConfig{
		Runtime: "/usr/bin/docker", RuntimeName: "docker", Image: "img", Name: "n",
		StateDir: stateDir, SocketSubpath: "sockets/a1",
		LocalMount: "/Users/you/code/marshal",
	})
	joined := strings.Join(tr.buildRunArgs(), " ")

	if !strings.Contains(joined, "-v /Users/you/code/marshal:"+containerWorkDir) {
		t.Fatalf("the host checkout was not bind-mounted at /work:\n%s", joined)
	}
	// The socket still comes from the state dir even for a local agent.
	if !strings.Contains(joined, "source="+filepath.Join(stateDir, "sockets/a1")) {
		t.Fatalf("the socket subpath is missing:\n%s", joined)
	}
	// And no workspace subpath: a local agent works on the checkout.
	if strings.Contains(joined, "source="+filepath.Join(stateDir, "work/")) {
		t.Fatalf("a local agent was given a workspace subpath as well:\n%s", joined)
	}
}

func TestLocalAndGitAgentsDifferOnlyInTheWorkspaceMount(t *testing.T) {
	stateDir := t.TempDir()
	base := ContainerConfig{
		Runtime: "/usr/bin/docker", RuntimeName: "docker", Image: "img", Name: "n",
		StateDir: stateDir, SocketSubpath: "sockets/a1",
	}
	gitCfg := base
	gitCfg.WorkSubpath = "work/a1"
	localCfg := base
	localCfg.LocalMount = "/host/checkout"

	gitArgs := strings.Join(newContainerTransport(gitCfg).buildRunArgs(), " ")
	localArgs := strings.Join(newContainerTransport(localCfg).buildRunArgs(), " ")

	for _, shared := range []string{"--name n", "source=" + filepath.Join(stateDir, "sockets/a1"), "img"} {
		if !strings.Contains(gitArgs, shared) || !strings.Contains(localArgs, shared) {
			t.Errorf("%q should appear in both argument vectors", shared)
		}
	}
	// The differentiator: the git agent mounts a workspace subpath, the
	// local agent bind-mounts the host checkout.
	if !strings.Contains(gitArgs, "source="+filepath.Join(stateDir, "work/a1")) {
		t.Errorf("git agent missing workspace subpath: %s", gitArgs)
	}
	if !strings.Contains(localArgs, "-v /host/checkout:"+containerWorkDir) {
		t.Errorf("local agent missing bind mount: %s", localArgs)
	}
}
