package bridge

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// placementFleet builds a fleet the way the package's fleet tests do — the
// production agent factory intact — with a recording fake runner and a
// counting runtime probe that would answer a usable runtime. Placement alone
// decides what runs where.
func placementFleet(t *testing.T) (*Fleet, *fakeImages, *int) {
	t.Helper()
	imgs := newFakeImages()
	f := testFleetWithRunner(t, imgs.run)
	probes := 0
	f.probe = func() (path, name string, ok bool) {
		probes++
		return "/usr/local/bin/docker", "docker", true
	}
	return f, imgs, &probes
}

// TestPlainAgentsNeverProbeTheRuntime: a plain local agent runs on its
// project's child and, failing that, as a host process — never in a
// container. Nothing in either path asks the runtime probe, even though this
// fleet has one that would answer "docker", and nothing runs a runtime
// command. Only a workspace needs a runtime.
func TestPlainAgentsNeverProbeTheRuntime(t *testing.T) {
	f, imgs, probes := placementFleet(t)

	a := Agent{ID: "a1", Project: t.TempDir(), SourceKind: "local", Profile: DefaultRuntimeProfile()}
	if !f.onProjectChild(a) {
		t.Fatal("a plain local agent was not put on its project's child")
	}

	child, err := f.newRuntime(a)
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if child.Containerized {
		t.Fatal("a plain local agent got a containerized child")
	}

	ctl, err := f.buildControlChild()
	if err != nil {
		t.Fatalf("buildControlChild: %v", err)
	}
	if ctl.Containerized {
		t.Fatal("a plain agent's fleet built a containerized control child")
	}

	if *probes != 0 {
		t.Fatalf("the runtime probe ran %d times; a plain agent must never ask it", *probes)
	}
	imgs.mu.Lock()
	cmds := len(imgs.cmds)
	imgs.mu.Unlock()
	if cmds != 0 {
		t.Fatalf("the fake runner recorded %d command(s); a plain agent must run none", cmds)
	}
}

// TestNoRuntimeAnswers501ForContainerOnlyOperations: a bridge with no
// container runtime reports a limitation, not a gateway fault. Every
// container-only operation returns ErrWorkspaceNeedsRuntime — a workspace
// spawn, a workspace build (wsbuild.go) — and writeErr maps it to 501 rather
// than falling through to 502.
func TestNoRuntimeAnswers501ForContainerOnlyOperations(t *testing.T) {
	f := containerFleet(t)
	f.probe = func() (path, name string, ok bool) { return "", "", false }

	_, err := f.runtimeName()
	if err == nil {
		t.Fatal("a fleet with no runtime named one to build a workspace with")
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
