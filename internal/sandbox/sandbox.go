// Package sandbox provides isolated command execution backends for the
// Milestone Q shell execution feature. A Sandbox implements the
// native.CommandRunner interface so it slots directly into the existing
// tool execution path (see internal/tools/native/runner.go and
// internal/app/app.go).
//
// Three backends are supported:
//
//   - passthrough: no isolation (pre-Milestone-Q behavior)
//   - restricted:  in-process hardening — env allowlist scrubbing, resource
//     limits via ulimit/rlimit, cwd confinement, process-group kill on
//     timeout. The default backend.
//   - container:   Docker/Podman based isolation — real network isolation,
//     filesystem isolation, and hard memory/cpu limits. Falls back to
//     restricted when no container runtime is available (with a warning).
//
// Capability reporting is honest: restricted mode cannot block network
// cross-platform, so it reports NetworkIsolated=false; only container mode
// reports NetworkIsolated=true. The audit layer records these flags per
// command so users never receive a false sense of isolation.
package sandbox

import (
	"errors"
	"fmt"
	"log/slog"

	"marshal/internal/app/config"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

// Capabilities advertises what a backend can actually enforce. Used by the
// TUI/audit to describe isolation honestly.
type Capabilities struct {
	Backend             string
	ResourceLimits      bool
	NetworkIsolation    bool
	FilesystemIsolation bool
}

// Config is the resolved sandbox configuration derived from config.SandboxConfig.
type Config struct {
	Backend          string
	AllowNetwork     bool
	MemoryLimitMB    int
	CPUSeconds       int
	MaxProcesses     int
	FileSizeLimitMB  int
	ContainerRuntime string
	ContainerImage   string
	// AllowFallback controls whether the container backend may silently
	// downgrade to restricted when no container runtime is available.
	// Default false: missing runtime is a hard error. See sandbox.New.
	AllowFallback     bool
	UnsafePassthrough bool // opt-in required for backend=passthrough (no isolation)
	EnvAllowlist      []string
	EnvDenylist       []string
}

// FromConfig builds a Config from a ShellToolConfig, carrying the AllowNetwork
// flag (which lives on the shell config, not the sandbox subsection).
func FromConfig(shell config.ShellToolConfig) Config {
	sb := shell.Sandbox
	return Config{
		Backend:           sb.Backend,
		AllowNetwork:      shell.AllowNetwork,
		MemoryLimitMB:     sb.MemoryLimitMB,
		CPUSeconds:        sb.CPUSeconds,
		MaxProcesses:      sb.MaxProcesses,
		FileSizeLimitMB:   sb.FileSizeLimitMB,
		ContainerRuntime:  sb.ContainerRuntime,
		ContainerImage:    sb.ContainerImage,
		AllowFallback:     sb.AllowFallback,
		UnsafePassthrough: sb.UnsafePassthrough,
		EnvAllowlist:      sb.EnvAllowlist,
		EnvDenylist:       sb.EnvDenylist,
	}
}

// Sandbox executes a command under isolation and reports how it was run.
// Implementations satisfy native.CommandRunner.
type Sandbox interface {
	native.CommandRunner
	// Capabilities reports what this backend actually enforces.
	Capabilities() Capabilities
}

// New selects and constructs a backend from cfg. Unknown backends error.
// A container backend with no detected runtime falls back to restricted
// ONLY when cfg.AllowFallback=true — otherwise startup hard-fails so the
// user's isolation policy is not silently waived. The rationale is that a
// silent downgrade turns a container-isolated command (where network is
// blocked and filesystem is mount-namespaced) into a restricted command
// (where the parent env's secrets, host networking, etc. remain live).
func New(cfg Config, logger *slog.Logger) (Sandbox, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = "restricted"
	}

	switch backend {
	case "passthrough":
		if !cfg.UnsafePassthrough {
			return nil, errors.New("sandbox: backend=passthrough requires unsafe_passthrough=true (no isolation)")
		}
		if logger != nil {
			logger.Warn("sandbox: running in PASSTHROUGH mode — no isolation")
		}
		return &Passthrough{logger: logger}, nil
	case "restricted":
		return newRestricted(cfg, logger), nil
	case "container":
		if name, absPath, ok := detectRuntime(cfg.ContainerRuntime); ok {
			return newContainer(cfg, name, absPath, logger), nil
		}
		if !cfg.AllowFallback {
			return nil, errors.New("sandbox: container backend requested but no container runtime detected on PATH; install docker/podman or set allow_fallback = true (note: fallback silently drops network/filesystem containment)")
		}
		if logger != nil {
			logger.Warn("sandbox: container backend requested but no container runtime detected; falling back to restricted (allow_fallback=true was set)")
		}
		cfg.Backend = "restricted"
		return newRestricted(cfg, logger), nil
	default:
		return nil, fmt.Errorf("sandbox: unknown backend %q", backend)
	}
}

// metaFor builds a SandboxMeta for the backend, recording applied limits and
// capabilities. Backends call this after execution, filling KilledReason and
// DurationMS.
func metaFor(caps Capabilities, cfg Config) registry.SandboxMeta {
	memBytes := int64(cfg.MemoryLimitMB) * 1024 * 1024
	if cfg.MemoryLimitMB <= 0 {
		memBytes = 0
	}
	// Honest reporting: the restricted backend cannot enforce address-space
	// caps where ulimit -v is unsupported (darwin, windows) — record 0
	// rather than a phantom limit the audit log would show as enforced.
	if caps.Backend == "restricted" && !ulimitSupportsMem() {
		memBytes = 0
	}
	// Honest reporting: only emit limits the backend actually enforces.
	// The restricted backend applies MaxProcesses via ulimit -u; the
	// container backend does not pass --pids-limit, so recording it would
	// be a phantom isolation claim.
	maxProcesses := cfg.MaxProcesses
	if caps.Backend == "container" {
		maxProcesses = 0
	}

	return registry.SandboxMeta{
		Enabled:            true,
		Backend:            caps.Backend,
		NetworkIsolated:    caps.NetworkIsolation && !cfg.AllowNetwork,
		FilesystemIsolated: caps.FilesystemIsolation,
		ResourceLimits:     caps.ResourceLimits,
		MemoryLimitBytes:   memBytes,
		CPUSeconds:         cfg.CPUSeconds,
		MaxProcesses:       maxProcesses,
	}
}
