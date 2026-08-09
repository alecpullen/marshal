package config

import (
	"fmt"
	"strings"

	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// RenameProvider rekeys a provider in cfg, updates every preset that
// references it, renames pair-keyed presets of the form old/<model>, and
// renames the in-memory discovered entry. The caller is responsible for
// renaming any on-disk model cache entry.
func RenameProvider(cfg *Config, old, new string, discovered map[string][]schema.ModelInfo) error {
	if new == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if old == new {
		return nil
	}
	pc, ok := cfg.Providers[old]
	if !ok {
		return fmt.Errorf("provider %q not found", old)
	}
	if _, exists := cfg.Providers[new]; exists {
		return fmt.Errorf("provider %q already exists", new)
	}

	delete(cfg.Providers, old)
	cfg.Providers[new] = pc

	if cfg.Models.Presets != nil {
		renamed := make(map[string]routing.ModelPreset, len(cfg.Models.Presets))
		for name, preset := range cfg.Models.Presets {
			if preset.Provider == old {
				preset.Provider = new
			}
			newName := name
			if prefix := old + "/"; strings.HasPrefix(name, prefix) {
				newName = new + "/" + strings.TrimPrefix(name, prefix)
			}
			renamed[newName] = preset
		}
		cfg.Models.Presets = renamed
	}

	if discovered != nil {
		if models, ok := discovered[old]; ok {
			discovered[new] = models
			delete(discovered, old)
		}
	}

	return nil
}
