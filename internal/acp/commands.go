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
// session/command. "tui_only" commands are interactive; those without a
// Handler are rejected by session/command — see Command.
// "prompt" commands have no Handler — a client runs them by sending
// PromptBody as a normal session/prompt instead.
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

// CommandParams is the JSON-RPC body for session/command.
type CommandParams struct {
	SessionID string   `json:"sessionId"`
	Name      string   `json:"name"`
	Args      []string `json:"args,omitempty"`
}

// CommandResult is the JSON-RPC result for session/command.
type CommandResult struct {
	Text string   `json:"text,omitempty"`
	Doc  *WireDoc `json:"doc,omitempty"`
}

// WireDoc and WireRow mirror commands.Doc and commands.Row for JSON
// transport. Row.Action is a Go closure and has no wire representation;
// a row that carries an Action in-process is transmitted with ActionLabel
// as plain informational text — invoking it over ACP is not supported.
type WireDoc struct {
	Title     string    `json:"title,omitempty"`
	FullFrame bool      `json:"fullFrame,omitempty"`
	Footer    string    `json:"footer,omitempty"`
	Rows      []WireRow `json:"rows,omitempty"`
}

type WireRow struct {
	Header      string    `json:"header,omitempty"`
	Text        string    `json:"text,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Desc        string    `json:"desc,omitempty"`
	ActionLabel string    `json:"actionLabel,omitempty"`
	Children    []WireRow `json:"children,omitempty"`
}

func toWireDoc(d *commands.Doc) *WireDoc {
	if d == nil {
		return nil
	}
	return &WireDoc{
		Title:     d.Title,
		FullFrame: d.FullFrame,
		Footer:    d.Footer,
		Rows:      toWireRows(d.Rows),
	}
}

func toWireRows(rows []commands.Row) []WireRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]WireRow, len(rows))
	for i, r := range rows {
		out[i] = WireRow{
			Header:      r.Header,
			Text:        r.Text,
			Detail:      r.Detail,
			Desc:        r.Desc,
			ActionLabel: r.ActionLabel,
			Children:    toWireRows(r.Children),
		}
	}
	return out
}

// Command handles session/command. It rejects unknown commands, commands
// with no headless Handler (TUIOnly or bare PromptBody), and any command
// while the session has an active prompt turn.
func (m *CommandManager) Command(ctx context.Context, params json.RawMessage) (any, error) {
	var p CommandParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParamsError("parse session/command params: %v", err)
		}
	}
	if p.SessionID == "" {
		return nil, invalidParamsError("session/command requires sessionId")
	}
	if p.Name == "" {
		return nil, invalidParamsError("session/command requires name")
	}

	rt, ok := m.lookup(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("acp: unknown session: %s", p.SessionID)
	}
	if rt.Registry == nil {
		return nil, &jsonRPCError{Code: internalError, Message: "session has no command registry"}
	}

	cmd, ok := rt.Registry.Lookup(p.Name)
	if !ok {
		return nil, &jsonRPCError{Code: methodNotFound, Message: "unknown command: " + p.Name}
	}
	if cmd.Handler == nil {
		reason := "prompt command; send its body via session/prompt instead"
		if cmd.TUIOnly {
			reason = "command is TUI-only and has no headless handler"
		}
		return nil, &jsonRPCError{Code: methodNotFound, Message: fmt.Sprintf("command %q not available over ACP: %s", p.Name, reason)}
	}

	if m.hasActive(p.SessionID) {
		return nil, serverErrorf("session %s already has an active turn", p.SessionID)
	}

	result := cmd.Handler(rt.State, p.Args)
	return CommandResult{Text: result.Text, Doc: toWireDoc(result.Doc)}, nil
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
