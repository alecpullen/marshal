package config

import "marshal/internal/llm/routing"

// MigrateLegacyAgentModel rewrites a legacy [agent] provider+model pair into
// a single-model profile and preset. It returns true when it made changes.
//
// The provider and model are passed as explicit arguments (read from the raw
// file mirror in LoadLayers) because the fields no longer exist on AgentConfig.
// The migration creates a preset named "<provider>/<model>", sets
// Profile.Default = "single" and Profile.ActivePreset to the preset name,
// and inserts a single-model profile into AgentProfiles so the router can
// resolve every role through it.
//
// "No usable default profile" means the name in cfg.Profile.Default does not
// resolve to an entry in cfg.AgentProfiles — not merely that the string is
// empty. Default() supplies Profile.Default = "local_balanced", so a config
// that omits [profile] entirely still arrives here naming a profile nothing
// defines.
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
	cfg.Profile.ActivePreset = presetName

	return true
}

// MigrateEmbeddingRoleBinding moves the legacy per-profile embedding role
// binding ([agent_profiles.<name>.roles] embedding = "<preset>") to the
// structural [indexing] embedding_preset field. The role binding sat on the
// active profile, so profile synthesis, /models and /connect (which force
// Profile.Default = "single"), and the config.agent_profiles.set tool could
// all silently drop it. Returns true when it changed anything.
//
// Like MigrateLegacyAgentModel this runs in memory at load; the legacy key
// disappears from the file on the next config save. When several profiles
// bind embedding, the active profile's binding wins (it was the one actually
// in effect); an existing embedding_preset always wins over any binding. A
// binding that names a CustomAgent (rather than a Preset) is stripped but
// does not set EmbeddingPreset — a custom agent cannot embed.
func MigrateEmbeddingRoleBinding(cfg *Config) bool {
	var active string
	changed := false
	for name, profile := range cfg.AgentProfiles {
		b, ok := profile.Roles[routing.RoleEmbedding]
		if !ok {
			continue
		}
		if name == cfg.Profile.Default {
			active = b.Preset
		} else if active == "" {
			active = b.Preset
		}
		delete(profile.Roles, routing.RoleEmbedding)
		cfg.AgentProfiles[name] = profile
		changed = true
	}
	if cfg.Indexing.EmbeddingPreset == "" && active != "" {
		cfg.Indexing.EmbeddingPreset = active
	}
	return changed
}
