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
	file.Profile = &fileProfile{Default: &defaultProfile}

	activePresetName := activePresetName(cfg)
	maxToolIterations := cfg.Agent.MaxToolIterations
	maxRetries := cfg.Agent.MaxRetries
	maxTurnContextTokens := cfg.Agent.MaxTurnContextTokens
	planFirst := cfg.Agent.PlanFirst
	subtaskIterations := cfg.Agent.SubtaskIterations
	if activePresetName == "" {
		agentProvider := cfg.Agent.Provider
		agentModel := cfg.Agent.Model
		file.Agent = &fileAgent{Provider: &agentProvider, Model: &agentModel, MaxToolIterations: &maxToolIterations, MaxRetries: &maxRetries, MaxTurnContextTokens: &maxTurnContextTokens, PlanFirst: &planFirst, SubtaskIterations: &subtaskIterations}
	} else {
		file.Agent = &fileAgent{MaxToolIterations: &maxToolIterations, MaxRetries: &maxRetries, MaxTurnContextTokens: &maxTurnContextTokens, PlanFirst: &planFirst, SubtaskIterations: &subtaskIterations}
	}

	remoteAllowed := cfg.Privacy.RemoteProvidersAllowed
	if file.Privacy == nil {
		file.Privacy = &filePrivacy{}
	}
	file.Privacy.RemoteProvidersAllowed = &remoteAllowed
	if activePresetName != "" {
		if preset, ok := cfg.Models.Presets[activePresetName]; ok {
			if file.Models == nil {
				file.Models = &fileModels{Presets: map[string]modelPresetConfig{}}
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
		file.Tools = &fileTools{}
	}
	if file.Tools.Shell == nil {
		file.Tools.Shell = &fileShell{}
	}
	shellTimeout := cfg.Tools.Shell.DefaultTimeoutSeconds
	maxOutputBytes := cfg.Tools.Shell.MaxOutputBytes
	maxBackgroundJobs := cfg.Tools.Shell.MaxBackgroundJobs
	backgroundRetention := cfg.Tools.Shell.BackgroundRetention.String()
	allowNetwork := cfg.Tools.Shell.AllowNetwork
	allowSudo := cfg.Tools.Shell.AllowSudo
	allowDestructive := cfg.Tools.Shell.AllowDestructive
	autoApprove := cfg.Tools.Shell.AutoApprove
	file.Tools.Shell.DefaultTimeoutSeconds = &shellTimeout
	file.Tools.Shell.MaxOutputBytes = &maxOutputBytes
	file.Tools.Shell.MaxBackgroundJobs = &maxBackgroundJobs
	file.Tools.Shell.BackgroundRetention = &backgroundRetention
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
		file.Permissions = &filePermissions{}
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
