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
	maxToolIterations := cfg.Agent.MaxToolIterations
	maxRetries := cfg.Agent.MaxRetries
	maxTurnContextTokens := cfg.Agent.MaxTurnContextTokens
	if activePresetName == "" {
		agentProvider := cfg.Agent.Provider
		agentModel := cfg.Agent.Model
		file.Agent = &struct {
			Provider                 *string `toml:"provider"`
			Model                    *string `toml:"model"`
			MaxToolIterations        *int    `toml:"max_tool_iterations"`
			MaxRetries               *int    `toml:"max_retries"`
			MaxTurnContextTokens     *int    `toml:"max_turn_context_tokens"`
			MaxStructuredOutputChars *int    `toml:"max_structured_output_chars"`
		}{Provider: &agentProvider, Model: &agentModel, MaxToolIterations: &maxToolIterations, MaxRetries: &maxRetries, MaxTurnContextTokens: &maxTurnContextTokens}
	} else {
		file.Agent = &struct {
			Provider                 *string `toml:"provider"`
			Model                    *string `toml:"model"`
			MaxToolIterations        *int    `toml:"max_tool_iterations"`
			MaxRetries               *int    `toml:"max_retries"`
			MaxTurnContextTokens     *int    `toml:"max_turn_context_tokens"`
			MaxStructuredOutputChars *int    `toml:"max_structured_output_chars"`
		}{MaxToolIterations: &maxToolIterations, MaxRetries: &maxRetries, MaxTurnContextTokens: &maxTurnContextTokens}
	}

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

	if file.Tools == nil {
		file.Tools = &struct {
			Shell *struct {
				DefaultTimeoutSeconds *int          `toml:"default_timeout_seconds"`
				MaxOutputBytes        *int          `toml:"max_output_bytes"`
				AllowNetwork          *bool         `toml:"allow_network"`
				AllowSudo             *bool         `toml:"allow_sudo"`
				AllowDestructive      *bool         `toml:"allow_destructive"`
				AutoApprove           *bool         `toml:"auto_approve"`
				GuardrailDynamicArgv0 *string       `toml:"guardrail_dynamic_argv0"`
				Allow                 *CommandRules `toml:"allow"`
				Confirm               *CommandRules `toml:"confirm"`
				Deny                  *PatternRules `toml:"deny"`
				Sandbox               *sandboxFile  `toml:"sandbox"`
			} `toml:"shell"`
		}{}
	}
	if file.Tools.Shell == nil {
		file.Tools.Shell = &struct {
			DefaultTimeoutSeconds *int          `toml:"default_timeout_seconds"`
			MaxOutputBytes        *int          `toml:"max_output_bytes"`
			AllowNetwork          *bool         `toml:"allow_network"`
			AllowSudo             *bool         `toml:"allow_sudo"`
			AllowDestructive      *bool         `toml:"allow_destructive"`
			AutoApprove           *bool         `toml:"auto_approve"`
			GuardrailDynamicArgv0 *string       `toml:"guardrail_dynamic_argv0"`
			Allow                 *CommandRules `toml:"allow"`
			Confirm               *CommandRules `toml:"confirm"`
			Deny                  *PatternRules `toml:"deny"`
			Sandbox               *sandboxFile  `toml:"sandbox"`
		}{}
	}
	shellTimeout := cfg.Tools.Shell.DefaultTimeoutSeconds
	maxOutputBytes := cfg.Tools.Shell.MaxOutputBytes
	allowNetwork := cfg.Tools.Shell.AllowNetwork
	allowSudo := cfg.Tools.Shell.AllowSudo
	allowDestructive := cfg.Tools.Shell.AllowDestructive
	autoApprove := cfg.Tools.Shell.AutoApprove
	file.Tools.Shell.DefaultTimeoutSeconds = &shellTimeout
	file.Tools.Shell.MaxOutputBytes = &maxOutputBytes
	file.Tools.Shell.AllowNetwork = &allowNetwork
	file.Tools.Shell.AllowSudo = &allowSudo
	file.Tools.Shell.AllowDestructive = &allowDestructive
	file.Tools.Shell.AutoApprove = &autoApprove

	guardrailDyn := cfg.Tools.Shell.GuardrailDynamicArgv0
	file.Tools.Shell.GuardrailDynamicArgv0 = &guardrailDyn

	if file.Tools.Shell.Sandbox == nil {
		file.Tools.Shell.Sandbox = &sandboxFile{}
	}
	sandboxBackend := cfg.Tools.Shell.Sandbox.Backend
	sandboxMemory := cfg.Tools.Shell.Sandbox.MemoryLimitMB
	sandboxCPU := cfg.Tools.Shell.Sandbox.CPUSeconds
	sandboxMaxProcs := cfg.Tools.Shell.Sandbox.MaxProcesses
	sandboxFileSize := cfg.Tools.Shell.Sandbox.FileSizeLimitMB
	sandboxRuntime := cfg.Tools.Shell.Sandbox.ContainerRuntime
	sandboxImage := cfg.Tools.Shell.Sandbox.ContainerImage
	sandboxAllowFallback := cfg.Tools.Shell.Sandbox.AllowFallback
	file.Tools.Shell.Sandbox.Backend = &sandboxBackend
	file.Tools.Shell.Sandbox.MemoryLimitMB = &sandboxMemory
	file.Tools.Shell.Sandbox.CPUSeconds = &sandboxCPU
	file.Tools.Shell.Sandbox.MaxProcesses = &sandboxMaxProcs
	file.Tools.Shell.Sandbox.FileSizeLimitMB = &sandboxFileSize
	file.Tools.Shell.Sandbox.ContainerRuntime = &sandboxRuntime
	file.Tools.Shell.Sandbox.ContainerImage = &sandboxImage
	file.Tools.Shell.Sandbox.AllowFallback = &sandboxAllowFallback
	if cfg.Tools.Shell.Sandbox.EnvAllowlist != nil {
		file.Tools.Shell.Sandbox.EnvAllowlist = cfg.Tools.Shell.Sandbox.EnvAllowlist
	}
	if cfg.Tools.Shell.Sandbox.EnvDenylist != nil {
		file.Tools.Shell.Sandbox.EnvDenylist = cfg.Tools.Shell.Sandbox.EnvDenylist
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

func SaveUserConfigRule(path string, rule PermissionRule) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	if file.Permissions == nil {
		file.Permissions = &struct {
			Rules []PermissionRule `toml:"rules"`
		}{}
	}
	for _, existing := range file.Permissions.Rules {
		if existing.Permission == rule.Permission && existing.Pattern == rule.Pattern && existing.Action == rule.Action {
			return nil
		}
	}
	file.Permissions.Rules = append(file.Permissions.Rules, rule)
	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal user config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
