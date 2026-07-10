package settings

import (
	"maps"
	"slices"

	"marshal/internal/app/config"
)

// state holds the single mutable working copy of the config that every
// section pane binds to by pointer, plus an immutable snapshot used for
// dirty detection. It is heap-allocated (Model stores *state) so pointer
// bindings survive Model value copies.
type state struct {
	cfg      config.Config
	snapshot config.Config
}

func newState(cfg config.Config) *state {
	working := cloneConfig(cfg)
	return &state{cfg: working, snapshot: cloneConfig(working)}
}

// cloneConfig deep-copies every map and slice reachable from cfg that the
// settings panes can mutate, so edits to the working copy never leak into
// the snapshot (or the caller's config).
func cloneConfig(cfg config.Config) config.Config {
	out := cfg
	out.Project.Languages = slices.Clone(cfg.Project.Languages)
	out.Indexing.Ignore = slices.Clone(cfg.Indexing.Ignore)
	out.Providers = maps.Clone(cfg.Providers)
	out.Models.Presets = maps.Clone(cfg.Models.Presets)
	out.AgentProfiles = maps.Clone(cfg.AgentProfiles)
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
