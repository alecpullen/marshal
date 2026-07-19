package config

import (
	"fmt"
	"net/url"
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

	for name, p := range cfg.Providers {
		if err := validateProviderBaseURL(p.BaseURL); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
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
	redactSecrets := cfg.Privacy.RedactSecrets
	includeGitignoredFiles := cfg.Privacy.IncludeGitignoredFiles
	if file.Privacy == nil {
		file.Privacy = &filePrivacy{}
	}
	file.Privacy.RemoteProvidersAllowed = &remoteAllowed
	file.Privacy.RedactSecrets = &redactSecrets
	file.Privacy.IncludeGitignoredFiles = &includeGitignoredFiles

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
	autoApprove := cfg.Tools.Shell.AutoApprove
	file.Tools.Shell.DefaultTimeoutSeconds = &shellTimeout
	file.Tools.Shell.MaxOutputBytes = &maxOutputBytes
	file.Tools.Shell.MaxBackgroundJobs = &maxBackgroundJobs
	file.Tools.Shell.BackgroundRetention = &backgroundRetention
	file.Tools.Shell.AllowNetwork = &allowNetwork
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
	sandboxUnsafePassthrough := cfg.Tools.Shell.Sandbox.UnsafePassthrough
	file.Tools.Shell.Sandbox.Backend = &sandboxBackend
	file.Tools.Shell.Sandbox.MemoryLimitMB = &sandboxMemory
	file.Tools.Shell.Sandbox.CPUSeconds = &sandboxCPU
	file.Tools.Shell.Sandbox.MaxProcesses = &sandboxMaxProcs
	file.Tools.Shell.Sandbox.FileSizeLimitMB = &sandboxFileSize
	file.Tools.Shell.Sandbox.ContainerRuntime = &sandboxRuntime
	file.Tools.Shell.Sandbox.ContainerImage = &sandboxImage
	file.Tools.Shell.Sandbox.AllowFallback = &sandboxAllowFallback
	file.Tools.Shell.Sandbox.UnsafePassthrough = &sandboxUnsafePassthrough
	if cfg.Tools.Shell.Sandbox.EnvAllowlist != nil {
		file.Tools.Shell.Sandbox.EnvAllowlist = cfg.Tools.Shell.Sandbox.EnvAllowlist
	}
	if cfg.Tools.Shell.Sandbox.EnvDenylist != nil {
		file.Tools.Shell.Sandbox.EnvDenylist = cfg.Tools.Shell.Sandbox.EnvDenylist
	}

	def := Default()

	if file.Project != nil || !reflect.DeepEqual(cfg.Project, def.Project) {
		file.Project = &fileProject{Name: ptr(cfg.Project.Name), Languages: cfg.Project.Languages}
	}
	if file.Commands != nil || cfg.Commands != def.Commands {
		file.Commands = &fileCommands{Test: ptr(cfg.Commands.Test), Format: ptr(cfg.Commands.Format), Vet: ptr(cfg.Commands.Vet)}
	}
	if file.Indexing != nil || !reflect.DeepEqual(cfg.Indexing, def.Indexing) {
		file.Indexing = &fileIndexing{
			UseTreesitter:          ptr(cfg.Indexing.UseTreesitter),
			UseEmbeddings:          ptr(cfg.Indexing.UseEmbeddings),
			SummariseFiles:         ptr(cfg.Indexing.SummariseFiles),
			Ignore:                 cfg.Indexing.Ignore,
			MaxIndexableFileBytes:  ptr(cfg.Indexing.MaxIndexableFileBytes),
			MaxSearchableFileBytes: ptr(cfg.Indexing.MaxSearchableFileBytes),
		}
	}
	if file.Web != nil || cfg.Web != def.Web {
		file.Web = &fileWeb{
			Enabled:        ptr(cfg.Web.Enabled),
			FetchTimeout:   ptr(cfg.Web.FetchTimeout.String()),
			SearchProvider: ptr(cfg.Web.SearchProvider),
			SearchURL:      ptr(cfg.Web.SearchURL),
			SearchKey:      ptr(cfg.Web.SearchKey),
		}
	}
	if file.Desktop != nil || !reflect.DeepEqual(cfg.Desktop, def.Desktop) {
		file.Desktop = &fileDesktop{
			Enabled:          ptr(cfg.Desktop.Enabled),
			Mode:             ptr(cfg.Desktop.Mode),
			Headless:         ptr(cfg.Desktop.Headless),
			CDPURL:           ptr(cfg.Desktop.CDPURL),
			URLAllowlist:     cfg.Desktop.URLAllowlist,
			URLDenylist:      cfg.Desktop.URLDenylist,
			DefaultTimeout:   ptr(cfg.Desktop.DefaultTimeout.String()),
			ScreenshotFormat: ptr(cfg.Desktop.ScreenshotFormat),
		}
	}
	if file.TUI != nil || !reflect.DeepEqual(cfg.TUI, def.TUI) {
		file.TUI = &fileTUI{
			Theme:   ptr(cfg.TUI.Theme),
			Palette: cfg.TUI.Palette,
			Mode:    ptr(cfg.TUI.Mode),
		}
	}
	if file.Swarm != nil || !reflect.DeepEqual(cfg.Swarm, def.Swarm) {
		file.Swarm = &fileSwarm{Budget: &fileSwarmBudget{
			MaxFixRounds:   ptr(cfg.Swarm.Budget.MaxFixRounds),
			MaxTotalTokens: ptr(cfg.Swarm.Budget.MaxTotalTokens),
			ToolIters:      cfg.Swarm.Budget.ToolIters,
		}}
	}
	if file.SDD != nil || !reflect.DeepEqual(cfg.SDD, def.SDD) {
		file.SDD = &fileSDD{
			AutoWorktree:   ptr(cfg.SDD.AutoWorktree),
			MaxFixRounds:   ptr(cfg.SDD.MaxFixRounds),
			MaxTotalTokens: ptr(cfg.SDD.MaxTotalTokens),
			PlansDir:       ptr(cfg.SDD.PlansDir),
		}
	}
	if file.MCP != nil || !reflect.DeepEqual(cfg.MCP, def.MCP) {
		servers := map[string]fileMCPServer{}
		for name, srv := range cfg.MCP.Servers {
			servers[name] = fileMCPServer{Command: ptr(srv.Command), Args: srv.Args, Env: srv.Env, Trust: ptr(srv.Trust)}
		}
		file.MCP = &fileMCP{
			Servers:                  servers,
			Policies:                 cfg.MCP.Policies,
			DisclosureThresholdTools: ptr(cfg.MCP.DisclosureThresholdTools),
		}
	}
	if file.Snapshots != nil || cfg.Snapshots != def.Snapshots {
		file.Snapshots = &fileSnapshots{
			Enabled:       ptr(cfg.Snapshots.Enabled),
			RetentionDays: ptr(cfg.Snapshots.RetentionDays),
			MaxFileBytes:  ptr(cfg.Snapshots.MaxFileBytes),
		}
	}
	if file.Permissions != nil || len(cfg.Permissions.Rules) > 0 {
		file.Permissions = &filePermissions{Rules: cfg.Permissions.Rules}
	}
	if file.Diagnostics != nil || !reflect.DeepEqual(cfg.Diagnostics, def.Diagnostics) {
		file.Diagnostics = &fileDiagnostics{Commands: cfg.Diagnostics.Commands}
	}
	if file.Hooks != nil || !reflect.DeepEqual(cfg.Hooks, def.Hooks) {
		entries := make([]fileHookEntry, 0, len(cfg.Hooks.Entries))
		for _, h := range cfg.Hooks.Entries {
			entries = append(entries, fileHookEntry{Event: ptr(h.Event), Matcher: ptr(h.Matcher), Command: ptr(h.Command), TimeoutMS: ptr(h.TimeoutMS)})
		}
		file.Hooks = &fileHooks{FailClosed: ptr(cfg.Hooks.FailClosed), Entries: entries}
	}
	if file.Providers != nil || len(cfg.Providers) > 0 {
		file.Providers = cfg.Providers
	}
	if file.Models != nil || len(cfg.Models.Presets) > 0 {
		if file.Models == nil {
			file.Models = &fileModels{}
		}
		file.Models.Presets = map[string]modelPresetConfig{}
		for name, p := range cfg.Models.Presets {
			file.Models.Presets[name] = modelPresetConfig{
				Provider: p.Provider, Model: p.Model, ContextWindow: p.ContextWindow,
				MaxOutputTokens: p.MaxOutputTokens,
				ToolCalling: p.ToolCalling, LocalOnly: p.LocalOnly,
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

func SaveUserConfigProviderAPIKey(path, providerName, apiKey string) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	if file.Providers == nil {
		file.Providers = make(map[string]ProviderConfig, 1)
	}
	existing := file.Providers[providerName]
	existing.APIKey = apiKey
	existing.APIKeyEnv = "" // clear stale env-var reference when switching to inline
	file.Providers[providerName] = existing

	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal user config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// Prepend a minimal header if the file is being created for the first time.
	// loadFile returns an empty configFile when the file does not exist, so we
	// check whether the file exists on disk.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		header := "# Marshal global configuration\n"
		data = append([]byte(header), data...)
	}
	return os.WriteFile(path, data, 0644)
}

// validateProviderBaseURL returns an error if the base URL is not a
// parseable HTTP/HTTPS URL with a non-empty host. See F-SEC-30.
func validateProviderBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("provider base_url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("provider base_url %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("provider base_url %q: empty host", raw)
	}
	return nil
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
