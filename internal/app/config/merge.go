package config

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/llm/routing"
)

// set assigns *src to *dst when src is non-nil. It collapses the
// nullable-mirror merge stanzas to one line per field.
func set[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

func merge(cfg *Config, file configFile) error {
	if file.Project != nil {
		set(&cfg.Project.Name, file.Project.Name)
		if file.Project.Languages != nil {
			cfg.Project.Languages = file.Project.Languages
		}
	}
	if file.Commands != nil {
		set(&cfg.Commands.Test, file.Commands.Test)
		set(&cfg.Commands.Format, file.Commands.Format)
		set(&cfg.Commands.Vet, file.Commands.Vet)
	}
	if file.Profile != nil {
		set(&cfg.Profile.Default, file.Profile.Default)
		set(&cfg.Profile.ActivePreset, file.Profile.ActivePreset)
	}
	if file.Agent != nil {
		set(&cfg.Agent.MaxToolIterations, file.Agent.MaxToolIterations)
		set(&cfg.Agent.MaxRetries, file.Agent.MaxRetries)
		set(&cfg.Agent.MaxTurnContextTokens, file.Agent.MaxTurnContextTokens)
		set(&cfg.Agent.ReconnectMaxWaitSeconds, file.Agent.ReconnectMaxWaitSeconds)
		set(&cfg.Agent.MaxToolResultChars, file.Agent.MaxToolResultChars)
		set(&cfg.Agent.MaxStructuredOutputChars, file.Agent.MaxStructuredOutputChars)
		set(&cfg.Agent.PlanFirst, file.Agent.PlanFirst)
		set(&cfg.Agent.SubtaskIterations, file.Agent.SubtaskIterations)
		set(&cfg.Agent.ApprovalMode, file.Agent.ApprovalMode)
		set(&cfg.Agent.HistoryBudgetTokens, file.Agent.HistoryBudgetTokens)
	}
	if file.Privacy != nil {
		set(&cfg.Privacy.RemoteProvidersAllowed, file.Privacy.RemoteProvidersAllowed)
		set(&cfg.Privacy.RedactSecrets, file.Privacy.RedactSecrets)
		set(&cfg.Privacy.IncludeGitignoredFiles, file.Privacy.IncludeGitignoredFiles)
	}
	if file.Skills != nil {
		if file.Skills.Autoload != nil {
			cfg.Skills.Autoload = file.Skills.Autoload
		}
		set(&cfg.Skills.MaxActive, file.Skills.MaxActive)
	}
	if file.Indexing != nil {
		set(&cfg.Indexing.UseTreesitter, file.Indexing.UseTreesitter)
		set(&cfg.Indexing.UseEmbeddings, file.Indexing.UseEmbeddings)
		set(&cfg.Indexing.SummariseFiles, file.Indexing.SummariseFiles)
		if file.Indexing.Ignore != nil {
			cfg.Indexing.Ignore = file.Indexing.Ignore
		}
		set(&cfg.Indexing.MaxIndexableFileBytes, file.Indexing.MaxIndexableFileBytes)
		set(&cfg.Indexing.MaxSearchableFileBytes, file.Indexing.MaxSearchableFileBytes)
		if file.Indexing.Watch != nil {
			cfg.Indexing.Watch = file.Indexing.Watch
		}
		set(&cfg.Indexing.WatchDebounceMs, file.Indexing.WatchDebounceMs)
		set(&cfg.Indexing.EmbeddingPreset, file.Indexing.EmbeddingPreset)
	}
	if file.Providers != nil {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]ProviderConfig, len(file.Providers))
		}
		// Overwrite by key: a provider name defined in both the global and
		// project file takes the project file's entry for non-credential
		// fields, mirroring how a later file "wins" for scalar fields
		// elsewhere in this function. Credential fields are the exception:
		// project configs are written without API keys (they live in the
		// user config), so an entry that names no credentials inherits them
		// from the lower layer instead of hiding them.
		for name, pc := range file.Providers {
			if prev, ok := cfg.Providers[name]; ok && pc.APIKey == "" && pc.APIKeyEnv == "" {
				pc.APIKey = prev.APIKey
				pc.APIKeyEnv = prev.APIKeyEnv
			}
			cfg.Providers[name] = pc
		}
	}
	// legacyRenames maps old non-canonical preset keys to their canonical
	// "<provider>/<model>" form so references in profiles and custom
	// agents can be rewritten in the same merge pass.
	legacyRenames := map[string]string{}
	if file.Models != nil && file.Models.Presets != nil {
		if cfg.Models.Presets == nil {
			cfg.Models.Presets = map[string]routing.ModelPreset{}
		}
		for name, preset := range file.Models.Presets {
			preset.Name = name
			if !routing.IsCanonicalPresetName(name) && preset.Provider != "" && preset.Model != "" {
				// Migrate a legacy non-canonical key (e.g. "fast") to the
				// canonical "<provider>/<model>" form. The provider and model
				// fields come from the TOML [models.presets.fast] section.
				canonName := preset.Provider + "/" + preset.Model
				legacyRenames[name] = canonName
				preset.Name = canonName
				if _, exists := cfg.Models.Presets[canonName]; !exists {
					cfg.Models.Presets[canonName] = preset
					continue
				}
				// Canonical key already exists (e.g. from a higher-priority
				// layer); skip the legacy entry rather than overwriting it.
				continue
			}
			// For canonical keys, derive provider/model from the key when the
			// TOML section omits them (the common pair-only case).
			if routing.IsCanonicalPresetName(name) && preset.Provider == "" && preset.Model == "" {
				preset.Provider, preset.Model, _ = strings.Cut(name, "/")
			}
			cfg.Models.Presets[name] = preset
		}
	}
	if file.AgentProfilesRaw != nil {
		if cfg.AgentProfiles == nil {
			cfg.AgentProfiles = map[string]routing.AgentProfile{}
		}
		for name, rawRoles := range file.AgentProfilesRaw {
			roles := map[routing.AgentRole]routing.RoleBinding{}
			for roleKey, roleVal := range rawRoles {
				role := routing.AgentRole(roleKey)
				var rb routing.RoleBinding
				if s, ok := roleVal.(string); ok {
					rb.Preset = s
				} else if m, ok := roleVal.(map[string]any); ok {
					if ca, ok := m["custom_agent"].(string); ok {
						rb.CustomAgent = ca
					}
					if p, ok := m["preset"].(string); ok {
						rb.Preset = p
					}
				}
				roles[role] = rb
			}
			cfg.AgentProfiles[name] = routing.AgentProfile{Name: name, Roles: roles}
		}
	}
	if file.CustomAgents != nil {
		if cfg.CustomAgents == nil {
			cfg.CustomAgents = map[string]routing.CustomAgent{}
		}
		for name, a := range file.CustomAgents {
			a.Name = name
			cfg.CustomAgents[name] = a
		}
	}
	// Rewrite references to renamed presets in agent profiles and custom
	// agents so they point at the canonical key. This must run after both
	// the presets and profiles/custom agents have been merged.
	if len(legacyRenames) > 0 {
		rewritePresetReferences(cfg, legacyRenames)
	}
	if file.Agents != nil {
		if cfg.Agents == nil {
			cfg.Agents = map[routing.AgentRole]AgentRoleConfig{}
		}
		for role, agentCfg := range file.Agents {
			cfg.Agents[role] = AgentRoleConfig{Context: agentCfg.Context}
		}
	}
	if file.Tools != nil && file.Tools.Shell != nil {
		s := file.Tools.Shell
		set(&cfg.Tools.Shell.DefaultTimeoutSeconds, s.DefaultTimeoutSeconds)
		set(&cfg.Tools.Shell.MaxOutputBytes, s.MaxOutputBytes)
		set(&cfg.Tools.Shell.MaxBackgroundJobs, s.MaxBackgroundJobs)
		if s.BackgroundRetention != nil && *s.BackgroundRetention != "" {
			d, err := time.ParseDuration(*s.BackgroundRetention)
			if err != nil {
				return fmt.Errorf("parse background_retention: %w", err)
			}
			cfg.Tools.Shell.BackgroundRetention = d
		}
		set(&cfg.Tools.Shell.AllowNetwork, s.AllowNetwork)
		set(&cfg.Tools.Shell.AutoApprove, s.AutoApprove)
		if s.GuardrailDynamicArgv0 != nil {
			cfg.Tools.Shell.GuardrailDynamicArgv0 = *s.GuardrailDynamicArgv0
		}
		if s.Allow != nil && s.Allow.Commands != nil {
			cfg.Tools.Shell.Allow.Commands = s.Allow.Commands
		}
		if s.Confirm != nil && s.Confirm.Commands != nil {
			cfg.Tools.Shell.Confirm.Commands = s.Confirm.Commands
		}
		if s.Deny != nil && s.Deny.Patterns != nil {
			cfg.Tools.Shell.Deny.Patterns = s.Deny.Patterns
		}
		if s.Sandbox != nil {
			set(&cfg.Tools.Shell.Sandbox.Backend, s.Sandbox.Backend)
			set(&cfg.Tools.Shell.Sandbox.MemoryLimitMB, s.Sandbox.MemoryLimitMB)
			set(&cfg.Tools.Shell.Sandbox.CPUSeconds, s.Sandbox.CPUSeconds)
			set(&cfg.Tools.Shell.Sandbox.MaxProcesses, s.Sandbox.MaxProcesses)
			set(&cfg.Tools.Shell.Sandbox.FileSizeLimitMB, s.Sandbox.FileSizeLimitMB)
			set(&cfg.Tools.Shell.Sandbox.ContainerRuntime, s.Sandbox.ContainerRuntime)
			set(&cfg.Tools.Shell.Sandbox.ContainerImage, s.Sandbox.ContainerImage)
			set(&cfg.Tools.Shell.Sandbox.AllowFallback, s.Sandbox.AllowFallback)
			set(&cfg.Tools.Shell.Sandbox.UnsafePassthrough, s.Sandbox.UnsafePassthrough)
			if s.Sandbox.EnvAllowlist != nil {
				cfg.Tools.Shell.Sandbox.EnvAllowlist = s.Sandbox.EnvAllowlist
			}
			if s.Sandbox.EnvDenylist != nil {
				cfg.Tools.Shell.Sandbox.EnvDenylist = s.Sandbox.EnvDenylist
			}
		}
	}
	if file.Swarm != nil && file.Swarm.Budget != nil {
		b := file.Swarm.Budget
		set(&cfg.Swarm.Budget.MaxFixRounds, b.MaxFixRounds)
		set(&cfg.Swarm.Budget.MaxTotalTokens, b.MaxTotalTokens)
		for role, iters := range b.ToolIters {
			if cfg.Swarm.Budget.ToolIters == nil {
				cfg.Swarm.Budget.ToolIters = map[string]int{}
			}
			cfg.Swarm.Budget.ToolIters[role] = iters
		}
	}
	if file.SDD != nil {
		set(&cfg.SDD.AutoWorktree, file.SDD.AutoWorktree)
		set(&cfg.SDD.MaxFixRounds, file.SDD.MaxFixRounds)
		set(&cfg.SDD.DispatchRetries, file.SDD.DispatchRetries)
		set(&cfg.SDD.PlansDir, file.SDD.PlansDir)
		set(&cfg.SDD.VerifyTimeoutMS, file.SDD.VerifyTimeoutMS)
		set(&cfg.SDD.CleanupAtStart, file.SDD.CleanupAtStart)
		set(&cfg.SDD.MaxTotalTokens, file.SDD.MaxTotalTokens)
		if file.SDD.Verify != nil {
			set(&cfg.SDD.Verify.Build, file.SDD.Verify.Build)
			set(&cfg.SDD.Verify.Test, file.SDD.Verify.Test)
		}
	}
	if file.MCP != nil {
		for name, srv := range file.MCP.Servers {
			if cfg.MCP.Servers == nil {
				cfg.MCP.Servers = map[string]MCPServerConfig{}
			}
			cfgSrv := MCPServerConfig{Env: srv.Env}
			if srv.Command != nil {
				cfgSrv.Command = *srv.Command
			}
			if srv.Trust != nil {
				cfgSrv.Trust = *srv.Trust
			}
			cfgSrv.Args = srv.Args
			cfg.MCP.Servers[name] = cfgSrv
		}
		for k, v := range file.MCP.Policies {
			if cfg.MCP.Policies == nil {
				cfg.MCP.Policies = map[string]string{}
			}
			cfg.MCP.Policies[k] = v
		}
		if file.MCP.DisclosureThresholdTools != nil {
			set(&cfg.MCP.DisclosureThresholdTools, file.MCP.DisclosureThresholdTools)
		}
	}
	if file.Snapshots != nil {
		set(&cfg.Snapshots.Enabled, file.Snapshots.Enabled)
		set(&cfg.Snapshots.RetentionDays, file.Snapshots.RetentionDays)
		set(&cfg.Snapshots.MaxFileBytes, file.Snapshots.MaxFileBytes)
	}
	if file.TUI != nil {
		set(&cfg.TUI.Theme, file.TUI.Theme)
		if file.TUI.Palette != nil {
			if cfg.TUI.Palette == nil {
				cfg.TUI.Palette = map[string]string{}
			}
			for k, v := range file.TUI.Palette {
				cfg.TUI.Palette[k] = v
			}
		}
		set(&cfg.TUI.Mode, file.TUI.Mode)
		set(&cfg.TUI.MouseCapture, file.TUI.MouseCapture)
		if sp := file.TUI.SidePanel; sp != nil {
			set(&cfg.TUI.SidePanel.Enabled, sp.Enabled)
			set(&cfg.TUI.SidePanel.MinWidth, sp.MinWidth)
			set(&cfg.TUI.SidePanel.WidthPct, sp.WidthPct)
			set(&cfg.TUI.SidePanel.MinCols, sp.MinCols)
			set(&cfg.TUI.SidePanel.MaxCols, sp.MaxCols)
			if sp.Hidden != nil {
				cfg.TUI.SidePanel.Hidden = append([]string(nil), sp.Hidden...)
			}
		}
	}
	if file.History != nil {
		set(&cfg.History.Enabled, file.History.Enabled)
	}
	if file.Permissions != nil && file.Permissions.Rules != nil {
		cfg.Permissions.Rules = file.Permissions.Rules
	}
	if file.Diagnostics != nil && file.Diagnostics.Commands != nil {
		if cfg.Diagnostics.Commands == nil {
			cfg.Diagnostics.Commands = map[string]string{}
		}
		for k, v := range file.Diagnostics.Commands {
			cfg.Diagnostics.Commands[k] = v
		}
	}
	if file.Web != nil {
		set(&cfg.Web.Enabled, file.Web.Enabled)
		if file.Web.FetchTimeout != nil && *file.Web.FetchTimeout != "" {
			d, err := time.ParseDuration(*file.Web.FetchTimeout)
			if err != nil {
				return fmt.Errorf("parse web.fetch_timeout: %w", err)
			}
			cfg.Web.FetchTimeout = d
		}
		set(&cfg.Web.SearchProvider, file.Web.SearchProvider)
		set(&cfg.Web.SearchURL, file.Web.SearchURL)
		set(&cfg.Web.SearchKey, file.Web.SearchKey)
	}
	if file.Desktop != nil {
		set(&cfg.Desktop.Enabled, file.Desktop.Enabled)
		set(&cfg.Desktop.Mode, file.Desktop.Mode)
		set(&cfg.Desktop.Headless, file.Desktop.Headless)
		set(&cfg.Desktop.CDPURL, file.Desktop.CDPURL)
		if file.Desktop.URLAllowlist != nil {
			cfg.Desktop.URLAllowlist = file.Desktop.URLAllowlist
		}
		if file.Desktop.URLDenylist != nil {
			cfg.Desktop.URLDenylist = file.Desktop.URLDenylist
		}
		if file.Desktop.DefaultTimeout != nil && *file.Desktop.DefaultTimeout != "" {
			d, err := time.ParseDuration(*file.Desktop.DefaultTimeout)
			if err != nil {
				return fmt.Errorf("parse desktop.default_timeout: %w", err)
			}
			cfg.Desktop.DefaultTimeout = d
		}
		set(&cfg.Desktop.ScreenshotFormat, file.Desktop.ScreenshotFormat)
	}
	if file.Session != nil && file.Session.Rollover != nil {
		r := file.Session.Rollover
		set(&cfg.Session.Rollover.Enabled, r.Enabled)
		set(&cfg.Session.Rollover.Policy, r.Policy)
		set(&cfg.Session.Rollover.ContextPercentThreshold, r.ContextPercentThreshold)
		set(&cfg.Session.Rollover.TurnCountThreshold, r.TurnCountThreshold)
		set(&cfg.Session.Rollover.TokenCounter, r.TokenCounter)
		set(&cfg.Session.Rollover.DigestModel, r.DigestModel)
		set(&cfg.Session.Rollover.DigestProvider, r.DigestProvider)
		switch cfg.Session.Rollover.DigestProvider {
		case "", "auto":
			cfg.Session.Rollover.DigestProvider = "llm_summary"
		case "llm_summary", "files", "minimal":
			// valid
		default:
			return fmt.Errorf("session.rollover.digest_provider: unrecognized value %q (want llm_summary, files, minimal, or auto)", cfg.Session.Rollover.DigestProvider)
		}
		set(&cfg.Session.Rollover.RecallToolEnabled, r.RecallToolEnabled)
		set(&cfg.Session.Rollover.Retention, r.Retention)
		set(&cfg.Session.Rollover.BlobThresholdBytes, r.BlobThresholdBytes)
		if r.Calibration != nil {
			set(&cfg.Session.Rollover.Calibration.Enabled, r.Calibration.Enabled)
		}
	}
	if file.LSP != nil {
		if file.LSP.Enabled != nil {
			cfg.LSP.Enabled = file.LSP.Enabled
		}
		for name, srv := range file.LSP.Servers {
			if cfg.LSP.Servers == nil {
				cfg.LSP.Servers = map[string]LSPServerConfig{}
			}
			cfgSrv := LSPServerConfig{}
			if srv.Command != nil {
				cfgSrv.Command = *srv.Command
			}
			cfgSrv.Args = srv.Args
			if srv.Disabled != nil {
				cfgSrv.Disabled = *srv.Disabled
			}
			cfg.LSP.Servers[name] = cfgSrv
		}
	}
	if file.Hooks != nil {
		set(&cfg.Hooks.FailClosed, file.Hooks.FailClosed)
		if file.Hooks.Entries != nil {
			cfg.Hooks.Entries = nil
			for _, entry := range file.Hooks.Entries {
				var hc HookConfig
				set(&hc.Event, entry.Event)
				set(&hc.Matcher, entry.Matcher)
				set(&hc.Command, entry.Command)
				set(&hc.TimeoutMS, entry.TimeoutMS)
				cfg.Hooks.Entries = append(cfg.Hooks.Entries, hc)
			}
		}
	}
	if file.Scratchpad != nil {
		set(&cfg.Scratchpad.MaxEntries, file.Scratchpad.MaxEntries)
		set(&cfg.Scratchpad.MaxTotalTokens, file.Scratchpad.MaxTotalTokens)
		set(&cfg.Scratchpad.MaxEntryTokens, file.Scratchpad.MaxEntryTokens)
		set(&cfg.Scratchpad.ProjectionMaxTokens, file.Scratchpad.ProjectionMaxTokens)
	}
	return nil
}

// rewritePresetReferences rewrites preset references in agent profiles and
// custom agents using the old→canonical rename map. This ensures that
// migrating a legacy key like "fast" to "ollama/qwen3" also updates
// [agent_profiles.*] bindings and [custom_agents.*] preset fields that
// pointed at "fast", so routes resolve correctly after load.
func rewritePresetReferences(cfg *Config, renames map[string]string) {
	for name, profile := range cfg.AgentProfiles {
		changed := false
		for role, rb := range profile.Roles {
			if rb.Preset != "" {
				if canon, ok := renames[rb.Preset]; ok {
					rb.Preset = canon
					profile.Roles[role] = rb
					changed = true
				}
			}
		}
		if changed {
			cfg.AgentProfiles[name] = profile
		}
	}
	for name, agent := range cfg.CustomAgents {
		if agent.Preset != "" {
			if canon, ok := renames[agent.Preset]; ok {
				agent.Preset = canon
				cfg.CustomAgents[name] = agent
			}
		}
	}
}
