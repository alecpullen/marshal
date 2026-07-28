package config

import "marshal/internal/llm/routing"

// MigrateLegacyAgentModel rewrites a legacy [agent] provider+model pair into
// a single-model profile and preset. It returns true when it made changes.
//
// A legacy config has both cfg.Agent.Provider and cfg.Agent.Model set and no
// cfg.Profile.Default. The migration creates a preset named
// "<provider>/<model>", creates a single-model profile named "single" that
// binds every chat role to that preset, and sets cfg.Profile.Default = "single".
// The legacy fields are then cleared so the rest of the system sees only the
// new shape.
//
// Existing presets and profiles are never disturbed.
func MigrateLegacyAgentModel(cfg *Config) bool {
	if cfg.Agent.Provider == "" || cfg.Agent.Model == "" {
		return false
	}
	if cfg.Profile.Default != "" {
		return false
	}

	presetName := cfg.Agent.Provider + "/" + cfg.Agent.Model

	// Insert preset, preserving existing entries.
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = make(map[string]routing.ModelPreset)
	}
	if _, exists := cfg.Models.Presets[presetName]; !exists {
		cfg.Models.Presets[presetName] = routing.ModelPreset{
			Name:     presetName,
			Provider: cfg.Agent.Provider,
			Model:    cfg.Agent.Model,
		}
	}

	// Insert single-model profile, preserving existing entries.
	if cfg.AgentProfiles == nil {
		cfg.AgentProfiles = make(map[string]routing.AgentProfile)
	}
	if _, exists := cfg.AgentProfiles["single"]; !exists {
		cfg.AgentProfiles["single"] = routing.SingleModelProfile("single", presetName)
	}

	cfg.Profile.Default = "single"
	cfg.Agent.Provider = ""
	cfg.Agent.Model = ""

	return true
}
