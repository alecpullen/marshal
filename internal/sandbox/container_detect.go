package sandbox

import (
	"context"
	"os/exec"
	"time"
)

// probeTimeout caps the runtime detection daemon probe so app startup does
// not hang for long when the docker/podman daemon is unreachable.
const probeTimeout = 4 * time.Second

// detectRuntime finds an available container runtime and pins its absolute
// path. A configured value of "auto" (or empty) probes docker then podman;
// "docker"/"podman" are verified explicitly. Returns the runtime name
// (for user-facing messages) and its absolute executable path (pinned at
// detect time, so a later PATH-reorder or planted `docker`/`podman` shim
// cannot substitute a different binary at exec time) and ok.
//
// Probe = LookPath (binary present, absolute-path pinned) + `<rt> info`
// (daemon reachable). The caller (sandbox.New) returns an error unless
// AllowFallback is set when no runtime is available.
func detectRuntime(cfg string) (name string, absPath string, ok bool) {
	candidates := []string{cfg}
	if cfg == "" || cfg == "auto" {
		candidates = []string{"docker", "podman"}
	}
	for _, c := range candidates {
		if c == "" || c == "auto" {
			continue
		}
		abs, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		probe := exec.CommandContext(ctx, abs, "info")
		probe.Stdout = nil
		probe.Stderr = nil
		if err := probe.Run(); err == nil {
			cancel()
			return c, abs, true
		}
		cancel()
	}
	return "", "", false
}
