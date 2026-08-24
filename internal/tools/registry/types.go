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
	case RiskReadOnly, RiskWorkspaceWrite, RiskCommand, RiskNetwork, RiskDestructive, RiskEnvironment:
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

// SymbolRef identifies one symbol a tool call touched.
//
// Line and Col are the position of the symbol's *name*, in LSP's 0-based
// convention, so a references query can be issued without re-deriving it.
// Resolved is false when the position could not be determined; consumers
// must then use the name for display only and must NOT issue a query — a
// query at a guessed position returns confidently wrong results.
type SymbolRef struct {
	File     string
	Name     string
	Kind     string // "function", "method", "type"
	Receiver string // e.g. "*Scanner"; empty for non-methods
	Line     int    // 0-based (LSP)
	Col      int    // 0-based
	Resolved bool
}

type ToolResult struct {
	Summary         string
	Content         string
	Error           string
	FilesChanged    []string
	CommandExitCode *int
	Sandbox         SandboxMeta
	// Symbols are the symbols this call touched, when the tool and the
	// file's language support attribution. Always safe to be empty.
	Symbols []SymbolRef
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
