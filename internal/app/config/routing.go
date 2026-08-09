package config

import (
	"maps"
	"strings"

	"marshal/internal/llm/routing"
)

// RoutingConfig converts the Config into a routing.Config for the static
// router. It maps agent role configurations to context budgets and copies
// the top-level profile, privacy, model, and agent settings.
//
// When the default profile does not exist in AgentProfiles, a single-model
// profile is synthesized in memory so the router always has a profile to
// resolve roles through. The synthesized profile binds every chat role to
// the preset named by Profile.ActivePreset, or (when exactly one preset
// exists) to that sole preset. This keeps the config file pair-only — no
// [agent_profiles] section is needed for the common single-model case.
func (c Config) RoutingConfig() routing.Config {
	contextBudgets := make(map[routing.AgentRole]routing.ContextBudget, len(c.Agents))
	for role, agentCfg := range c.Agents {
		contextBudgets[role] = agentCfg.Context
	}
	providerBaseURLs := make(map[string]string, len(c.Providers))
	for name, p := range c.Providers {
		providerBaseURLs[name] = p.BaseURL
	}
	profiles := maps.Clone(c.AgentProfiles)
	if profiles == nil {
		profiles = map[string]routing.AgentProfile{}
	}
	// When ActivePreset is set, always (re)synthesize the default profile
	// from it — even if a stale entry already exists in AgentProfiles. This
	// happens after MigrateLegacyAgentModel persists a "single" profile
	// bound to the old model; without this overwrite, switching models via
	// /models or /connect would leave the stale bindings in place and the
	// status bar would never update.
	if c.Profile.ActivePreset != "" {
		if p := synthesizeSingleModelProfile(c, profiles); p != nil {
			// The embedding role is exempt from replacement: a chat
			// preset can never serve it, so a model switch cannot
			// stale it, and a user binding (via
			// [agent_profiles.<name>.roles] or /settings) must
			// survive synthesis.
			if existing, ok := profiles[c.Profile.Default]; ok {
				if b, ok := existing.Roles[routing.RoleEmbedding]; ok {
					p.Roles[routing.RoleEmbedding] = b
				}
			}
			profiles[c.Profile.Default] = *p
		}
	} else if _, ok := profiles[c.Profile.Default]; !ok {
		if p := synthesizeSingleModelProfile(c, profiles); p != nil {
			profiles[c.Profile.Default] = *p
		}
	}
	return routing.Config{
		DefaultProfile:   c.Profile.Default,
		RemoteAllowed:    c.Privacy.RemoteProvidersAllowed,
		Presets:          c.Models.Presets,
		Profiles:         profiles,
		CustomAgents:     c.CustomAgents,
		ContextBudgets:   contextBudgets,
		ProviderBaseURLs: providerBaseURLs,
	}
}

// synthesizeSingleModelProfile builds a single-model profile binding every
// chat role to one preset. The preset is chosen from Profile.ActivePreset
// when set; otherwise, when exactly one preset is configured, that sole
// preset is used. It returns nil when no preset can be determined.
func synthesizeSingleModelProfile(c Config, profiles map[string]routing.AgentProfile) *routing.AgentProfile {
	presetName := c.Profile.ActivePreset
	if presetName == "" {
		if len(c.Models.Presets) == 1 {
			for name := range c.Models.Presets {
				presetName = name
			}
		} else {
			return nil
		}
	}
	if presetName == "" {
		return nil
	}
	// Guard against a stale ActivePreset: if the preset it names no longer
	// exists and no provider is configured for it, don't revive it. This
	// handles the edge case where the default profile was switched to a
	// custom one and the preset was later deleted.
	if _, ok := c.Models.Presets[presetName]; !ok {
		provider, _, hasSlash := strings.Cut(presetName, "/")
		if !hasSlash || provider == "" {
			return nil
		}
		if _, known := c.Providers[provider]; !known {
			return nil
		}
	}
	p := routing.SingleModelProfile(c.Profile.Default, presetName)
	return &p
}
