package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"marshal/internal/llm/routing"
	"marshal/internal/strutil"
)

// SaveProjectConfig writes the essential settings-editable sections of cfg to
// path (typically .marshal/config.toml). It preserves any unrelated sections
// already present in the file.
//
// layers carries the load-time snapshots (config.LoadLayers). Only values
// the project layer itself contributes are persisted: a section whose
// merged value equals the user-layer value came from the user config or
// defaults, so writing it here would bake machine-local state into a
// shared, often-committed file. A zero Layers (tests, callers without
// snapshots) preserves the historical behaviour.
func SaveProjectConfig(path string, cfg Config, layers Layers) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load existing project config: %w", err)
	}

	for name, p := range cfg.Providers {
		if err := validateProviderBaseURL(p.BaseURL); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
	}

	// [providers] and [models.presets] are user-global only: writeSections
	// never emits them, so API keys cannot land in this shared,
	// often-committed file (they live in the user config; see
	// SaveUserConfigProviderAPIKey and writeGlobalSections).
	writeSections(&file, cfg, Default())
	applyProjectLayer(&file, cfg, layers)

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

// SaveVerifyCommands persists just the [sdd.verify] build/test fields to the
// project config at path, preserving all other sections and fields. It is
// used by the /sdd offer-to-fill flow; a write failure must not block the run.
func SaveVerifyCommands(path, build, test string) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load existing project config: %w", err)
	}
	if file.SDD == nil {
		file.SDD = &fileSDD{}
	}
	if file.SDD.Verify == nil {
		file.SDD.Verify = &fileSDDVerify{}
	}
	file.SDD.Verify.Build = strutil.Ptr(build)
	file.SDD.Verify.Test = strutil.Ptr(test)

	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal project config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write project config: %w", err)
	}
	return nil
}

// SaveUserConfigSection writes the editable sections of cfg to the user-global
// config file at path, preserving unrelated sections already present. It
// mirrors SaveProjectConfig's section-preservation logic against the global
// file. Used by the agent config.* tools for global-scope writes.
func SaveUserConfigSection(path string, cfg Config) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load existing user config: %w", err)
	}
	for name, p := range cfg.Providers {
		if err := validateProviderBaseURL(p.BaseURL); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
	}
	writeSections(&file, cfg, Default())
	// [providers] and [models.presets] are user-global only — SaveProjectConfig
	// deliberately never calls writeGlobalSections.
	writeGlobalSections(&file, cfg)
	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal user config: %w", err)
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		header := "# Marshal global configuration\n"
		data = append([]byte(header), data...)
	}
	return writeUserConfigFile(path, data)
}

// putKey writes merged into dst when the file already carries the key
// (fileVal != nil) or when merged differs from the default. This prevents
// keys the user deleted from the file from reappearing when the running
// merged config happens to hold the default value.
func putKey[T comparable](dst **T, fileVal *T, merged, def T) {
	if fileVal != nil || merged != def {
		*dst = strutil.Ptr(merged)
	}
}

func fileField[T any](section any, name string) *T {
	if section == nil {
		return nil
	}
	v := reflect.ValueOf(section)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	f := v.FieldByName(name)
	if !f.IsValid() || f.IsNil() {
		return nil
	}
	return f.Interface().(*T)
}

func fileSlice[T any](section any, name string) []T {
	if section == nil {
		return nil
	}
	v := reflect.ValueOf(section)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	f := v.FieldByName(name)
	if !f.IsValid() || f.IsNil() {
		return nil
	}
	return f.Interface().([]T)
}

// putSlice writes merged into dst when the file already carries the key
// or when merged differs from the default (using DeepEqual for slices).
func putSlice[T any](dst *[]T, fileVal []T, merged, def []T) {
	if len(fileVal) > 0 || !reflect.DeepEqual(merged, def) {
		*dst = merged
	}
}

// writeSections applies every editable section of cfg onto file in place,
// preserving sections that are absent from cfg (nil in file) and equal to
// Default. Shared by SaveProjectConfig and SaveUserConfigSection so both
// files get identical section-preservation semantics.
func writeSections(file *configFile, cfg Config, def Config) {
	if file.Tools == nil {
		file.Tools = &fileTools{}
	}
	profile := &fileProfile{}
	putKey(&profile.Default, fileField[string](file.Profile, "Default"), cfg.Profile.Default, def.Profile.Default)
	putKey(&profile.ActivePreset, fileField[string](file.Profile, "ActivePreset"), cfg.Profile.ActivePreset, def.Profile.ActivePreset)
	file.Profile = profile

	agent := &fileAgent{}
	putKey(&agent.MaxToolIterations, fileField[int](file.Agent, "MaxToolIterations"), cfg.Agent.MaxToolIterations, def.Agent.MaxToolIterations)
	putKey(&agent.MaxRetries, fileField[int](file.Agent, "MaxRetries"), cfg.Agent.MaxRetries, def.Agent.MaxRetries)
	putKey(&agent.MaxTurnContextTokens, fileField[int](file.Agent, "MaxTurnContextTokens"), cfg.Agent.MaxTurnContextTokens, def.Agent.MaxTurnContextTokens)
	putKey(&agent.ReconnectMaxWaitSeconds, fileField[int](file.Agent, "ReconnectMaxWaitSeconds"), cfg.Agent.ReconnectMaxWaitSeconds, def.Agent.ReconnectMaxWaitSeconds)
	putKey(&agent.MaxToolResultChars, fileField[int](file.Agent, "MaxToolResultChars"), cfg.Agent.MaxToolResultChars, def.Agent.MaxToolResultChars)
	putKey(&agent.MaxStructuredOutputChars, fileField[int](file.Agent, "MaxStructuredOutputChars"), cfg.Agent.MaxStructuredOutputChars, def.Agent.MaxStructuredOutputChars)
	putKey(&agent.PlanFirst, fileField[bool](file.Agent, "PlanFirst"), cfg.Agent.PlanFirst, def.Agent.PlanFirst)
	putKey(&agent.SubtaskIterations, fileField[int](file.Agent, "SubtaskIterations"), cfg.Agent.SubtaskIterations, def.Agent.SubtaskIterations)
	putKey(&agent.ApprovalMode, fileField[string](file.Agent, "ApprovalMode"), cfg.Agent.ApprovalMode, def.Agent.ApprovalMode)
	putKey(&agent.HistoryBudgetTokens, fileField[int](file.Agent, "HistoryBudgetTokens"), cfg.Agent.HistoryBudgetTokens, def.Agent.HistoryBudgetTokens)
	// ParseRepairFeedback is a *bool (tri-state). putKey can't infer a **bool
	// target, so mirror its "write if file had it OR merged differs from
	// default" semantics manually; the default is nil, so "differs" collapses
	// to merged != nil.
	if fileField[bool](file.Agent, "ParseRepairFeedback") != nil || cfg.Agent.ParseRepairFeedback != nil {
		agent.ParseRepairFeedback = cfg.Agent.ParseRepairFeedback
	}
	putKey(&agent.MaxTouchedFileBytes, fileField[int](file.Agent, "MaxTouchedFileBytes"), cfg.Agent.MaxTouchedFileBytes, def.Agent.MaxTouchedFileBytes)
	putKey(&agent.ThinkingBudgetMargin, fileField[int](file.Agent, "ThinkingBudgetMargin"), cfg.Agent.ThinkingBudgetMargin, def.Agent.ThinkingBudgetMargin)
	putKey(&agent.MaxConcurrentSubagents, fileField[int](file.Agent, "MaxConcurrentSubagents"), cfg.Agent.MaxConcurrentSubagents, def.Agent.MaxConcurrentSubagents)
	file.Agent = agent

	privacy := &filePrivacy{}
	putKey(&privacy.RemoteProvidersAllowed, fileField[bool](file.Privacy, "RemoteProvidersAllowed"), cfg.Privacy.RemoteProvidersAllowed, def.Privacy.RemoteProvidersAllowed)
	putKey(&privacy.RemoteLimitDiscovery, fileField[bool](file.Privacy, "RemoteLimitDiscovery"), cfg.Privacy.RemoteLimitDiscovery, def.Privacy.RemoteLimitDiscovery)
	putKey(&privacy.RedactSecrets, fileField[bool](file.Privacy, "RedactSecrets"), cfg.Privacy.RedactSecrets, def.Privacy.RedactSecrets)
	putKey(&privacy.IncludeGitignoredFiles, fileField[bool](file.Privacy, "IncludeGitignoredFiles"), cfg.Privacy.IncludeGitignoredFiles, def.Privacy.IncludeGitignoredFiles)
	file.Privacy = privacy

	if file.Tools.Shell == nil {
		file.Tools.Shell = &fileShell{}
	}
	shell := &fileShell{}
	putKey(&shell.DefaultTimeoutSeconds, fileField[int](file.Tools.Shell, "DefaultTimeoutSeconds"), cfg.Tools.Shell.DefaultTimeoutSeconds, def.Tools.Shell.DefaultTimeoutSeconds)
	putKey(&shell.MaxOutputBytes, fileField[int](file.Tools.Shell, "MaxOutputBytes"), cfg.Tools.Shell.MaxOutputBytes, def.Tools.Shell.MaxOutputBytes)
	putKey(&shell.MaxBackgroundJobs, fileField[int](file.Tools.Shell, "MaxBackgroundJobs"), cfg.Tools.Shell.MaxBackgroundJobs, def.Tools.Shell.MaxBackgroundJobs)
	putKey(&shell.BackgroundRetention, fileField[string](file.Tools.Shell, "BackgroundRetention"), cfg.Tools.Shell.BackgroundRetention.String(), def.Tools.Shell.BackgroundRetention.String())
	putKey(&shell.AllowNetwork, fileField[bool](file.Tools.Shell, "AllowNetwork"), cfg.Tools.Shell.AllowNetwork, def.Tools.Shell.AllowNetwork)
	putKey(&shell.AutoApprove, fileField[bool](file.Tools.Shell, "AutoApprove"), cfg.Tools.Shell.AutoApprove, def.Tools.Shell.AutoApprove)
	putKey(&shell.GuardrailDynamicArgv0, fileField[string](file.Tools.Shell, "GuardrailDynamicArgv0"), cfg.Tools.Shell.GuardrailDynamicArgv0, def.Tools.Shell.GuardrailDynamicArgv0)

	if file.Tools.Shell.Sandbox == nil {
		file.Tools.Shell.Sandbox = &sandboxFile{}
	}
	sandbox := &sandboxFile{}
	putKey(&sandbox.Backend, fileField[string](file.Tools.Shell.Sandbox, "Backend"), cfg.Tools.Shell.Sandbox.Backend, def.Tools.Shell.Sandbox.Backend)
	putKey(&sandbox.MemoryLimitMB, fileField[int](file.Tools.Shell.Sandbox, "MemoryLimitMB"), cfg.Tools.Shell.Sandbox.MemoryLimitMB, def.Tools.Shell.Sandbox.MemoryLimitMB)
	putKey(&sandbox.CPUSeconds, fileField[int](file.Tools.Shell.Sandbox, "CPUSeconds"), cfg.Tools.Shell.Sandbox.CPUSeconds, def.Tools.Shell.Sandbox.CPUSeconds)
	putKey(&sandbox.MaxProcesses, fileField[int](file.Tools.Shell.Sandbox, "MaxProcesses"), cfg.Tools.Shell.Sandbox.MaxProcesses, def.Tools.Shell.Sandbox.MaxProcesses)
	putKey(&sandbox.FileSizeLimitMB, fileField[int](file.Tools.Shell.Sandbox, "FileSizeLimitMB"), cfg.Tools.Shell.Sandbox.FileSizeLimitMB, def.Tools.Shell.Sandbox.FileSizeLimitMB)
	putKey(&sandbox.ContainerRuntime, fileField[string](file.Tools.Shell.Sandbox, "ContainerRuntime"), cfg.Tools.Shell.Sandbox.ContainerRuntime, def.Tools.Shell.Sandbox.ContainerRuntime)
	putKey(&sandbox.ContainerImage, fileField[string](file.Tools.Shell.Sandbox, "ContainerImage"), cfg.Tools.Shell.Sandbox.ContainerImage, def.Tools.Shell.Sandbox.ContainerImage)
	putKey(&sandbox.AllowFallback, fileField[bool](file.Tools.Shell.Sandbox, "AllowFallback"), cfg.Tools.Shell.Sandbox.AllowFallback, def.Tools.Shell.Sandbox.AllowFallback)
	putKey(&sandbox.UnsafePassthrough, fileField[bool](file.Tools.Shell.Sandbox, "UnsafePassthrough"), cfg.Tools.Shell.Sandbox.UnsafePassthrough, def.Tools.Shell.Sandbox.UnsafePassthrough)
	putSlice(&sandbox.EnvAllowlist, fileSlice[string](file.Tools.Shell.Sandbox, "EnvAllowlist"), cfg.Tools.Shell.Sandbox.EnvAllowlist, def.Tools.Shell.Sandbox.EnvAllowlist)
	putSlice(&sandbox.EnvDenylist, fileSlice[string](file.Tools.Shell.Sandbox, "EnvDenylist"), cfg.Tools.Shell.Sandbox.EnvDenylist, def.Tools.Shell.Sandbox.EnvDenylist)
	shell.Allow = file.Tools.Shell.Allow
	shell.Confirm = file.Tools.Shell.Confirm
	shell.Deny = file.Tools.Shell.Deny
	shell.Sandbox = sandbox
	file.Tools.Shell = shell

	// Guard policy: profile/agent/privacy/shell/sandbox sections are written
	// per-key so a deleted key is not resurrected when the merged config
	// holds the default. Every other section is written only when the file
	// already has it or the value differs from Default(). Callers pass
	// merged user+project config, so primary sections can still bake
	// user-global values into the project file when those values differ
	// from default.

	if !reflect.DeepEqual(cfg.Project, def.Project) {
		file.Project = &fileProject{Name: strutil.Ptr(cfg.Project.Name), Languages: cfg.Project.Languages}
	}
	if cfg.Commands != def.Commands {
		file.Commands = &fileCommands{Test: strutil.Ptr(cfg.Commands.Test), Format: strutil.Ptr(cfg.Commands.Format), Vet: strutil.Ptr(cfg.Commands.Vet)}
	}
	if !reflect.DeepEqual(cfg.Indexing, def.Indexing) {
		file.Indexing = &fileIndexing{
			UseTreesitter:          strutil.Ptr(cfg.Indexing.UseTreesitter),
			UseEmbeddings:          strutil.Ptr(cfg.Indexing.UseEmbeddings),
			SummariseFiles:         strutil.Ptr(cfg.Indexing.SummariseFiles),
			Ignore:                 cfg.Indexing.Ignore,
			MaxIndexableFileBytes:  strutil.Ptr(cfg.Indexing.MaxIndexableFileBytes),
			MaxSearchableFileBytes: strutil.Ptr(cfg.Indexing.MaxSearchableFileBytes),
			Watch:                  cfg.Indexing.Watch,
			WatchDebounceMs:        strutil.Ptr(cfg.Indexing.WatchDebounceMs),
			EmbeddingPreset:        strutil.Ptr(cfg.Indexing.EmbeddingPreset),
		}
	}
	if !reflect.DeepEqual(cfg.Skills, def.Skills) {
		file.Skills = &fileSkills{
			Autoload:      cfg.Skills.Autoload,
			MaxActive:     strutil.Ptr(cfg.Skills.MaxActive),
			BodyFullTurns: strutil.Ptr(cfg.Skills.BodyFullTurns),
		}
	}
	if cfg.Web != def.Web {
		file.Web = &fileWeb{
			Enabled:        strutil.Ptr(cfg.Web.Enabled),
			FetchTimeout:   strutil.Ptr(cfg.Web.FetchTimeout.String()),
			SearchProvider: strutil.Ptr(cfg.Web.SearchProvider),
			SearchURL:      strutil.Ptr(cfg.Web.SearchURL),
			SearchKey:      strutil.Ptr(cfg.Web.SearchKey),
		}
	}
	if !reflect.DeepEqual(cfg.Desktop, def.Desktop) {
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
	if !reflect.DeepEqual(cfg.TUI, def.TUI) {
		file.TUI = &fileTUI{
			Theme:        strutil.Ptr(cfg.TUI.Theme),
			Depth:        strutil.Ptr(cfg.TUI.Depth),
			Palette:      cfg.TUI.Palette,
			Mode:         strutil.Ptr(cfg.TUI.Mode),
			MouseCapture: strutil.Ptr(cfg.TUI.MouseCapture),
			Suggestions:  strutil.Ptr(cfg.TUI.Suggestions),
		}
		if !reflect.DeepEqual(cfg.TUI.SidePanel, def.TUI.SidePanel) {
			file.TUI.SidePanel = &fileSidePanel{
				Enabled:  strutil.Ptr(cfg.TUI.SidePanel.Enabled),
				MinWidth: strutil.Ptr(cfg.TUI.SidePanel.MinWidth),
				WidthPct: strutil.Ptr(cfg.TUI.SidePanel.WidthPct),
				MinCols:  strutil.Ptr(cfg.TUI.SidePanel.MinCols),
				MaxCols:  strutil.Ptr(cfg.TUI.SidePanel.MaxCols),
				Hidden:   cfg.TUI.SidePanel.Hidden,
			}
		}
	}
	if !reflect.DeepEqual(cfg.Swarm, def.Swarm) {
		file.Swarm = &fileSwarm{Budget: &fileSwarmBudget{
			MaxFixRounds:   strutil.Ptr(cfg.Swarm.Budget.MaxFixRounds),
			MaxTotalTokens: strutil.Ptr(cfg.Swarm.Budget.MaxTotalTokens),
			ToolIters:      cfg.Swarm.Budget.ToolIters,
		}}
	}
	if !reflect.DeepEqual(cfg.SDD, def.SDD) {
		file.SDD = &fileSDD{
			AutoWorktree:    strutil.Ptr(cfg.SDD.AutoWorktree),
			MaxFixRounds:    strutil.Ptr(cfg.SDD.MaxFixRounds),
			DispatchRetries: strutil.Ptr(cfg.SDD.DispatchRetries),
			PlansDir:        strutil.Ptr(cfg.SDD.PlansDir),
			VerifyTimeoutMS: strutil.Ptr(cfg.SDD.VerifyTimeoutMS),
			CleanupAtStart:  strutil.Ptr(cfg.SDD.CleanupAtStart),
			MaxTotalTokens:  strutil.Ptr(cfg.SDD.MaxTotalTokens),
			Verify: &fileSDDVerify{
				Build: strutil.Ptr(cfg.SDD.Verify.Build),
				Test:  strutil.Ptr(cfg.SDD.Verify.Test),
			},
		}
	}
	if !reflect.DeepEqual(cfg.MCP, def.MCP) {
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
	if cfg.Snapshots != def.Snapshots {
		file.Snapshots = &fileSnapshots{
			Enabled:       strutil.Ptr(cfg.Snapshots.Enabled),
			RetentionDays: strutil.Ptr(cfg.Snapshots.RetentionDays),
			MaxFileBytes:  strutil.Ptr(cfg.Snapshots.MaxFileBytes),
		}
	}
	if len(cfg.Permissions.Rules) > 0 {
		file.Permissions = &filePermissions{Rules: cfg.Permissions.Rules}
	}
	if !reflect.DeepEqual(cfg.Diagnostics, def.Diagnostics) {
		file.Diagnostics = &fileDiagnostics{Commands: cfg.Diagnostics.Commands}
	}
	if !reflect.DeepEqual(cfg.Hooks, def.Hooks) {
		entries := make([]fileHookEntry, 0, len(cfg.Hooks.Entries))
		for _, h := range cfg.Hooks.Entries {
			entries = append(entries, fileHookEntry{Event: strutil.Ptr(h.Event), Matcher: strutil.Ptr(h.Matcher), Command: strutil.Ptr(h.Command), TimeoutMS: strutil.Ptr(h.TimeoutMS)})
		}
		file.Hooks = &fileHooks{FailClosed: strutil.Ptr(cfg.Hooks.FailClosed), Entries: entries}
	}
	if file.AgentProfilesRaw != nil || len(cfg.AgentProfiles) > 0 {
		file.AgentProfilesRaw = map[string]map[string]any{}
		for name, profile := range cfg.AgentProfiles {
			roles := profile.Roles
			if roles == nil {
				roles = map[routing.AgentRole]routing.RoleBinding{}
			}
			roleMap := map[string]any{}
			for role, binding := range roles {
				if binding.CustomAgent != "" {
					roleMap[string(role)] = map[string]any{"custom_agent": binding.CustomAgent}
				} else {
					roleMap[string(role)] = binding.Preset
				}
			}
			file.AgentProfilesRaw[name] = roleMap
		}
	}
	if file.CustomAgents != nil || len(cfg.CustomAgents) > 0 {
		file.CustomAgents = map[string]routing.CustomAgent{}
		for name, a := range cfg.CustomAgents {
			ca := a
			ca.Name = name
			file.CustomAgents[name] = ca
		}
	}
	// Sections below were previously merged on load but never written
	// back, so config.session.rollover.set / config.lsp.set /
	// config.history.set / config.scratchpad.set / config.agents.set
	// reported success and lost their values on restart. Same rule as the
	// sections above: write when the file already carries the section or
	// the merged value differs from Default().
	if file.Session != nil || !reflect.DeepEqual(cfg.Session, def.Session) {
		session := &fileSession{}
		if file.Session != nil && file.Session.Rollover != nil {
			session.Rollover = file.Session.Rollover
		} else {
			session.Rollover = &fileRollover{}
		}
		r := cfg.Session.Rollover
		dr := def.Session.Rollover
		putKey(&session.Rollover.Enabled, fileField[bool](session.Rollover, "Enabled"), r.Enabled, dr.Enabled)
		putKey(&session.Rollover.Policy, fileField[string](session.Rollover, "Policy"), r.Policy, dr.Policy)
		putKey(&session.Rollover.ContextPercentThreshold, fileField[int](session.Rollover, "ContextPercentThreshold"), r.ContextPercentThreshold, dr.ContextPercentThreshold)
		putKey(&session.Rollover.TurnCountThreshold, fileField[int](session.Rollover, "TurnCountThreshold"), r.TurnCountThreshold, dr.TurnCountThreshold)
		putKey(&session.Rollover.TokenCounter, fileField[string](session.Rollover, "TokenCounter"), r.TokenCounter, dr.TokenCounter)
		putKey(&session.Rollover.DigestModel, fileField[string](session.Rollover, "DigestModel"), r.DigestModel, dr.DigestModel)
		putKey(&session.Rollover.DigestProvider, fileField[string](session.Rollover, "DigestProvider"), r.DigestProvider, dr.DigestProvider)
		putKey(&session.Rollover.RecallToolEnabled, fileField[string](session.Rollover, "RecallToolEnabled"), r.RecallToolEnabled, dr.RecallToolEnabled)
		putKey(&session.Rollover.Retention, fileField[string](session.Rollover, "Retention"), r.Retention, dr.Retention)
		putKey(&session.Rollover.BlobThresholdBytes, fileField[int](session.Rollover, "BlobThresholdBytes"), r.BlobThresholdBytes, dr.BlobThresholdBytes)
		if session.Rollover.Calibration == nil {
			session.Rollover.Calibration = &fileCalibration{}
		}
		putKey(&session.Rollover.Calibration.Enabled, fileField[bool](session.Rollover.Calibration, "Enabled"), r.Calibration.Enabled, dr.Calibration.Enabled)
		file.Session = session
	}
	if file.LSP != nil || (cfg.LSP.Enabled != nil && !*cfg.LSP.Enabled) || len(cfg.LSP.Servers) > 0 {
		lsp := &fileLSP{}
		if file.LSP != nil {
			lsp.Enabled = file.LSP.Enabled
			if file.LSP.Servers != nil {
				lsp.Servers = map[string]fileLSPServer{}
				for name, srv := range file.LSP.Servers {
					lsp.Servers[name] = srv
				}
			}
		}
		// LSPConfig.Enabled is nil-means-true; only an explicit false
		// differs from the default and needs persisting.
		if cfg.LSP.Enabled != nil && !*cfg.LSP.Enabled {
			lsp.Enabled = strutil.Ptr(false)
		}
		if len(cfg.LSP.Servers) > 0 {
			if lsp.Servers == nil {
				lsp.Servers = map[string]fileLSPServer{}
			}
			for name, srv := range cfg.LSP.Servers {
				entry := fileLSPServer{Args: srv.Args}
				if srv.Command != "" {
					entry.Command = strutil.Ptr(srv.Command)
				}
				if srv.Disabled {
					entry.Disabled = strutil.Ptr(true)
				}
				lsp.Servers[name] = entry
			}
		}
		file.LSP = lsp
	}
	if file.History != nil || cfg.History.Enabled != def.History.Enabled {
		file.History = &fileHistory{Enabled: strutil.Ptr(cfg.History.Enabled)}
	}
	if file.Scratchpad != nil || !reflect.DeepEqual(cfg.Scratchpad, def.Scratchpad) {
		file.Scratchpad = &fileScratchpad{
			MaxEntries:          strutil.Ptr(cfg.Scratchpad.MaxEntries),
			MaxTotalTokens:      strutil.Ptr(cfg.Scratchpad.MaxTotalTokens),
			MaxEntryTokens:      strutil.Ptr(cfg.Scratchpad.MaxEntryTokens),
			ProjectionMaxTokens: strutil.Ptr(cfg.Scratchpad.ProjectionMaxTokens),
		}
	}
	if file.Agents != nil || len(cfg.Agents) > 0 {
		agents := map[routing.AgentRole]fileAgentEntry{}
		if file.Agents != nil {
			for role, entry := range file.Agents {
				agents[role] = entry
			}
		}
		for role, agentCfg := range cfg.Agents {
			agents[role] = fileAgentEntry{Context: agentCfg.Context}
		}
		file.Agents = agents
	}
}

// writeGlobalSections applies the user-global-only sections of cfg onto file
// in place: [providers] and [models.presets]. Only SaveUserConfigSection and
// the targeted SaveUserConfig* writers may persist these — project saves
// never emit them, so credentials cannot leak into a shared, often-committed
// project file.
//
// Provider entries are persisted including API keys. This is intentional:
// the TUI /settings flow needs to save provider configuration to disk so it
// survives restart. Keys are only persisted in the user-global config
// (~/.config/marshal/config.toml), which writeUserConfigFile writes with
// owner-only 0600 permissions. If a user does not want keys persisted, they
// should use api_key_env to reference an environment variable instead of
// embedding the key directly.
func writeGlobalSections(file *configFile, cfg Config) {
	if len(cfg.Providers) > 0 {
		file.Providers = cfg.Providers
	}
	if len(cfg.Models.Presets) > 0 {
		if file.Models == nil {
			file.Models = &fileModels{}
		}
		file.Models.Presets = normalizedPresets(cfg.Models.Presets)
	}
}

// normalizedPresets re-keys identity fields from each preset's map key: Name
// from the key, Provider/Model from its canonical "<provider>/<model>" form.
func normalizedPresets(presets map[string]routing.ModelPreset) map[string]routing.ModelPreset {
	out := make(map[string]routing.ModelPreset, len(presets))
	for name, p := range presets {
		preset := routing.ModelPreset{
			ContextWindow:   p.ContextWindow,
			MaxOutputTokens: p.MaxOutputTokens,
			ToolCalling:     p.ToolCalling,
			LocalOnly:       p.LocalOnly,
			Thinking:        p.Thinking,
			Temperature:     p.Temperature,
			Pricing:         p.Pricing,
		}
		preset.Name = name
		preset.Provider, preset.Model, _ = strings.Cut(name, "/")
		out[name] = preset
	}
	return out
}

// userConfigFileMode and userConfigDirMode are the permissions for the
// user-global config, which stores provider API keys inline
// (SaveUserConfigProviderAPIKey). Every writer of that file must go through
// writeUserConfigFile.
//
// These were 0644/0755, which left plaintext credentials readable by every
// other user on the machine. The project config is exempt because providers
// (and therefore keys) are never written to it — it is a shared,
// often-committed file by design.
const (
	userConfigFileMode = 0o600
	userConfigDirMode  = 0o700
)

// writeUserConfigFile writes the user-global config with owner-only
// permissions, tightening the file first if it already exists with broader
// ones.
//
// The re-tighten matters: os.WriteFile only applies its mode when creating a
// file, so a config written by an older build keeps its 0644 forever
// otherwise, and the leak silently survives the fix.
func writeUserConfigFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), userConfigDirMode); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			if cerr := os.Chmod(path, userConfigFileMode); cerr != nil {
				return fmt.Errorf("tighten config permissions: %w", cerr)
			}
		}
	}
	return os.WriteFile(path, data, userConfigFileMode)
}

// SaveUserConfigProviderAPIKey persists the given ProviderConfig for
// providerName in the user-global config. Callers pass either a full config
// carrying the provider's metadata (type, base_url, template, capabilities)
// or a credential-only value when they know an entry already exists.
//
// Issue #9: persisting only the key for a brand-new provider used to create
// a zero-value skeleton ([providers.x] with type = "" and base_url = "")
// that only worked in the project whose .marshal/config.toml supplied the
// real metadata. A global credential must stay usable from any repository,
// so callers that know the full provider config pass it here; the save then
// behaves as a credential overlay:
//
//   - when the incoming value is "full" (Type set), every field is taken
//     exactly as provided — including turning capabilities such as
//     tool_calling off. This matches re-connect through the connect flow,
//     which owns the authoritative provider definition;
//   - otherwise (credential-only update) non-credential fields are
//     preserved from the existing entry and only credentials are written;
//   - credentials always come from the incoming value (api_key is stored,
//     api_key_env cleared so a stale env reference cannot shadow the key).
func SaveUserConfigProviderAPIKey(path, providerName string, pc ProviderConfig) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	if file.Providers == nil {
		file.Providers = make(map[string]ProviderConfig, 1)
	}

	in := pc
	existing := file.Providers[providerName]
	merged := existing
	if in.Type != "" {
		// Full provider definition: take every field exactly as provided so
		// the caller can also downgrade capabilities (e.g. a probe on
		// re-connect found no tool support).
		merged.Type = in.Type
		merged.BaseURL = in.BaseURL
		merged.Template = in.Template
		merged.ToolCalling = in.ToolCalling
		merged.KeepAlive = in.KeepAlive
		merged.ThinkingBudget = in.ThinkingBudget
		merged.ReasoningSummary = in.ReasoningSummary
	} else {
		// Credential-only update of an existing entry: preserve its
		// non-credential metadata.
		if in.BaseURL != "" {
			merged.BaseURL = in.BaseURL
		}
		if in.ToolCalling {
			merged.ToolCalling = true
		}
		if in.ThinkingBudget != 0 {
			merged.ThinkingBudget = in.ThinkingBudget
		}
		if in.ReasoningSummary {
			merged.ReasoningSummary = true
		}
	}
	merged.APIKey = in.APIKey
	merged.APIKeyEnv = "" // clear stale env-var reference when switching to inline
	file.Providers[providerName] = merged

	data, err := toml.Marshal(&file)
	if err != nil {
		return fmt.Errorf("marshal user config: %w", err)
	}
	// Prepend a minimal header if the file is being created for the first time.
	// loadFile returns an empty configFile when the file does not exist, so we
	// check whether the file exists on disk.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		header := "# Marshal global configuration\n"
		data = append([]byte(header), data...)
	}
	return writeUserConfigFile(path, data)
}

// SaveUserConfigProviders replaces the [providers] section of the user-global
// config at path with the given entries, credentials included. An empty or
// nil map removes the section from the file.
func SaveUserConfigProviders(path string, providers map[string]ProviderConfig) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	for name, p := range providers {
		if err := validateProviderBaseURL(p.BaseURL); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
	}
	if len(providers) == 0 {
		file.Providers = nil
	} else {
		file.Providers = providers
	}
	return writeUserConfig(path, &file)
}

// SaveUserConfigPresets replaces the [models.presets] section of the
// user-global config at path with the given presets, applying the save-path
// normalization (identity fields derived from each map key). An empty or nil
// map removes the presets — and the [models] section, which holds nothing
// else — from the file.
func SaveUserConfigPresets(path string, presets map[string]routing.ModelPreset) error {
	file, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	if len(presets) == 0 {
		file.Models = nil
	} else {
		file.Models = &fileModels{Presets: normalizedPresets(presets)}
	}
	return writeUserConfig(path, &file)
}

// writeUserConfig marshals file and writes it to path via writeUserConfigFile,
// prepending the standard header when the file is being created.
func writeUserConfig(path string, file *configFile) error {
	data, err := toml.Marshal(file)
	if err != nil {
		return fmt.Errorf("marshal user config: %w", err)
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		header := "# Marshal global configuration\n"
		data = append([]byte(header), data...)
	}
	return writeUserConfigFile(path, data)
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
	return writeUserConfigFile(path, data)
}
