package config

import (
	"time"

	"marshal/internal/llm/routing"
)

func Default() Config {
	cfg := Config{
		Project: ProjectConfig{
			Name:      "marshal",
			Languages: []string{"go", "markdown"},
		},
		Commands: CommandsConfig{
			Test:   "go test ./...",
			Format: "gofmt -w .",
			Vet:    "go vet ./...",
		},
		Profile: ProfileConfig{
			Default: "local_balanced",
		},
		Agent: AgentConfig{
			ApprovalMode:           "default",
			MaxTouchedFileBytes:    65536, // 64 KiB
			MaxConcurrentSubagents: 3,
			// Preserve the long-standing fixed +4096 Anthropic thinking
			// headroom as the default margin. 0 would switch to the auto
			// formula, changing behavior for existing setups.
			ThinkingBudgetMargin: 4096,
		},
		Privacy: PrivacyConfig{
			RemoteProvidersAllowed: false,
			RemoteLimitDiscovery:   false,
			RedactSecrets:          true,
			IncludeGitignoredFiles: false,
		},
		Skills: SkillsConfig{
			MaxActive: 8,
		},
		Indexing: IndexingConfig{
			UseTreesitter:          false,
			UseEmbeddings:          false,
			SummariseFiles:         false,
			Ignore:                 []string{"node_modules/**", "vendor/**", "dist/**", ".git/**"},
			MaxIndexableFileBytes:  25 * 1024 * 1024, // 25 MiB
			MaxSearchableFileBytes: 1 * 1024 * 1024,  // 1 MiB
		},
		Models: ModelsConfig{
			Presets: map[string]routing.ModelPreset{},
		},
		AgentProfiles: map[string]routing.AgentProfile{},
		CustomAgents:  map[string]routing.CustomAgent{},
		Agents:        map[routing.AgentRole]AgentRoleConfig{},
		Tools: ToolsConfig{
			Shell: ShellToolConfig{
				DefaultTimeoutSeconds: 120,
				MaxOutputBytes:        200000,
				MaxBackgroundJobs:     25,
				BackgroundRetention:   8 * time.Hour,
				AllowNetwork:          false,
				AutoApprove:           false,
				GuardrailDynamicArgv0: "deny",
				Allow:                 CommandRules{Commands: []string{"go test", "git status", "git diff"}},
				Confirm:               CommandRules{Commands: []string{"go get", "npm install"}},
				Deny:                  PatternRules{Patterns: []string{"rm -rf", "sudo", "curl * | sh"}},
				Sandbox: SandboxConfig{
					Backend: "restricted",
					// Memory, CPU, and file-size caps stay opt-in: a build or
					// test suite can legitimately be large or slow, and a cap
					// that kills real work trains users to disable the sandbox
					// outright.
					MemoryLimitMB:   0,
					CPUSeconds:      0,
					FileSizeLimitMB: 0,
					// A process cap is the exception, and is on by default.
					// With every limit unset, restrictedWrapCommand emitted no
					// ulimit at all, leaving a fork bomb entirely unguarded —
					// the guardrails do not classify one, and `restricted`
					// provides no filesystem or process isolation.
					//
					// 2048 is deliberately generous. `ulimit -u` is per-UID on
					// unix, not per-process-tree, so it is shared with every
					// other process the user is already running; a tight cap
					// would fail in ways that look nothing like their cause. A
					// fork bomb spawns without bound and hits this immediately,
					// while no ordinary build or parallel test run approaches
					// it.
					MaxProcesses:     2048,
					ContainerRuntime: "auto",
					// ContainerImage intentionally empty here: the single
					// source of truth for the default image lives in
					// internal/sandbox/container.go (defaultContainerImage).
					// If this field is empty at runtime, the container
					// backend substitutes defaultContainerImage.
					ContainerImage:    "",
					AllowFallback:     false,
					UnsafePassthrough: false,
					EnvAllowlist: []string{
						"PATH", "HOME", "USER", "SHELL", "LANG", "LC_ALL",
						"TERM", "TMPDIR", "GOPATH", "GOCACHE", "GOMODCACHE",
					},
					EnvDenylist: nil,
				},
			},
		},
		Swarm: SwarmConfig{
			Budget: SwarmBudgetConfig{
				MaxFixRounds:   3,
				MaxTotalTokens: 500000,
				ToolIters:      map[string]int{},
			},
		},
		SDD: SDDConfig{
			AutoWorktree:    true,
			MaxFixRounds:    3,
			DispatchRetries: 2,
			PlansDir:        ".marshal/plans",
			VerifyTimeoutMS: 300000,
			CleanupAtStart:  true,
			MaxTotalTokens:  0,
		},
		Web: WebConfig{
			Enabled:      false,
			FetchTimeout: 30 * time.Second,
		},
		Desktop: DesktopConfig{
			Enabled:          false,
			Mode:             "standalone",
			Headless:         false,
			DefaultTimeout:   30 * time.Second,
			ScreenshotFormat: "png",
		},
		MCP: MCPConfig{
			Servers:                  map[string]MCPServerConfig{},
			Policies:                 map[string]string{},
			DisclosureThresholdTools: 40,
		},
		Diagnostics: DiagnosticsConfig{
			Commands: map[string]string{"go": "go vet {package}"},
		},
		Snapshots: SnapshotsConfig{
			Enabled:       true,
			RetentionDays: 7,
			MaxFileBytes:  2_000_000,
		},
		Permissions: PermissionsConfig{
			Rules: nil,
		},
		Session: SessionConfig{
			Rollover: RolloverConfig{
				Enabled:                 false,
				Policy:                  "context_percent",
				ContextPercentThreshold: 70,
				TurnCountThreshold:      40,
				TokenCounter:            "auto",
				DigestProvider:          "llm_summary",
				RecallToolEnabled:       "auto",
				Retention:               "forever",
				BlobThresholdBytes:      2048,
				Calibration:             CalibrationConfig{Enabled: false},
			},
		},
		History: HistoryConfig{
			Enabled: true,
		},
		TUI: TUIConfig{
			MouseCapture: true,
			SidePanel: SidePanelConfig{
				Enabled:  true,
				MinWidth: 120,
				WidthPct: 25,
				MinCols:  30,
				MaxCols:  60,
			},
			Suggestions: "rules",
		},
		Hooks: HooksConfig{
			FailClosed: false,
			Entries:    nil,
		},
		// Providers is intentionally left nil: Marshal is local-friendly with no
		// built-in provider assumptions, and provider URLs/keys are
		// user-specific (see docs/09-configuration-examples.md).
	}
	cfg.Privacy.RemoteLimitDiscovery = cfg.Privacy.RemoteProvidersAllowed
	cfg.Scratchpad.ApplyDefaults()
	return cfg
}
