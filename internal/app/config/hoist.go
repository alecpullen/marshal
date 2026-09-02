package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"marshal/internal/llm/routing"
)

// hoistResult records what hoistProjectGlobals did with a project config's
// [providers] and [models.presets] sections.
type hoistResult struct {
	// providers and presets name the entries removed from the project file:
	// either moved into the user config or dropped because the user config
	// already carried an identical entry.
	providers []string
	presets   []string
	// conflicts name the entries left in the project file because the user
	// config carries a materially different entry under the same name, as
	// dotted paths ("providers.x", "models.presets.x/y").
	conflicts []string
}

// hoistProjectGlobals migrates [providers] and [models.presets] entries from
// a project config into the user-global config: those sections are
// user-global only. userFile and projectFile are the raw file mirrors; both
// are updated in memory and, when anything changed, persisted (user file via
// writeUserConfigFile, project file in place at 0644).
//
// Per project entry: a name the user config lacks is moved wholesale
// (credentials included — the user file is the only place literal keys
// belong); a name whose non-credential fields match the user entry is
// dropped, carrying up any credential the user entry lacks; anything else is
// a conflict and stays project-local.
//
// Best-effort at the call site: on a write error the mirrors are left
// unpersisted as far as possible and the error is returned; the merged
// in-memory config the caller already built is unaffected either way.
func hoistProjectGlobals(userPath, projectPath string, userFile, projectFile *configFile) (hoistResult, error) {
	var res hoistResult
	var userChanged, projectChanged bool

	for name, p := range projectFile.Providers {
		u, ok := userFile.Providers[name]
		if !ok {
			if userFile.Providers == nil {
				userFile.Providers = map[string]ProviderConfig{}
			}
			userFile.Providers[name] = p
			delete(projectFile.Providers, name)
			res.providers = append(res.providers, name)
			userChanged, projectChanged = true, true
			continue
		}
		if providerFieldsEqual(u, p) {
			if u.APIKey == "" && p.APIKey != "" {
				u.APIKey = p.APIKey
				userChanged = true
			}
			if u.APIKeyEnv == "" && p.APIKeyEnv != "" {
				u.APIKeyEnv = p.APIKeyEnv
				userChanged = true
			}
			userFile.Providers[name] = u
			delete(projectFile.Providers, name)
			res.providers = append(res.providers, name)
			projectChanged = true
			continue
		}
		res.conflicts = append(res.conflicts, "providers."+name)
	}
	if len(projectFile.Providers) == 0 {
		projectFile.Providers = nil
	}

	if projectFile.Models != nil {
		for name, p := range projectFile.Models.Presets {
			var u routing.ModelPreset
			var ok bool
			if userFile.Models != nil {
				u, ok = userFile.Models.Presets[name]
			}
			if !ok {
				if userFile.Models == nil {
					userFile.Models = &fileModels{}
				}
				if userFile.Models.Presets == nil {
					userFile.Models.Presets = map[string]routing.ModelPreset{}
				}
				userFile.Models.Presets[name] = hoistNormalizedPreset(name, p)
				delete(projectFile.Models.Presets, name)
				res.presets = append(res.presets, name)
				userChanged, projectChanged = true, true
				continue
			}
			if reflect.DeepEqual(u, p) {
				delete(projectFile.Models.Presets, name)
				res.presets = append(res.presets, name)
				projectChanged = true
				continue
			}
			res.conflicts = append(res.conflicts, "models.presets."+name)
		}
		if len(projectFile.Models.Presets) == 0 {
			projectFile.Models = nil
		}
	}

	sort.Strings(res.providers)
	sort.Strings(res.presets)
	sort.Strings(res.conflicts)

	if !userChanged && !projectChanged {
		return res, nil
	}

	userData, err := toml.Marshal(userFile)
	if err != nil {
		return res, fmt.Errorf("marshal user config: %w", err)
	}
	projectData, err := toml.Marshal(projectFile)
	if err != nil {
		return res, fmt.Errorf("marshal project config: %w", err)
	}

	// Write the user file first: if the project write then fails, the hoisted
	// entries already live in the user config and the next load retries the
	// prune. The opposite order could strand entries removed from the project
	// file that never landed in the user file.
	if _, statErr := os.Stat(userPath); os.IsNotExist(statErr) {
		header := "# Marshal global configuration\n"
		userData = append([]byte(header), userData...)
	}
	if err := writeUserConfigFile(userPath, userData); err != nil {
		return res, fmt.Errorf("write user config: %w", err)
	}
	// The project file exists (the hoist only runs after it was loaded) and
	// must not be created here. The pruned mirror is written even when
	// nothing remains — an empty file is fine.
	if _, statErr := os.Stat(projectPath); statErr == nil {
		if err := os.WriteFile(projectPath, projectData, 0o644); err != nil {
			return res, fmt.Errorf("write project config: %w", err)
		}
	}
	return res, nil
}

// providerFieldsEqual compares the non-credential fields of two provider
// entries — the same field set the old project-layer diff used. Credentials
// are handled separately by the hoist (carry-up on drop).
func providerFieldsEqual(a, b ProviderConfig) bool {
	return a.Type == b.Type && a.BaseURL == b.BaseURL && a.Template == b.Template &&
		a.ToolCalling == b.ToolCalling && a.KeepAlive == b.KeepAlive &&
		a.ThinkingBudget == b.ThinkingBudget && a.ReasoningSummary == b.ReasoningSummary
}

// hoistNormalizedPreset derives the preset's identity from its map key the
// way the save path does, without touching the other stored fields. Only the
// pair-only case (no stored provider/model) is filled from the canonical
// "<provider>/<model>" key; a legacy non-canonical key keeps its stored
// provider/model so the merge-time legacy rename still applies.
func hoistNormalizedPreset(name string, p routing.ModelPreset) routing.ModelPreset {
	p.Name = name
	if p.Provider == "" && p.Model == "" {
		p.Provider, p.Model, _ = strings.Cut(name, "/")
	}
	return p
}
