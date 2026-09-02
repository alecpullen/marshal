package config

import (
	"fmt"
	"net/url"
	"os"
	"sort"
)

// Severity ranks a diagnostic. SeverityError < SeverityWarning < SeverityInfo
// so that sorting by severity places errors first, then warnings, then
// informational notes.
type Severity int

const (
	SeverityError   Severity = 0
	SeverityWarning Severity = 1
	SeverityInfo    Severity = 2
)

// Diagnostic is a single semantic check result on the merged config.
type Diagnostic struct {
	Severity Severity
	Path     string
	Message  string
	Source   string
}

// Diagnose runs semantic checks over the merged config and returns an
// ordered slice of diagnostics: errors first, then warnings, each group
// sorted by Path for stable output.
func Diagnose(cfg Config, layers Layers) []Diagnostic {
	var ds []Diagnostic

	// 1 & 2: provider BaseURL checks
	for name, pc := range cfg.Providers {
		path := "providers." + name + ".base_url"
		if pc.BaseURL == "" {
			ds = append(ds, Diagnostic{
				Severity: SeverityError,
				Path:     path,
				Message:  "provider " + name + " has an empty base_url",
				Source:   layers.ProvenanceOf(path).SetBy.String(),
			})
			continue
		}
		u, err := url.Parse(pc.BaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			ds = append(ds, Diagnostic{
				Severity: SeverityError,
				Path:     path,
				Message:  "provider " + name + " has an invalid base_url",
				Source:   layers.ProvenanceOf(path).SetBy.String(),
			})
		}
	}

	// 3: APIKeyEnv absent from environment (only relevant when no literal api_key is set)
	for name, pc := range cfg.Providers {
		if pc.APIKeyEnv == "" || pc.APIKey != "" {
			continue
		}
		path := "providers." + name + ".api_key_env"
		if v, ok := os.LookupEnv(pc.APIKeyEnv); !ok || v == "" {
			ds = append(ds, Diagnostic{
				Severity: SeverityWarning,
				Path:     path,
				Message:  "provider " + name + " references env var " + pc.APIKeyEnv + " which is not set",
				Source:   layers.ProvenanceOf(path).SetBy.String(),
			})
		}
	}

	// 4: preset Provider not in cfg.Providers
	for name, preset := range cfg.Models.Presets {
		path := "models.presets." + name + ".provider"
		if _, ok := cfg.Providers[preset.Provider]; !ok {
			ds = append(ds, Diagnostic{
				Severity: SeverityError,
				Path:     path,
				Message:  "preset " + name + " references provider " + preset.Provider + " which is not configured",
				Source:   layers.ProvenanceOf(path).SetBy.String(),
			})
		}
	}

	// 5: profile role bound to a Preset or CustomAgent not in config
	for profName, profile := range cfg.AgentProfiles {
		for role, binding := range profile.Roles {
			if binding.Preset != "" {
				path := "agent_profiles." + profName + "." + string(role) + ".preset"
				if _, ok := cfg.Models.Presets[binding.Preset]; !ok {
					ds = append(ds, Diagnostic{
						Severity: SeverityError,
						Path:     path,
						Message:  "profile " + profName + " role " + string(role) + " references preset " + binding.Preset + " which is not configured",
						Source:   layers.ProvenanceOf(path).SetBy.String(),
					})
				}
			}
			if binding.CustomAgent != "" {
				path := "agent_profiles." + profName + "." + string(role) + ".custom_agent"
				if _, ok := cfg.CustomAgents[binding.CustomAgent]; !ok {
					ds = append(ds, Diagnostic{
						Severity: SeverityError,
						Path:     path,
						Message:  "profile " + profName + " role " + string(role) + " references custom_agent " + binding.CustomAgent + " which is not configured",
						Source:   layers.ProvenanceOf(path).SetBy.String(),
					})
				}
			}
		}
	}

	// 6: literal APIKey in project config warns
	for name, pc := range cfg.Providers {
		if pc.APIKey == "" {
			continue
		}
		path := "providers." + name + ".api_key"
		p := layers.ProvenanceOf(path)
		if p.SetBy == LayerProject {
			ds = append(ds, Diagnostic{
				Severity: SeverityWarning,
				Path:     path,
				Message:  "provider " + name + " has a literal api_key in project config; consider using api_key_env instead",
				Source:   p.SetBy.String(),
			})
		}
	}

	// 7: legacy settings migrated at load (agent.provider/agent.model, and
	// the per-profile embedding role binding)
	if layers.Migrated {
		path := "agent.provider"
		p := layers.ProvenanceOf(path)
		source := p.SetBy.String()
		ds = append(ds, Diagnostic{
			Severity: SeverityWarning,
			Path:     path,
			Message:  "legacy settings were migrated for this session (agent.provider/agent.model, or the per-profile embedding role binding). The next config save will rewrite these settings.",
			Source:   source,
		})
	}

	// 8: hoist migration outcome. [providers]/[models.presets] in the project
	// config are deprecated (user-global only); LoadLayers moves what it can
	// and reports what it moved and what it could not reconcile.
	for _, name := range layers.HoistConflicts {
		ds = append(ds, Diagnostic{
			Severity: SeverityWarning,
			Path:     name,
			Message:  fmt.Sprintf("project config entry %q conflicts with the user config; kept project-local (deprecated) — resolve manually", name),
			Source:   LayerProject.String(),
		})
	}
	if n := len(layers.HoistedProviders); n > 0 {
		ds = append(ds, Diagnostic{
			Severity: SeverityInfo,
			Path:     "providers",
			Message:  fmt.Sprintf("moved %d provider(s) to the user config", n),
			Source:   LayerProject.String(),
		})
	}
	if n := len(layers.HoistedPresets); n > 0 {
		ds = append(ds, Diagnostic{
			Severity: SeverityInfo,
			Path:     "models.presets",
			Message:  fmt.Sprintf("moved %d preset(s) to the user config", n),
			Source:   LayerProject.String(),
		})
	}

	// Sort: errors before warnings, then by Path within each group.
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Severity != ds[j].Severity {
			return ds[i].Severity < ds[j].Severity
		}
		return ds[i].Path < ds[j].Path
	})

	return ds
}
