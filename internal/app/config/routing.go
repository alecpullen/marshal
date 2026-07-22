package config

import "marshal/internal/llm/routing"

// RoutingConfig converts the Config into a routing.Config for the static
// router. It maps agent role configurations to context budgets and copies
// the top-level profile, privacy, model, and agent settings.
func (c Config) RoutingConfig() routing.Config {
	contextBudgets := make(map[routing.AgentRole]routing.ContextBudget, len(c.Agents))
	for role, agentCfg := range c.Agents {
		contextBudgets[role] = agentCfg.Context
	}
	return routing.Config{
		DefaultProfile: c.Profile.Default,
		RemoteAllowed:  c.Privacy.RemoteProvidersAllowed,
		Presets:        c.Models.Presets,
		Profiles:       c.AgentProfiles,
		ContextBudgets: contextBudgets,
		LegacyProvider: c.Agent.Provider,
		LegacyModel:    c.Agent.Model,
	}
}
