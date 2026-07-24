package config

import (
	"time"

	"marshal/internal/llm/routing"
)

func Default() Config {
	return Config{
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
			ApprovalMode: "default",
		},
		Privacy: PrivacyConfig{
			RemoteProvidersAllowed: false,
			RedactSecrets:          true,
			IncludeGitignoredFiles: false,
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
					Backend:          "restricted",
					MemoryLimitMB:    0,
					CPUSeconds:       0,
					MaxProcesses:     0,
					FileSizeLimitMB:  0,
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
				MaxTotalTokens: 120000,
				ToolIters:      map[string]int{},
			},
		},
		SDD: SDDConfig{
			AutoWorktree: true,
			MaxFixRounds: 3,
			PlansDir:     ".marshal/plans",
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
		Hooks: HooksConfig{
			FailClosed: false,
			Entries:    nil,
		},
		// Providers is intentionally left nil: Marshal is local-first with no
		// built-in provider assumptions, and provider URLs/keys are
		// user-specific (see docs/09-configuration-examples.md).
	}
}
