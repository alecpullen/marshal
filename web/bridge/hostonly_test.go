package bridge

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// hostOnlyFleet builds a fleet the way the package's fleet tests do — the
// production agent factory intact — with a recording fake runner and a
// counting runtime probe, then makes it host-only.
func hostOnlyFleet(t *testing.T) (*Fleet, *fakeImages, *int) {
	t.Helper()
	imgs := newFakeImages()
	f := testFleetWithRunner(t, imgs.run)
	probes := 0
	f.probe = func() (path, name string, ok bool) {
		probes++
		return "/usr/local/bin/docker", "docker", true
	}
	f.SetHostProcessesOnly()
	return f, imgs, &probes
}

// TestAHostOnlyFleetNeverProbes is decision 4: a host-only fleet reports
// no container runtime from every caller, builds host-process children for
// agents and the control agent, and never asks the probe or runs a runtime
// command — even though a fake runner is installed that would otherwise
// answer "docker".
func TestAHostOnlyFleetNeverProbes(t *testing.T) {
	f, imgs, probes := hostOnlyFleet(t)

	if path, name, ok := f.containerRuntime(); ok || path != "" || name != "" {
		t.Fatalf("containerRuntime = %q, %q, %v; a host-only fleet has no runtime", path, name, ok)
	}
	if name, ok := f.egressRuntimeName(); ok || name != "" {
		t.Fatalf("egressRuntimeName = %q, %v; a host-only fleet has no runtime", name, ok)
	}
	if name, err := f.runtimeName(); err == nil {
		t.Fatalf("runtimeName = %q, nil; a host-only fleet has no runtime to name", name)
	}
	if path, name, ok := f.runtimeInfo(); ok || path != "" || name != "" {
		t.Fatalf("runtimeInfo = %q, %q, %v; a host-only fleet has no runtime", path, name, ok)
	}

	child, err := f.newRuntime(Agent{ID: "a1", Project: t.TempDir(), SourceKind: "local", Profile: DefaultRuntimeProfile()})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if child.Containerized {
		t.Fatal("a host-only fleet built a containerized agent child")
	}

	ctl, _, err := f.buildControlChild()
	if err != nil {
		t.Fatalf("buildControlChild: %v", err)
	}
	if ctl.Containerized {
		t.Fatal("a host-only fleet built a containerized control child")
	}

	if *probes != 0 {
		t.Fatalf("the runtime probe ran %d times; a host-only fleet must never probe", *probes)
	}
	imgs.mu.Lock()
	cmds := len(imgs.cmds)
	imgs.mu.Unlock()
	if cmds != 0 {
		t.Fatalf("the fake runner recorded %d command(s); a host-only fleet must run none", cmds)
	}
}

// TestAHostOnlyFleetAnswers501ForContainerOnlyOperations: a bridge started
// without a container runtime reports a limitation, not a gateway fault.
// Every container-only operation returns ErrWorkspaceNeedsRuntime — a
// workspace spawn, a workspace build (wsbuild.go) — and writeErr maps it to
// 501 rather than falling through to 502.
func TestAHostOnlyFleetAnswers501ForContainerOnlyOperations(t *testing.T) {
	f, _, _ := hostOnlyFleet(t)

	_, err := f.runtimeName()
	if err == nil {
		t.Fatal("a host-only fleet named a runtime to build a workspace with")
	}
	if !errors.Is(err, ErrWorkspaceNeedsRuntime) {
		t.Fatalf("runtimeName error = %v, want it to wrap ErrWorkspaceNeedsRuntime", err)
	}

	rec := httptest.NewRecorder()
	writeErr(rec, fmt.Errorf("build workspace w1: %w", err))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body = %s", rec.Code, rec.Body.String())
	}
}

func TestAFleetProbesOnceByDefault(t *testing.T) {
	f := testFleetWithRunner(t, func(string, ...string) ([]byte, error) { return nil, nil })
	probes := 0
	f.probe = func() (path, name string, ok bool) {
		probes++
		return "/usr/local/bin/docker", "docker", true
	}

	path, name, ok := f.containerRuntime()
	if !ok || path != "/usr/local/bin/docker" || name != "docker" {
		t.Fatalf("containerRuntime = %q, %q, %v; want the probe's answer", path, name, ok)
	}
	if probes != 1 {
		t.Fatalf("the runtime probe ran %d times; want exactly 1", probes)
	}
}
