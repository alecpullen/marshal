package config

import "marshal/internal/llm/routing"

// MigrateLegacyAgentModel rewrites a legacy [agent] provider+model pair into
// a single-model profile and preset. It returns true when it made changes.
//
// A legacy config has both a provider and model set and no usable default
// profile. The migration creates a preset named "<provider>/<model>", creates
// a single-model profile named "single" that binds every chat role to that
// preset, and sets cfg.Profile.Default = "single".
//
// "No usable default profile" means the name in cfg.Profile.Default does not
// resolve to an entry in cfg.AgentProfiles — not merely that the string is
// empty. Default() supplies Profile.Default = "local_balanced", so a config
// that omits [profile] entirely still arrives here naming a profile nothing
// defines. Checking only for emptiness would skip those configs, and since
// the legacy resolution path was deleted the router would then resolve no
// route at all. That shape is what docs/09-configuration-examples.md shows.
//
// Existing presets and profiles are never disturbed.
func MigrateLegacyAgentModel(cfg *Config, provider, model string) bool {
	if provider == "" || model == "" {
		return false
	}
	if _, ok := cfg.AgentProfiles[cfg.Profile.Default]; ok {
		return false
	}

	presetName := provider + "/" + model

	// Insert preset, preserving existing entries.
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = make(map[string]routing.ModelPreset)
	}
	if _, exists := cfg.Models.Presets[presetName]; !exists {
		// Carry local-ness onto the preset. The deleted legacyRoute gated on
		// !RemoteAllowed && !isLocalProvider(LegacyProvider); presets gate on
		// preset.LocalOnly instead. Without this a local-only user — the
		// default, since remote_providers_allowed is false — migrates into a
		// preset the remote gate immediately blocks.
		//
		// The base URL is the real signal, so a provider with no configured
		// entry is never assumed local: guessing wrong here would let a
		// remote endpoint past a privacy gate the user did not open.
		localOnly := false
		if pc, ok := cfg.Providers[provider]; ok && pc.BaseURL != "" {
			localOnly = routing.IsLocalProvider(pc.BaseURL)
		}
		cfg.Models.Presets[presetName] = routing.ModelPreset{
			Name:      presetName,
			Provider:  provider,
			Model:     model,
			LocalOnly: localOnly,
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

	return true
}
