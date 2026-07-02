package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"marshal/internal/llm/routing"
)

// SaveProjectConfig writes the essential settings-editable sections of cfg to
// path (typically .marshal/config.toml). It preserves any unrelated sections
// already present in the file.
func SaveProjectConfig(path string, cfg Config) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load existing project config: %w", err)
	}

	defaultProfile := cfg.Profile.Default
	file.Profile = &struct {
		Default *string `toml:"default"`
	}{Default: &defaultProfile}

	activePresetName := activePresetName(cfg)
	agentProvider := cfg.Agent.Provider
	agentModel := cfg.Agent.Model
	file.Agent = &struct {
		Provider *string `toml:"provider"`
		Model    *string `toml:"model"`
	}{Provider: &agentProvider, Model: &agentModel}

	remoteAllowed := cfg.Privacy.RemoteProvidersAllowed
	if file.Privacy == nil {
		file.Privacy = &struct {
			RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
			RedactSecrets          *bool `toml:"redact_secrets"`
			IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
		}{}
	}
	file.Privacy.RemoteProvidersAllowed = &remoteAllowed
	if activePresetName != "" {
		if preset, ok := cfg.Models.Presets[activePresetName]; ok {
			if file.Models == nil {
				file.Models = &struct {
					Presets map[string]modelPresetConfig `toml:"presets"`
				}{Presets: map[string]modelPresetConfig{}}
			}
			if file.Models.Presets == nil {
				file.Models.Presets = map[string]modelPresetConfig{}
			}
			file.Models.Presets[activePresetName] = modelPresetConfig{
				Provider:        preset.Provider,
				Model:           preset.Model,
				ContextWindow:   preset.ContextWindow,
				MaxOutputTokens: preset.MaxOutputTokens,
				Temperature:     preset.Temperature,
				TopP:            preset.TopP,
				ToolCalling:     preset.ToolCalling,
				ReasoningEffort: preset.ReasoningEffort,
				LocalOnly:       preset.LocalOnly,
			}
		}
	}

	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal project config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write project config: %w", err)
	}
	return nil
}

func activePresetName(cfg Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	presetName, ok := profile.Roles[routing.RoleImplementer]
	if !ok {
		return ""
	}
	return presetName
}
