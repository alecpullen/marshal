package acp

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/commands"
)

// CommandRuntime is the per-session slice of state CommandManager needs.
type CommandRuntime struct {
	State    *session.State
	Registry *commands.Registry
}

// CommandLookup returns the runtime registered for an ACP session id.
type CommandLookup func(sessionID string) (*CommandRuntime, bool)

// ActiveTurnCheck reports whether sessionID currently has an in-flight
// prompt turn. session/command is rejected while true, matching the TUI's
// disablement of command dispatch while a turn is running.
type ActiveTurnCheck func(sessionID string) bool

// CommandManagerConfig wires a CommandManager to external dependencies.
type CommandManagerConfig struct {
	Lookup    CommandLookup
	HasActive ActiveTurnCheck
}

// CommandManager dispatches session/command and session/command_list
// against a session's commands.Registry.
type CommandManager struct {
	lookup    CommandLookup
	hasActive ActiveTurnCheck
}

func NewCommandManager(cfg CommandManagerConfig) *CommandManager {
	if cfg.Lookup == nil {
		panic("acp: CommandManagerConfig.Lookup is required")
	}
	if cfg.HasActive == nil {
		panic("acp: CommandManagerConfig.HasActive is required")
	}
	return &CommandManager{lookup: cfg.Lookup, hasActive: cfg.HasActive}
}

// kindOf classifies a command for the session/command_list wire result.
// "headless" commands have a real Handler and can be run via
// session/command. "tui_only" commands are interactive and rejected by
// session/command. "prompt" commands have no Handler — a client runs them
// by sending PromptBody as a normal session/prompt instead.
func kindOf(cmd commands.Command) string {
	switch {
	case cmd.TUIOnly:
		return "tui_only"
	case cmd.PromptBody != "":
		return "prompt"
	default:
		return "headless"
	}
}

// CommandInfo is one entry in the session/command_list result.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Args        string `json:"args,omitempty"`
	Group       string `json:"group,omitempty"`
	Kind        string `json:"kind"`
}

// CommandListResult is the JSON-RPC result for session/command_list.
type CommandListResult struct {
	Commands []CommandInfo `json:"commands"`
}

type commandListParams struct {
	SessionID string `json:"sessionId"`
}

// CommandList handles session/command_list.
func (m *CommandManager) CommandList(ctx context.Context, params json.RawMessage) (any, error) {
	var p commandListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/command_list params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, invalidParamsError("session/command_list requires sessionId")
	}

	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.Registry == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no command registry"}
	}

	cmds := rt.Registry.ListAll()
	out := make([]CommandInfo, len(cmds))
	for i, c := range cmds {
		out[i] = CommandInfo{
			Name:        c.Name,
			Description: c.Description,
			Args:        c.Args,
			Group:       c.Group,
			Kind:        kindOf(c),
		}
	}
	return CommandListResult{Commands: out}, nil
}
