package settings

import (
	"maps"
	"slices"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

// state holds the single mutable working copy of the config that every
// section pane binds to by pointer. It is heap-allocated (Model stores
// *state) so pointer bindings survive Model value copies.
type state struct {
	cfg              config.Config
	discovered       map[string][]string
	actionState      map[string]actionState
	connectRequested bool
	pendingCmd       tea.Cmd
}

type actionState struct {
	label string
}

func newState(cfg config.Config) *state {
	working := cloneConfig(cfg)
	return &state{
		cfg:         working,
		discovered:  map[string][]string{},
		actionState: map[string]actionState{},
	}
}

// NewState is the exported constructor for *State (alias for *state).
func NewState(cfg config.Config) *state { return newState(cfg) }

func (s *state) takePendingCmd() tea.Cmd {
	cmd := s.pendingCmd
	s.pendingCmd = nil
	return cmd
}

func (s *state) takeConnectRequested() bool {
	requested := s.connectRequested
	s.connectRequested = false
	return requested
}

func (s *state) applyActionResult(fieldID, label string) {
	s.actionState[fieldID] = actionState{label: label}
}

// State is an exported alias for state.
type State = state

// StateCfg returns the config from a state.
func StateCfg(s *State) config.Config { return s.cfg }

// cloneConfig deep-copies every map and slice reachable from cfg that the
// settings panes can mutate, so edits to the working copy never leak into
// the caller's config.
func cloneConfig(cfg config.Config) config.Config {
	out := cfg
	out.Project.Languages = slices.Clone(cfg.Project.Languages)
	out.Indexing.Ignore = slices.Clone(cfg.Indexing.Ignore)
	out.Providers = maps.Clone(cfg.Providers)
	out.Models.Presets = maps.Clone(cfg.Models.Presets)
	if cfg.AgentProfiles != nil {
		out.AgentProfiles = make(map[string]routing.AgentProfile, len(cfg.AgentProfiles))
		for name, profile := range cfg.AgentProfiles {
			profile.Roles = maps.Clone(profile.Roles)
			out.AgentProfiles[name] = profile
		}
	}
	out.Agents = maps.Clone(cfg.Agents)
	out.Tools.Shell.Allow.Commands = slices.Clone(cfg.Tools.Shell.Allow.Commands)
	out.Tools.Shell.Confirm.Commands = slices.Clone(cfg.Tools.Shell.Confirm.Commands)
	out.Tools.Shell.Deny.Patterns = slices.Clone(cfg.Tools.Shell.Deny.Patterns)
	out.Tools.Shell.Sandbox.EnvAllowlist = slices.Clone(cfg.Tools.Shell.Sandbox.EnvAllowlist)
	out.Tools.Shell.Sandbox.EnvDenylist = slices.Clone(cfg.Tools.Shell.Sandbox.EnvDenylist)
	out.Swarm.Budget.ToolIters = maps.Clone(cfg.Swarm.Budget.ToolIters)
	if cfg.MCP.Servers != nil {
		out.MCP.Servers = make(map[string]config.MCPServerConfig, len(cfg.MCP.Servers))
		for name, srv := range cfg.MCP.Servers {
			srv.Args = slices.Clone(srv.Args)
			srv.Env = maps.Clone(srv.Env)
			out.MCP.Servers[name] = srv
		}
	}
	out.MCP.Policies = maps.Clone(cfg.MCP.Policies)
	out.Permissions.Rules = slices.Clone(cfg.Permissions.Rules)
	out.Diagnostics.Commands = maps.Clone(cfg.Diagnostics.Commands)
	out.Hooks.Entries = slices.Clone(cfg.Hooks.Entries)
	return out
}
