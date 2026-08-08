package presetflow

import (
	"fmt"
	"sort"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/probe"
	"marshal/internal/llm/routing"
)

// Materialize ensures cfg has a preset named exactly "<provider>/<model>"
// for providerName+modelID, creating it if absent or updating its limits
// and tool-calling if present, and returns the preset's name. Preset names
// are mechanical and never hand-renamed.
//
// Zero fields in lim never overwrite a previously-saved non-zero preset
// field — re-materializing a model whose limits came back unknown this
// time must not erase figures confirmed earlier.
func Materialize(cfg *config.Config, providerName, modelID, baseURL string, lim Limits) (string, error) {
	name := providerName + "/" + modelID
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	preset, exists := cfg.Models.Presets[name]
	if exists &&
		((preset.Provider != "" && preset.Provider != providerName) ||
			(preset.Model != "" && preset.Model != modelID)) {
		return "", fmt.Errorf("preset %q is already used by %s/%s", name, preset.Provider, preset.Model)
	}
	if !exists {
		keys := make([]string, 0, len(cfg.Models.Presets))
		for key := range cfg.Models.Presets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			candidate := cfg.Models.Presets[key]
			if candidate.Provider == providerName && candidate.Model == modelID {
				preset = candidate
				break
			}
		}
	}
	preset.Name = name
	preset.Provider = providerName
	preset.Model = modelID
	if lim.ContextWindow != 0 {
		preset.ContextWindow = lim.ContextWindow
	}
	if lim.MaxOutputTokens != 0 {
		preset.MaxOutputTokens = lim.MaxOutputTokens
	}
	if lim.ToolCalling != nil {
		if *lim.ToolCalling {
			preset.ToolCalling = "native"
		} else {
			preset.ToolCalling = "none"
		}
	}
	preset.LocalOnly = probe.IsLocalhost(baseURL)
	cfg.Models.Presets[name] = preset
	return name, nil
}
