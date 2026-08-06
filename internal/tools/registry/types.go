package registry

import (
	"context"
	"encoding/json"
)

type RiskLevel string

const (
	RiskReadOnly       RiskLevel = "read_only"
	RiskWorkspaceWrite RiskLevel = "workspace_write"
	RiskCommand        RiskLevel = "command"
	RiskNetwork        RiskLevel = "network"
	RiskDestructive    RiskLevel = "destructive"
	// RiskEnvironment marks commands that mutate state OUTSIDE the
	// working directory (build/package caches, global config, system
	// package managers). It always requires explicit approval, even in
	// auto-approve modes: those modes own the workspace, not the machine.
	RiskEnvironment RiskLevel = "environment"
)

func (r RiskLevel) Valid() bool {
	switch r {
	case RiskReadOnly, RiskWorkspaceWrite, RiskCommand, RiskNetwork, RiskDestructive:
		return true
	default:
		return false
	}
}

type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Risk        RiskLevel
	Cacheable   bool
	Deferred    bool
	Handler     ToolHandler

	// validator is the compiled form of Schema, built once by Register.
	// Shared by pointer through cloneTool: jsonschema.Schema is immutable
	// after compilation and safe for concurrent use.
	validator *Validator
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

type ToolResult struct {
	Summary         string
	Content         string
	Error           string
	FilesChanged    []string
	CommandExitCode *int
	Sandbox         SandboxMeta
}

// SandboxMeta records how a command was executed under the Milestone Q
// sandbox. It is populated by sandbox backends (restricted, container,
// passthrough) and persisted into the audit trail so users can see, per
// command, which backend ran it and whether network/filesystem were
// actually isolated. Honest capability reporting matters more than
// claiming isolation that isn't enforced (e.g. restricted mode cannot
// block network cross-platform).
type SandboxMeta struct {
	Enabled            bool
	Backend            string
	NetworkIsolated    bool
	FilesystemIsolated bool
	ResourceLimits     bool
	MemoryLimitBytes   int64
	CPUSeconds         int
	MaxProcesses       int
	KilledReason       string
	DurationMS         int64
	OutputTruncated    bool
}

func (m SandboxMeta) LimitsJSON() (string, error) {
	limits := map[string]any{"backend": m.Backend}
	if m.MemoryLimitBytes > 0 {
		limits["memory_limit_bytes"] = m.MemoryLimitBytes
	}
	if m.CPUSeconds > 0 {
		limits["cpu_seconds"] = m.CPUSeconds
	}
	if m.MaxProcesses > 0 {
		limits["max_processes"] = m.MaxProcesses
	}
	if m.NetworkIsolated {
		limits["network_isolated"] = true
	}
	if m.FilesystemIsolated {
		limits["filesystem_isolated"] = true
	}
	if m.KilledReason != "" {
		limits["killed_reason"] = m.KilledReason
	}
	if m.OutputTruncated {
		limits["output_truncated"] = true
	}
	b, err := json.Marshal(limits)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
