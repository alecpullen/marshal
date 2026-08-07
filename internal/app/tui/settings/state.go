package settings

import (
	"maps"
	"slices"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// state holds the single mutable working copy of the config that every
// section pane binds to by pointer. It is heap-allocated (Model stores
// *state) so pointer bindings survive Model value copies.
type state struct {
	cfg              config.Config
	discovered       map[string][]schema.ModelInfo
	actionState      map[string]actionState
	connectRequested bool
	pendingCmd       tea.Cmd

	// dataDir enables on-disk limit resolution for probes launched from
	// settings. Empty disables it, which silently strips context/output
	// limits from every discovered model.
	dataDir string
	// probed records providers already probed during this browser's
	// lifetime, so rebuilding a frame does not re-probe on every refresh.
	probed map[string]bool
}

type actionState struct {
	label string
}

func newState(cfg config.Config) *state {
	working := cloneConfig(cfg)
	return &state{
		cfg:         working,
		discovered:  map[string][]schema.ModelInfo{},
		actionState: map[string]actionState{},
		probed:      map[string]bool{},
	}
}

// queueCmd adds a command to the pending batch. Unlike assigning pendingCmd
// directly it never drops an already-queued command, so a frame can probe
// several providers in one pass.
func (s *state) queueCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if s.pendingCmd == nil {
		s.pendingCmd = cmd
		return
	}
	s.pendingCmd = tea.Batch(s.pendingCmd, cmd)
}

// markProbed reports whether provider should be probed now, recording it so
// later calls return false.
func (s *state) markProbed(provider string) bool {
	if s.probed == nil {
		s.probed = map[string]bool{}
	}
	if s.probed[provider] {
		return false
	}
	s.probed[provider] = true
	return true
}

// SetDataDir sets the limit-resolution data directory. Exported for the
// agents panel, which builds a state directly.
func (s *state) SetDataDir(dir string) { s.dataDir = dir }

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

// SetStateProfileDefault sets the active profile default on the working state.
// Used by the agents roster picker, which holds a struct copy of the config
// and needs to mutate the state's live copy.
func SetStateProfileDefault(s *State, v string) {
	s.cfg.Profile.Default = v
}

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
	if cfg.CustomAgents != nil {
		out.CustomAgents = make(map[string]routing.CustomAgent, len(cfg.CustomAgents))
		for name, a := range cfg.CustomAgents {
			ca := a
			ca.ToolDenylist = slices.Clone(a.ToolDenylist)
			out.CustomAgents[name] = ca
		}
	}
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
