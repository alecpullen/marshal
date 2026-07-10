package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pelletier/go-toml/v2"

	"marshal/internal/llm/routing"
)

func ptr[T any](v T) *T { return &v }

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

	def := Default()

	if file.Project != nil || !reflect.DeepEqual(cfg.Project, def.Project) {
		if file.Project != nil {
			file.Project = &fileProject{Name: file.Project.Name, Languages: file.Project.Languages}
		} else {
			file.Project = &fileProject{Name: ptr(cfg.Project.Name), Languages: cfg.Project.Languages}
		}
	}
	if file.Commands != nil || cfg.Commands != def.Commands {
		if file.Commands != nil {
			file.Commands = &fileCommands{Test: file.Commands.Test, Format: file.Commands.Format, Vet: file.Commands.Vet}
		} else {
			file.Commands = &fileCommands{Test: ptr(cfg.Commands.Test), Format: ptr(cfg.Commands.Format), Vet: ptr(cfg.Commands.Vet)}
		}
	}
	if file.Indexing != nil || !reflect.DeepEqual(cfg.Indexing, def.Indexing) {
		if file.Indexing != nil {
			file.Indexing = &fileIndexing{
				UseTreesitter:  file.Indexing.UseTreesitter,
				UseEmbeddings:  file.Indexing.UseEmbeddings,
				SummariseFiles: file.Indexing.SummariseFiles,
				Ignore:         file.Indexing.Ignore,
			}
		} else {
			file.Indexing = &fileIndexing{
				UseTreesitter:  ptr(cfg.Indexing.UseTreesitter),
				UseEmbeddings:  ptr(cfg.Indexing.UseEmbeddings),
				SummariseFiles: ptr(cfg.Indexing.SummariseFiles),
				Ignore:         cfg.Indexing.Ignore,
			}
		}
	}
	if file.Web != nil || cfg.Web != def.Web {
		if file.Web != nil {
			file.Web = &fileWeb{
				Enabled:        file.Web.Enabled,
				FetchTimeout:   file.Web.FetchTimeout,
				SearchProvider: file.Web.SearchProvider,
				SearchURL:      file.Web.SearchURL,
				SearchKey:      file.Web.SearchKey,
			}
		} else {
			file.Web = &fileWeb{
				Enabled:        ptr(cfg.Web.Enabled),
				FetchTimeout:   ptr(cfg.Web.FetchTimeout.String()),
				SearchProvider: ptr(cfg.Web.SearchProvider),
				SearchURL:      ptr(cfg.Web.SearchURL),
				SearchKey:      ptr(cfg.Web.SearchKey),
			}
		}
	}
	if file.Swarm != nil || !reflect.DeepEqual(cfg.Swarm, def.Swarm) {
		if file.Swarm != nil && file.Swarm.Budget != nil {
			b := file.Swarm.Budget
			file.Swarm = &fileSwarm{Budget: &fileSwarmBudget{
				MaxFixRounds:   b.MaxFixRounds,
				MaxTotalTokens: b.MaxTotalTokens,
				ToolIters:      b.ToolIters,
			}}
		} else {
			file.Swarm = &fileSwarm{Budget: &fileSwarmBudget{
				MaxFixRounds:   ptr(cfg.Swarm.Budget.MaxFixRounds),
				MaxTotalTokens: ptr(cfg.Swarm.Budget.MaxTotalTokens),
				ToolIters:      cfg.Swarm.Budget.ToolIters,
			}}
		}
	}
	if file.MCP != nil || !reflect.DeepEqual(cfg.MCP, def.MCP) {
		if file.MCP != nil {
			servers := map[string]fileMCPServer{}
			for name, srv := range file.MCP.Servers {
				servers[name] = fileMCPServer{Command: srv.Command, Args: srv.Args, Env: srv.Env}
			}
			policies := map[string]string{}
			for k, v := range file.MCP.Policies {
				policies[k] = v
			}
			file.MCP = &fileMCP{
				Servers:                  servers,
				Policies:                 policies,
				DisclosureThresholdTools: file.MCP.DisclosureThresholdTools,
			}
		} else {
			servers := map[string]fileMCPServer{}
			for name, srv := range cfg.MCP.Servers {
				servers[name] = fileMCPServer{Command: ptr(srv.Command), Args: srv.Args, Env: srv.Env}
			}
			file.MCP = &fileMCP{
				Servers:                  servers,
				Policies:                 cfg.MCP.Policies,
				DisclosureThresholdTools: ptr(cfg.MCP.DisclosureThresholdTools),
			}
		}
	}
	if file.Snapshots != nil || cfg.Snapshots != def.Snapshots {
		if file.Snapshots != nil {
			file.Snapshots = &fileSnapshots{
				Enabled:       file.Snapshots.Enabled,
				RetentionDays: file.Snapshots.RetentionDays,
				MaxFileBytes:  file.Snapshots.MaxFileBytes,
			}
		} else {
			file.Snapshots = &fileSnapshots{
				Enabled:       ptr(cfg.Snapshots.Enabled),
				RetentionDays: ptr(cfg.Snapshots.RetentionDays),
				MaxFileBytes:  ptr(cfg.Snapshots.MaxFileBytes),
			}
		}
	}
	if file.Permissions != nil || len(cfg.Permissions.Rules) > 0 {
		if file.Permissions != nil {
			file.Permissions = &filePermissions{Rules: file.Permissions.Rules}
		} else {
			file.Permissions = &filePermissions{Rules: cfg.Permissions.Rules}
		}
	}
	if file.Diagnostics != nil || !reflect.DeepEqual(cfg.Diagnostics, def.Diagnostics) {
		if file.Diagnostics != nil {
			cmds := map[string]string{}
			for k, v := range file.Diagnostics.Commands {
				cmds[k] = v
			}
			file.Diagnostics = &fileDiagnostics{Commands: cmds}
		} else {
			file.Diagnostics = &fileDiagnostics{Commands: cfg.Diagnostics.Commands}
		}
	}
	if file.Hooks != nil || !reflect.DeepEqual(cfg.Hooks, def.Hooks) {
		if file.Hooks != nil {
			entries := make([]fileHookEntry, 0, len(file.Hooks.Entries))
			for _, h := range file.Hooks.Entries {
				entries = append(entries, fileHookEntry{Event: h.Event, Matcher: h.Matcher, Command: h.Command, TimeoutMS: h.TimeoutMS})
			}
			file.Hooks = &fileHooks{FailClosed: file.Hooks.FailClosed, Entries: entries}
		} else {
			entries := make([]fileHookEntry, 0, len(cfg.Hooks.Entries))
			for _, h := range cfg.Hooks.Entries {
				entries = append(entries, fileHookEntry{Event: ptr(h.Event), Matcher: ptr(h.Matcher), Command: ptr(h.Command), TimeoutMS: ptr(h.TimeoutMS)})
			}
			file.Hooks = &fileHooks{FailClosed: ptr(cfg.Hooks.FailClosed), Entries: entries}
		}
	}
	if file.Providers != nil || len(cfg.Providers) > 0 {
		if file.Providers == nil {
			file.Providers = cfg.Providers
		}
	}
	if file.Models != nil || len(cfg.Models.Presets) > 0 {
		if file.Models == nil {
			file.Models = &fileModels{}
			file.Models.Presets = map[string]modelPresetConfig{}
			for name, p := range cfg.Models.Presets {
				file.Models.Presets[name] = modelPresetConfig{
					Provider: p.Provider, Model: p.Model, ContextWindow: p.ContextWindow,
					MaxOutputTokens: p.MaxOutputTokens, Temperature: p.Temperature, TopP: p.TopP,
					ToolCalling: p.ToolCalling, ReasoningEffort: p.ReasoningEffort, LocalOnly: p.LocalOnly,
				}
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
