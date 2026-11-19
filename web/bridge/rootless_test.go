package bridge

import (
	"errors"
	"strings"
	"testing"
)

// rootlessFleet builds a fleet whose runtime probe answers docker, with the
// rootless answer injected, and — when hostState is set — the host-state
// rootless requirement set directly, so the test runs on any OS.
func rootlessFleet(t *testing.T, hostState, isRootless bool) (*Fleet, *int) {
	t.Helper()
	f := testFleet(t)
	f.probe = dockerProbe
	f.requireRootless = hostState
	asked := 0
	f.rootless = func() bool {
		asked++
		return isRootless
	}
	return f, &asked
}

// TestRootlessFromInfoReadsDocker reads Docker's SecurityOptions, the JSON
// array probeRuntime asks info for.
func TestRootlessFromInfoReadsDocker(t *testing.T) {
	rootless := []byte(`["name=seccomp,profile=builtin","name=cgroupns","name=rootless"]`)
	if !rootlessFromInfo("docker", rootless) {
		t.Fatalf("rootlessFromInfo(docker, %s) = false, want true", rootless)
	}
	rootful := []byte(`["name=seccomp,profile=builtin","name=cgroupns"]`)
	if rootlessFromInfo("docker", rootful) {
		t.Fatalf("rootlessFromInfo(docker, %s) = true, want false", rootful)
	}
}

// TestRootlessFromInfoReadsPodman reads Podman's Host.Security.Rootless,
// which it prints as a boolean.
func TestRootlessFromInfoReadsPodman(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"true\n", true},
		{"false\n", false},
		{"", false},
	} {
		if got := rootlessFromInfo("podman", []byte(tc.out)); got != tc.want {
			t.Errorf("rootlessFromInfo(podman, %q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}

// TestARootfulRuntimeIsRefusedForHostState: a host-state fleet on Linux
// refuses a rootful runtime, and every container-only operation says why.
func TestARootfulRuntimeIsRefusedForHostState(t *testing.T) {
	f, asked := rootlessFleet(t, true, false)

	if path, name, ok := f.containerRuntime(); ok || path != "" || name != "" {
		t.Fatalf("containerRuntime = %q, %q, %v; a rootful runtime must be refused for host state", path, name, ok)
	}
	if *asked == 0 {
		t.Fatal("the fleet never asked whether the runtime is rootless")
	}

	err := f.needsRuntime()
	if !errors.Is(err, ErrWorkspaceNeedsRuntime) {
		t.Fatalf("needsRuntime = %v, want it to wrap ErrWorkspaceNeedsRuntime", err)
	}
	if !strings.Contains(err.Error(), "runs as root") {
		t.Fatalf("needsRuntime = %v, want it to say the runtime runs as root", err)
	}
}

// TestARootlessRuntimeIsUsedForHostState: the same fleet with a rootless
// runtime keeps it.
func TestARootlessRuntimeIsUsedForHostState(t *testing.T) {
	f, asked := rootlessFleet(t, true, true)

	path, name, ok := f.containerRuntime()
	if !ok || path != "/usr/bin/docker" || name != "docker" {
		t.Fatalf("containerRuntime = %q, %q, %v; want the probe's answer", path, name, ok)
	}
	if *asked == 0 {
		t.Fatal("the fleet never asked whether the runtime is rootless")
	}

	err := f.needsRuntime()
	if !errors.Is(err, ErrWorkspaceNeedsRuntime) {
		t.Fatalf("needsRuntime = %v, want ErrWorkspaceNeedsRuntime", err)
	}
	if strings.Contains(err.Error(), "runs as root") {
		t.Fatalf("needsRuntime = %v; a rootless runtime must not be refused", err)
	}
}

// TestAVolumeStateFleetNeverAsksIfRootless: only a host-state fleet asks
// whether the runtime is rootless.
func TestAVolumeStateFleetNeverAsksIfRootless(t *testing.T) {
	f, asked := rootlessFleet(t, false, false)

	if path, name, ok := f.containerRuntime(); !ok || path != "/usr/bin/docker" || name != "docker" {
		t.Fatalf("containerRuntime = %q, %q, %v; a volume-state fleet uses the probe's answer", path, name, ok)
	}
	if err := f.needsRuntime(); err != ErrWorkspaceNeedsRuntime {
		t.Fatalf("needsRuntime = %v, want the bare ErrWorkspaceNeedsRuntime", err)
	}
	if *asked != 0 {
		t.Fatalf("a volume-state fleet asked whether the runtime is rootless %d time(s)", *asked)
	}
}
