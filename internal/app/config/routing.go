package config

import "marshal/internal/llm/routing"

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
	profiles := c.AgentProfiles
	if profiles == nil {
		profiles = map[string]routing.AgentProfile{}
	}
	if _, ok := profiles[c.Profile.Default]; !ok {
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
	p := routing.SingleModelProfile(c.Profile.Default, presetName)
	return &p
}
