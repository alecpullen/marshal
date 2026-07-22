package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pelletier/go-toml/v2"

	"marshal/internal/llm/routing"
	"marshal/internal/strutil"
)

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

	file.Profile = &fileProfile{Default: strutil.Ptr(cfg.Profile.Default)}

	activePresetName := activePresetName(cfg)
	if activePresetName == "" {
		file.Agent = &fileAgent{
			Provider:             strutil.Ptr(cfg.Agent.Provider),
			Model:                strutil.Ptr(cfg.Agent.Model),
			MaxToolIterations:    strutil.Ptr(cfg.Agent.MaxToolIterations),
			MaxRetries:           strutil.Ptr(cfg.Agent.MaxRetries),
			MaxTurnContextTokens: strutil.Ptr(cfg.Agent.MaxTurnContextTokens),
			PlanFirst:            strutil.Ptr(cfg.Agent.PlanFirst),
			SubtaskIterations:    strutil.Ptr(cfg.Agent.SubtaskIterations),
		}
	} else {
		file.Agent = &fileAgent{
			MaxToolIterations:    strutil.Ptr(cfg.Agent.MaxToolIterations),
			MaxRetries:           strutil.Ptr(cfg.Agent.MaxRetries),
			MaxTurnContextTokens: strutil.Ptr(cfg.Agent.MaxTurnContextTokens),
			PlanFirst:            strutil.Ptr(cfg.Agent.PlanFirst),
			SubtaskIterations:    strutil.Ptr(cfg.Agent.SubtaskIterations),
		}
	}

	if file.Privacy == nil {
		file.Privacy = &filePrivacy{}
	}
	file.Privacy.RemoteProvidersAllowed = strutil.Ptr(cfg.Privacy.RemoteProvidersAllowed)
	file.Privacy.RedactSecrets = strutil.Ptr(cfg.Privacy.RedactSecrets)
	file.Privacy.IncludeGitignoredFiles = strutil.Ptr(cfg.Privacy.IncludeGitignoredFiles)

	if file.Tools == nil {
		file.Tools = &fileTools{}
	}
	if file.Tools.Shell == nil {
		file.Tools.Shell = &fileShell{}
	}
	file.Tools.Shell.DefaultTimeoutSeconds = strutil.Ptr(cfg.Tools.Shell.DefaultTimeoutSeconds)
	file.Tools.Shell.MaxOutputBytes = strutil.Ptr(cfg.Tools.Shell.MaxOutputBytes)
	file.Tools.Shell.MaxBackgroundJobs = strutil.Ptr(cfg.Tools.Shell.MaxBackgroundJobs)
	file.Tools.Shell.BackgroundRetention = strutil.Ptr(cfg.Tools.Shell.BackgroundRetention.String())
	file.Tools.Shell.AllowNetwork = strutil.Ptr(cfg.Tools.Shell.AllowNetwork)
	file.Tools.Shell.AutoApprove = strutil.Ptr(cfg.Tools.Shell.AutoApprove)

	file.Tools.Shell.GuardrailDynamicArgv0 = strutil.Ptr(cfg.Tools.Shell.GuardrailDynamicArgv0)

	if file.Tools.Shell.Sandbox == nil {
		file.Tools.Shell.Sandbox = &sandboxFile{}
	}
	file.Tools.Shell.Sandbox.Backend = strutil.Ptr(cfg.Tools.Shell.Sandbox.Backend)
	file.Tools.Shell.Sandbox.MemoryLimitMB = strutil.Ptr(cfg.Tools.Shell.Sandbox.MemoryLimitMB)
	file.Tools.Shell.Sandbox.CPUSeconds = strutil.Ptr(cfg.Tools.Shell.Sandbox.CPUSeconds)
	file.Tools.Shell.Sandbox.MaxProcesses = strutil.Ptr(cfg.Tools.Shell.Sandbox.MaxProcesses)
	file.Tools.Shell.Sandbox.FileSizeLimitMB = strutil.Ptr(cfg.Tools.Shell.Sandbox.FileSizeLimitMB)
	file.Tools.Shell.Sandbox.ContainerRuntime = strutil.Ptr(cfg.Tools.Shell.Sandbox.ContainerRuntime)
	file.Tools.Shell.Sandbox.ContainerImage = strutil.Ptr(cfg.Tools.Shell.Sandbox.ContainerImage)
	file.Tools.Shell.Sandbox.AllowFallback = strutil.Ptr(cfg.Tools.Shell.Sandbox.AllowFallback)
	file.Tools.Shell.Sandbox.UnsafePassthrough = strutil.Ptr(cfg.Tools.Shell.Sandbox.UnsafePassthrough)
	if cfg.Tools.Shell.Sandbox.EnvAllowlist != nil {
		file.Tools.Shell.Sandbox.EnvAllowlist = cfg.Tools.Shell.Sandbox.EnvAllowlist
	}
	if cfg.Tools.Shell.Sandbox.EnvDenylist != nil {
		file.Tools.Shell.Sandbox.EnvDenylist = cfg.Tools.Shell.Sandbox.EnvDenylist
	}

	// Guard policy: profile/agent/privacy/shell/sandbox sections are written
	// unconditionally (they are the settings UI's primary targets); every
	// other section is written only when the file already has it or the
	// value differs from Default(). Callers pass merged user+project
	// config, so unconditional sections bake user-global values into the
	// project file — that is deliberate for the primary sections.
	def := Default()

	if file.Project != nil || !reflect.DeepEqual(cfg.Project, def.Project) {
		file.Project = &fileProject{Name: strutil.Ptr(cfg.Project.Name), Languages: cfg.Project.Languages}
	}
	if file.Commands != nil || cfg.Commands != def.Commands {
		file.Commands = &fileCommands{Test: strutil.Ptr(cfg.Commands.Test), Format: strutil.Ptr(cfg.Commands.Format), Vet: strutil.Ptr(cfg.Commands.Vet)}
	}
	if file.Indexing != nil || !reflect.DeepEqual(cfg.Indexing, def.Indexing) {
		file.Indexing = &fileIndexing{
			UseTreesitter:          strutil.Ptr(cfg.Indexing.UseTreesitter),
			UseEmbeddings:          strutil.Ptr(cfg.Indexing.UseEmbeddings),
			SummariseFiles:         strutil.Ptr(cfg.Indexing.SummariseFiles),
			Ignore:                 cfg.Indexing.Ignore,
			MaxIndexableFileBytes:  strutil.Ptr(cfg.Indexing.MaxIndexableFileBytes),
			MaxSearchableFileBytes: strutil.Ptr(cfg.Indexing.MaxSearchableFileBytes),
		}
	}
	if file.Web != nil || cfg.Web != def.Web {
		file.Web = &fileWeb{
			Enabled:        strutil.Ptr(cfg.Web.Enabled),
			FetchTimeout:   strutil.Ptr(cfg.Web.FetchTimeout.String()),
			SearchProvider: strutil.Ptr(cfg.Web.SearchProvider),
			SearchURL:      strutil.Ptr(cfg.Web.SearchURL),
			SearchKey:      strutil.Ptr(cfg.Web.SearchKey),
		}
	}
	if file.Desktop != nil || !reflect.DeepEqual(cfg.Desktop, def.Desktop) {
		file.Desktop = &fileDesktop{
			Enabled:          strutil.Ptr(cfg.Desktop.Enabled),
			Mode:             strutil.Ptr(cfg.Desktop.Mode),
			Headless:         strutil.Ptr(cfg.Desktop.Headless),
			CDPURL:           strutil.Ptr(cfg.Desktop.CDPURL),
			URLAllowlist:     cfg.Desktop.URLAllowlist,
			URLDenylist:      cfg.Desktop.URLDenylist,
			DefaultTimeout:   strutil.Ptr(cfg.Desktop.DefaultTimeout.String()),
			ScreenshotFormat: strutil.Ptr(cfg.Desktop.ScreenshotFormat),
		}
	}
	if file.TUI != nil || !reflect.DeepEqual(cfg.TUI, def.TUI) {
		file.TUI = &fileTUI{
			Theme:   strutil.Ptr(cfg.TUI.Theme),
			Palette: cfg.TUI.Palette,
			Mode:    strutil.Ptr(cfg.TUI.Mode),
		}
	}
	if file.Swarm != nil || !reflect.DeepEqual(cfg.Swarm, def.Swarm) {
		file.Swarm = &fileSwarm{Budget: &fileSwarmBudget{
			MaxFixRounds:   strutil.Ptr(cfg.Swarm.Budget.MaxFixRounds),
			MaxTotalTokens: strutil.Ptr(cfg.Swarm.Budget.MaxTotalTokens),
			ToolIters:      cfg.Swarm.Budget.ToolIters,
		}}
	}
	if file.SDD != nil || !reflect.DeepEqual(cfg.SDD, def.SDD) {
		file.SDD = &fileSDD{
			AutoWorktree: strutil.Ptr(cfg.SDD.AutoWorktree),
			MaxFixRounds: strutil.Ptr(cfg.SDD.MaxFixRounds),
			PlansDir:     strutil.Ptr(cfg.SDD.PlansDir),
		}
	}
	if file.MCP != nil || !reflect.DeepEqual(cfg.MCP, def.MCP) {
		servers := map[string]fileMCPServer{}
		for name, srv := range cfg.MCP.Servers {
			servers[name] = fileMCPServer{Command: strutil.Ptr(srv.Command), Args: srv.Args, Env: srv.Env, Trust: strutil.Ptr(srv.Trust)}
		}
		file.MCP = &fileMCP{
			Servers:                  servers,
			Policies:                 cfg.MCP.Policies,
			DisclosureThresholdTools: strutil.Ptr(cfg.MCP.DisclosureThresholdTools),
		}
	}
	if file.Snapshots != nil || cfg.Snapshots != def.Snapshots {
		file.Snapshots = &fileSnapshots{
			Enabled:       strutil.Ptr(cfg.Snapshots.Enabled),
			RetentionDays: strutil.Ptr(cfg.Snapshots.RetentionDays),
			MaxFileBytes:  strutil.Ptr(cfg.Snapshots.MaxFileBytes),
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
			entries = append(entries, fileHookEntry{Event: strutil.Ptr(h.Event), Matcher: strutil.Ptr(h.Matcher), Command: strutil.Ptr(h.Command), TimeoutMS: strutil.Ptr(h.TimeoutMS)})
		}
		file.Hooks = &fileHooks{FailClosed: strutil.Ptr(cfg.Hooks.FailClosed), Entries: entries}
	}
	if file.Providers != nil || len(cfg.Providers) > 0 {
		file.Providers = cfg.Providers
	}
	if file.Models != nil || len(cfg.Models.Presets) > 0 {
		if file.Models == nil {
			file.Models = &fileModels{}
		}
		file.Models.Presets = map[string]routing.ModelPreset{}
		for name, p := range cfg.Models.Presets {
			preset := routing.ModelPreset{
				Provider: p.Provider, Model: p.Model, ContextWindow: p.ContextWindow,
				MaxOutputTokens: p.MaxOutputTokens,
				ToolCalling:     p.ToolCalling, LocalOnly: p.LocalOnly,
			}
			preset.Name = name
			file.Models.Presets[name] = preset
		}
	}
	if file.AgentProfiles != nil || len(cfg.AgentProfiles) > 0 {
		file.AgentProfiles = map[string]map[routing.AgentRole]string{}
		for name, profile := range cfg.AgentProfiles {
			roles := profile.Roles
			if roles == nil {
				roles = map[routing.AgentRole]string{}
			}
			file.AgentProfiles[name] = roles
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
