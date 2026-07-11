package config

import (
	"fmt"
	"marshal/internal/llm/routing"
	"marshal/internal/trust"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Project       ProjectConfig                         `toml:"project"`
	Commands      CommandsConfig                        `toml:"commands"`
	Profile       ProfileConfig                         `toml:"profile"`
	Agent         AgentConfig                           `toml:"agent"`
	Privacy       PrivacyConfig                         `toml:"privacy"`
	Indexing      IndexingConfig                        `toml:"indexing"`
	Providers     map[string]ProviderConfig             `toml:"providers"`
	Models        ModelsConfig                          `toml:"models"`
	AgentProfiles map[string]routing.AgentProfile       `toml:"agent_profiles"`
	Agents        map[routing.AgentRole]AgentRoleConfig `toml:"agents"`
	Tools         ToolsConfig                           `toml:"tools"`
	Web           WebConfig                             `toml:"web"`
	Swarm         SwarmConfig                           `toml:"swarm"`
	MCP           MCPConfig                             `toml:"mcp"`
	Snapshots     SnapshotsConfig                       `toml:"snapshots"`
	Permissions   PermissionsConfig                     `toml:"permissions"`
	Diagnostics   DiagnosticsConfig                     `toml:"diagnostics"`
	Hooks         HooksConfig                           `toml:"hooks"`
}

type ModelsConfig struct {
	Presets map[string]routing.ModelPreset `toml:"presets"`
}

type AgentRoleConfig struct {
	Context routing.ContextBudget `toml:"context"`
}

type modelPresetConfig struct {
	Provider        string  `toml:"provider"`
	Model           string  `toml:"model"`
	ContextWindow   int     `toml:"context_window"`
	MaxOutputTokens int     `toml:"max_output_tokens"`
	Temperature     float64 `toml:"temperature"`
	TopP            float64 `toml:"top_p"`
	ToolCalling     string  `toml:"tool_calling"`
	ReasoningEffort string  `toml:"reasoning_effort"`
	LocalOnly       bool    `toml:"local_only"`
}

type contextBudgetConfig struct {
	MaxRepoContextTokens  int  `toml:"max_repo_context_tokens"`
	MaxConversationTokens int  `toml:"max_conversation_tokens"`
	IncludeRawCode        bool `toml:"include_raw_code"`
	IncludeSummaries      bool `toml:"include_summaries"`
	IncludeSymbols        bool `toml:"include_symbols"`
	IncludeDiff           bool `toml:"include_diff"`
	IncludeTests          bool `toml:"include_tests"`
}

type ToolsConfig struct {
	Shell ShellToolConfig `toml:"shell"`
}

// SwarmConfig holds run-level swarm settings.
type SwarmConfig struct {
	Budget SwarmBudgetConfig `toml:"budget"`
}

type SwarmBudgetConfig struct {
	MaxFixRounds   int            `toml:"max_fix_rounds"`
	MaxTotalTokens int            `toml:"max_total_tokens"`
	ToolIters      map[string]int `toml:"tool_iters"`
}

type MCPConfig struct {
	Servers                  map[string]MCPServerConfig `toml:"servers"`
	Policies                 map[string]string          `toml:"policies"`
	DisclosureThresholdTools int                        `toml:"disclosure_threshold_tools"`
}

type MCPServerConfig struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type SnapshotsConfig struct {
	Enabled       bool `toml:"enabled"`
	RetentionDays int  `toml:"retention_days"`
	MaxFileBytes  int  `toml:"max_file_bytes"`
}

type PermissionsConfig struct {
	Rules []PermissionRule `toml:"rules"`
}

type PermissionRule struct {
	Permission string `toml:"permission"`
	Pattern    string `toml:"pattern"`
	Action     string `toml:"action"`
}

type DiagnosticsConfig struct {
	Commands map[string]string `toml:"commands"`
}

// HooksConfig holds the lifecycle-hook subsystem settings: the global
// fail-closed policy and the per-event hook entries. Hooks are local
// process execution only (no network, no remote) and are gated on project
// trust when the project-local config defines them.
type HooksConfig struct {
	FailClosed bool         `toml:"fail_closed"`
	Entries    []HookConfig `toml:"entries"`
}

// HookConfig is one lifecycle hook entry. Event names are validated by the
// hook runtime (see internal/hooks), not here, so the config layer only
// parses shape.
type HookConfig struct {
	Event     string `toml:"event"`
	Matcher   string `toml:"matcher"`
	Command   string `toml:"command"`
	TimeoutMS int    `toml:"timeout_ms"`
}

// WebConfig gates outbound network access from agent-side tools (web.fetch
// and web.search). It is disabled by default — Marshal is local-first and
// opt-in for anytying that talks to the public internet. Search is further
// conditional on SearchURL being set; web.fetch is available whenever
// Enabled is true.
type WebConfig struct {
	Enabled        bool          `toml:"enabled"`
	FetchTimeout   time.Duration `toml:"fetch_timeout"`
	SearchProvider string        `toml:"search_provider"`
	SearchURL      string        `toml:"search_url"`
	SearchKey      string        `toml:"search_key"`
}

type ShellToolConfig struct {
	DefaultTimeoutSeconds int           `toml:"default_timeout_seconds"`
	MaxOutputBytes        int           `toml:"max_output_bytes"`
	MaxBackgroundJobs     int           `toml:"max_background_jobs"`
	BackgroundRetention   time.Duration `toml:"background_retention"`
	AllowNetwork          bool          `toml:"allow_network"`
	AllowSudo             bool          `toml:"allow_sudo"`
	AllowDestructive      bool          `toml:"allow_destructive"`
	AutoApprove           bool          `toml:"auto_approve"`
	Allow                 CommandRules  `toml:"allow"`
	Confirm               CommandRules  `toml:"confirm"`
	Deny                  PatternRules  `toml:"deny"`
	GuardrailDynamicArgv0 string        `toml:"guardrail_dynamic_argv0"`
	Sandbox               SandboxConfig `toml:"sandbox"`
}

// SandboxConfig selects and tunes the Milestone Q execution backend for
// shell.run / test.run. The default ("restricted") enables in-process
// hardening (env allowlist scrubbing, resource limits via ulimit/rlimit,
// cwd confinement, process-group kill on timeout). "container" requires a
// Docker/Podman runtime and enforces network isolation, filesystem
// isolation, and hard memory/cpu limits; it falls back to "restricted"
// ONLY when AllowFallback=true AND no runtime is detected — otherwise
// startup fails with a clear error so isolation is not silently waived.
// "passthrough" disables all sandboxing (the pre-Milestone-Q behavior).
type SandboxConfig struct {
	Backend          string `toml:"backend"`
	MemoryLimitMB    int    `toml:"memory_limit_mb"`
	CPUSeconds       int    `toml:"cpu_seconds"`
	MaxProcesses     int    `toml:"max_processes"`
	FileSizeLimitMB  int    `toml:"file_size_limit_mb"`
	ContainerRuntime string `toml:"container_runtime"`
	ContainerImage   string `toml:"container_image"`
	// AllowFallback controls whether a configured container backend that
	// cannot be reached (no docker/podman on the host) may silently
	// downgrade to the restricted backend. Default = false: failing to
	// run the requested sandbox is a hard error, because a downgrade
	// silently drops network/filesystem containment the user expected.
	AllowFallback bool     `toml:"allow_fallback"`
	EnvAllowlist  []string `toml:"env_allowlist"`
	EnvDenylist   []string `toml:"env_denylist"`
}

type CommandRules struct {
	Commands []string `toml:"commands"`
}

type PatternRules struct {
	Patterns []string `toml:"patterns"`
}

// sandboxFile is the TOML-mirror nullable form of SandboxConfig, used both
// by configFile (load path) and by SaveProjectConfig (save path) so all
// mirror copies share a single definition — adding/renaming a SandboxConfig
// field requires editing just this struct plus SandboxConfig itself, plus
// the merge block.
type sandboxFile struct {
	Backend          *string  `toml:"backend"`
	MemoryLimitMB    *int     `toml:"memory_limit_mb"`
	CPUSeconds       *int     `toml:"cpu_seconds"`
	MaxProcesses     *int     `toml:"max_processes"`
	FileSizeLimitMB  *int     `toml:"file_size_limit_mb"`
	ContainerRuntime *string  `toml:"container_runtime"`
	ContainerImage   *string  `toml:"container_image"`
	AllowFallback    *bool    `toml:"allow_fallback"`
	EnvAllowlist     []string `toml:"env_allowlist"`
	EnvDenylist      []string `toml:"env_denylist"`
}

type ProjectConfig struct {
	Name      string   `toml:"name"`
	Languages []string `toml:"languages"`
}

type CommandsConfig struct {
	Test   string `toml:"test"`
	Format string `toml:"format"`
	Vet    string `toml:"vet"`
}

type ProfileConfig struct {
	Default string `toml:"default"`
}

// AgentConfig names which configured provider and model the agent loop
// (Milestone H) uses. Both fields are intentionally blank in Default():
// Marshal is local-first with no built-in provider assumptions (see
// Providers' Default() comment) — the app runs with the agent loop disabled
// until a user configures both a [providers.<name>] entry and this section.
type AgentConfig struct {
	Provider                 string `toml:"provider"`
	Model                    string `toml:"model"`
	MaxToolIterations        int    `toml:"max_tool_iterations"`
	MaxRetries               int    `toml:"max_retries"`
	MaxTurnContextTokens     int    `toml:"max_turn_context_tokens"`
	MaxStructuredOutputChars int    `toml:"max_structured_output_chars"`
	PlanFirst                bool   `toml:"plan_first"`
	// SubtaskIterations caps tool iterations for an ad-hoc agent.run child.
	// The child gets a fresh Runner with this cap (defaults to 12 when zero).
	// A subtask that exhausts its budget is salvaged: the parent receives
	// whatever partial answer the child produced instead of a hard error.
	SubtaskIterations int `toml:"subtask_iterations"`
}

type PrivacyConfig struct {
	RemoteProvidersAllowed bool `toml:"remote_providers_allowed"`
	RedactSecrets          bool `toml:"redact_secrets"`
	IncludeGitignoredFiles bool `toml:"include_gitignored_files"`
}

type IndexingConfig struct {
	UseTreesitter  bool     `toml:"use_treesitter"`
	UseEmbeddings  bool     `toml:"use_embeddings"`
	SummariseFiles bool     `toml:"summarise_files"`
	Ignore         []string `toml:"ignore"`
}

// ProviderConfig is one [providers.<name>] entry. Only the fields needed
// for the generic OpenAI-compatible provider are present.
type ProviderConfig struct {
	Type        string `toml:"type"` // "openai_compatible" is the only supported value in this milestone
	BaseURL     string `toml:"base_url"`
	APIKey      string `toml:"api_key"`      // literal key; wins over APIKeyEnv if both set
	APIKeyEnv   string `toml:"api_key_env"`  // env var name to resolve at provider-construction time (NOT resolved here)
	ToolCalling bool   `toml:"tool_calling"` // provider advertises native OpenAI-compatible tool-calling support
}

type LoadOptions struct {
	HomeDir       string
	WorkingDir    string
	TrustResolver trust.Resolver
	Trusted       *bool
}

type fileProject struct {
	Name      *string  `toml:"name"`
	Languages []string `toml:"languages"`
}

type fileCommands struct {
	Test   *string `toml:"test"`
	Format *string `toml:"format"`
	Vet    *string `toml:"vet"`
}

type fileProfile struct {
	Default *string `toml:"default"`
}

type fileAgent struct {
	Provider                 *string `toml:"provider"`
	Model                    *string `toml:"model"`
	MaxToolIterations        *int    `toml:"max_tool_iterations"`
	MaxRetries               *int    `toml:"max_retries"`
	MaxTurnContextTokens     *int    `toml:"max_turn_context_tokens"`
	MaxStructuredOutputChars *int    `toml:"max_structured_output_chars"`
	PlanFirst                *bool   `toml:"plan_first"`
	SubtaskIterations        *int    `toml:"subtask_iterations"`
}

type filePrivacy struct {
	RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
	RedactSecrets          *bool `toml:"redact_secrets"`
	IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
}

type fileIndexing struct {
	UseTreesitter  *bool    `toml:"use_treesitter"`
	UseEmbeddings  *bool    `toml:"use_embeddings"`
	SummariseFiles *bool    `toml:"summarise_files"`
	Ignore         []string `toml:"ignore"`
}

type fileShell struct {
	DefaultTimeoutSeconds *int          `toml:"default_timeout_seconds"`
	MaxOutputBytes        *int          `toml:"max_output_bytes"`
	MaxBackgroundJobs     *int          `toml:"max_background_jobs"`
	BackgroundRetention   *string       `toml:"background_retention"`
	AllowNetwork          *bool         `toml:"allow_network"`
	AllowSudo             *bool         `toml:"allow_sudo"`
	AllowDestructive      *bool         `toml:"allow_destructive"`
	AutoApprove           *bool         `toml:"auto_approve"`
	GuardrailDynamicArgv0 *string       `toml:"guardrail_dynamic_argv0"`
	Allow                 *CommandRules `toml:"allow"`
	Confirm               *CommandRules `toml:"confirm"`
	Deny                  *PatternRules `toml:"deny"`
	Sandbox               *sandboxFile  `toml:"sandbox"`
}

type fileTools struct {
	Shell *fileShell `toml:"shell"`
}

type fileWeb struct {
	Enabled        *bool   `toml:"enabled"`
	FetchTimeout   *string `toml:"fetch_timeout"`
	SearchProvider *string `toml:"search_provider"`
	SearchURL      *string `toml:"search_url"`
	SearchKey      *string `toml:"search_key"`
}

type fileSwarmBudget struct {
	MaxFixRounds   *int           `toml:"max_fix_rounds"`
	MaxTotalTokens *int           `toml:"max_total_tokens"`
	ToolIters      map[string]int `toml:"tool_iters"`
}

type fileSwarm struct {
	Budget *fileSwarmBudget `toml:"budget"`
}

type fileMCPServer struct {
	Command *string           `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type fileMCP struct {
	Servers                  map[string]fileMCPServer `toml:"servers"`
	Policies                 map[string]string        `toml:"policies"`
	DisclosureThresholdTools *int                     `toml:"disclosure_threshold_tools"`
}

type fileSnapshots struct {
	Enabled       *bool `toml:"enabled"`
	RetentionDays *int  `toml:"retention_days"`
	MaxFileBytes  *int  `toml:"max_file_bytes"`
}

type filePermissions struct {
	Rules []PermissionRule `toml:"rules"`
}

type fileDiagnostics struct {
	Commands map[string]string `toml:"commands"`
}

type fileHookEntry struct {
	Event     *string `toml:"event"`
	Matcher   *string `toml:"matcher"`
	Command   *string `toml:"command"`
	TimeoutMS *int    `toml:"timeout_ms"`
}

type fileHooks struct {
	FailClosed *bool           `toml:"fail_closed"`
	Entries    []fileHookEntry `toml:"entries"`
}

type fileModels struct {
	Presets map[string]modelPresetConfig `toml:"presets"`
}

type fileAgentEntry struct {
	Context contextBudgetConfig `toml:"context"`
}

type configFile struct {
	Project     *fileProject     `toml:"project"`
	Commands    *fileCommands    `toml:"commands"`
	Profile     *fileProfile     `toml:"profile"`
	Agent       *fileAgent       `toml:"agent"`
	Privacy     *filePrivacy     `toml:"privacy"`
	Indexing    *fileIndexing    `toml:"indexing"`
	Tools       *fileTools       `toml:"tools"`
	Web         *fileWeb         `toml:"web"`
	Swarm       *fileSwarm       `toml:"swarm"`
	MCP         *fileMCP         `toml:"mcp"`
	Snapshots   *fileSnapshots   `toml:"snapshots"`
	Permissions *filePermissions `toml:"permissions"`
	Diagnostics *fileDiagnostics `toml:"diagnostics"`
	Hooks       *fileHooks       `toml:"hooks"`
	// Providers stays a plain map: nil already distinguishes absent/present.
	Providers     map[string]ProviderConfig            `toml:"providers"`
	Models        *fileModels                          `toml:"models"`
	AgentProfiles map[string]agentProfileConfig        `toml:"agent_profiles"`
	Agents        map[routing.AgentRole]fileAgentEntry `toml:"agents"`
}

type agentProfileConfig struct {
	Router           string `toml:"router"`
	Knowledge        string `toml:"knowledge"`
	Summarizer       string `toml:"summarizer"`
	RepoScout        string `toml:"repo_scout"`
	Tester           string `toml:"tester"`
	Planner          string `toml:"planner"`
	Implementer      string `toml:"implementer"`
	Reviewer         string `toml:"reviewer"`
	SecurityReviewer string `toml:"security_reviewer"`
}

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
		Privacy: PrivacyConfig{
			RemoteProvidersAllowed: false,
			RedactSecrets:          true,
			IncludeGitignoredFiles: false,
		},
		Indexing: IndexingConfig{
			UseTreesitter:  false,
			UseEmbeddings:  false,
			SummariseFiles: false,
			Ignore:         []string{"node_modules/**", "vendor/**", "dist/**", ".git/**"},
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
				AllowSudo:             false,
				AllowDestructive:      false,
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
					ContainerImage: "",
					AllowFallback:  false,
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
		Web: WebConfig{
			Enabled:      false,
			FetchTimeout: 30 * time.Second,
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
		Hooks: HooksConfig{
			FailClosed: false,
			Entries:    nil,
		},
		// Providers is intentionally left nil: Marshal is local-first with no
		// built-in provider assumptions, and provider URLs/keys are
		// user-specific (see docs/09-configuration-examples.md).
	}
}

func Load(opts LoadOptions) (Config, error) {
	cfg := Default()

	home := opts.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("find home directory: %w", err)
		}
	}

	work := opts.WorkingDir
	if work == "" {
		var err error
		work, err = os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("find working directory: %w", err)
		}
	}

	userPath := filepath.Join(home, ".config", "marshal", "config.toml")
	userFile, err := loadFile(userPath)
	if err != nil {
		return Config{}, err
	}
	merge(&cfg, userFile)

	projectPath := filepath.Join(work, ".marshal", "config.toml")
	hasProject := trust.HasProjectConfig(work)
	applyProject := true
	if hasProject && opts.TrustResolver != nil {
		decision, derr := opts.TrustResolver.Resolve(work, true)
		if derr != nil {
			return Config{}, fmt.Errorf("resolve project trust: %w", derr)
		}
		if decision == trust.DecisionDontTrust {
			applyProject = false
		} else if decision == trust.DecisionTrustPermanent {
			_ = opts.TrustResolver.Record(work, decision)
		}
		if opts.Trusted != nil {
			*opts.Trusted = decision == trust.DecisionTrustPermanent || decision == trust.DecisionTrustSession
		}
	}
	if applyProject {
		projectFile, err := loadFile(projectPath)
		if err != nil {
			return Config{}, err
		}
		if err := merge(&cfg, projectFile); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func loadFile(path string) (configFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configFile{}, nil
		}
		return configFile{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var file configFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return configFile{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return file, nil
}

func profileFromConfig(name string, in agentProfileConfig) routing.AgentProfile {
	roles := map[routing.AgentRole]string{}
	if in.Router != "" {
		roles[routing.RoleRouter] = in.Router
	}
	if in.Knowledge != "" {
		roles[routing.RoleKnowledge] = in.Knowledge
	}
	if in.Summarizer != "" {
		roles[routing.RoleSummarizer] = in.Summarizer
	}
	if in.RepoScout != "" {
		roles[routing.RoleRepoScout] = in.RepoScout
	}
	if in.Tester != "" {
		roles[routing.RoleTester] = in.Tester
	}
	if in.Planner != "" {
		roles[routing.RolePlanner] = in.Planner
	}
	if in.Implementer != "" {
		roles[routing.RoleImplementer] = in.Implementer
	}
	if in.Reviewer != "" {
		roles[routing.RoleReviewer] = in.Reviewer
	}
	if in.SecurityReviewer != "" {
		roles[routing.RoleSecurityReviewer] = in.SecurityReviewer
	}
	return routing.AgentProfile{Name: name, Roles: roles}
}

func presetFromConfig(name string, in modelPresetConfig) routing.ModelPreset {
	return routing.ModelPreset{
		Name:            name,
		Provider:        in.Provider,
		Model:           in.Model,
		ContextWindow:   in.ContextWindow,
		MaxOutputTokens: in.MaxOutputTokens,
		Temperature:     in.Temperature,
		TopP:            in.TopP,
		ToolCalling:     in.ToolCalling,
		ReasoningEffort: in.ReasoningEffort,
		LocalOnly:       in.LocalOnly,
	}
}

func contextBudgetFromConfig(in contextBudgetConfig) routing.ContextBudget {
	return routing.ContextBudget{
		MaxRepoContextTokens:  in.MaxRepoContextTokens,
		MaxConversationTokens: in.MaxConversationTokens,
		IncludeRawCode:        in.IncludeRawCode,
		IncludeSummaries:      in.IncludeSummaries,
		IncludeSymbols:        in.IncludeSymbols,
		IncludeDiff:           in.IncludeDiff,
		IncludeTests:          in.IncludeTests,
	}
}

func merge(cfg *Config, file configFile) error {
	if file.Project != nil {
		if file.Project.Name != nil {
			cfg.Project.Name = *file.Project.Name
		}
		if file.Project.Languages != nil {
			cfg.Project.Languages = file.Project.Languages
		}
	}
	if file.Commands != nil {
		if file.Commands.Test != nil {
			cfg.Commands.Test = *file.Commands.Test
		}
		if file.Commands.Format != nil {
			cfg.Commands.Format = *file.Commands.Format
		}
		if file.Commands.Vet != nil {
			cfg.Commands.Vet = *file.Commands.Vet
		}
	}
	if file.Profile != nil && file.Profile.Default != nil {
		cfg.Profile.Default = *file.Profile.Default
	}
	if file.Agent != nil {
		if file.Agent.Provider != nil {
			cfg.Agent.Provider = *file.Agent.Provider
		}
		if file.Agent.Model != nil {
			cfg.Agent.Model = *file.Agent.Model
		}
		if file.Agent.MaxToolIterations != nil {
			cfg.Agent.MaxToolIterations = *file.Agent.MaxToolIterations
		}
		if file.Agent.MaxRetries != nil {
			cfg.Agent.MaxRetries = *file.Agent.MaxRetries
		}
		if file.Agent.MaxTurnContextTokens != nil {
			cfg.Agent.MaxTurnContextTokens = *file.Agent.MaxTurnContextTokens
		}
		if file.Agent.MaxStructuredOutputChars != nil {
			cfg.Agent.MaxStructuredOutputChars = *file.Agent.MaxStructuredOutputChars
		}
		if file.Agent.PlanFirst != nil {
			cfg.Agent.PlanFirst = *file.Agent.PlanFirst
		}
		if file.Agent.SubtaskIterations != nil {
			cfg.Agent.SubtaskIterations = *file.Agent.SubtaskIterations
		}
	}
	if file.Privacy != nil {
		if file.Privacy.RemoteProvidersAllowed != nil {
			cfg.Privacy.RemoteProvidersAllowed = *file.Privacy.RemoteProvidersAllowed
		}
		if file.Privacy.RedactSecrets != nil {
			cfg.Privacy.RedactSecrets = *file.Privacy.RedactSecrets
		}
		if file.Privacy.IncludeGitignoredFiles != nil {
			cfg.Privacy.IncludeGitignoredFiles = *file.Privacy.IncludeGitignoredFiles
		}
	}
	if file.Indexing != nil {
		if file.Indexing.UseTreesitter != nil {
			cfg.Indexing.UseTreesitter = *file.Indexing.UseTreesitter
		}
		if file.Indexing.UseEmbeddings != nil {
			cfg.Indexing.UseEmbeddings = *file.Indexing.UseEmbeddings
		}
		if file.Indexing.SummariseFiles != nil {
			cfg.Indexing.SummariseFiles = *file.Indexing.SummariseFiles
		}
		if file.Indexing.Ignore != nil {
			cfg.Indexing.Ignore = file.Indexing.Ignore
		}
	}
	if file.Providers != nil {
		if cfg.Providers == nil {
			cfg.Providers = make(map[string]ProviderConfig, len(file.Providers))
		}
		// Whole-entry overwrite by key: a provider name defined in both the
		// global and project file is fully replaced by the project file's
		// entry (not deep-merged field-by-field), mirroring how a later file
		// "wins" for scalar fields elsewhere in this function.
		for name, pc := range file.Providers {
			cfg.Providers[name] = pc
		}
	}
	if file.Models != nil && file.Models.Presets != nil {
		if cfg.Models.Presets == nil {
			cfg.Models.Presets = map[string]routing.ModelPreset{}
		}
		for name, preset := range file.Models.Presets {
			cfg.Models.Presets[name] = presetFromConfig(name, preset)
		}
	}
	if file.AgentProfiles != nil {
		if cfg.AgentProfiles == nil {
			cfg.AgentProfiles = map[string]routing.AgentProfile{}
		}
		for name, profile := range file.AgentProfiles {
			cfg.AgentProfiles[name] = profileFromConfig(name, profile)
		}
	}
	if file.Agents != nil {
		if cfg.Agents == nil {
			cfg.Agents = map[routing.AgentRole]AgentRoleConfig{}
		}
		for role, agentCfg := range file.Agents {
			cfg.Agents[role] = AgentRoleConfig{Context: contextBudgetFromConfig(agentCfg.Context)}
		}
	}
	if file.Tools != nil && file.Tools.Shell != nil {
		s := file.Tools.Shell
		if s.DefaultTimeoutSeconds != nil {
			cfg.Tools.Shell.DefaultTimeoutSeconds = *s.DefaultTimeoutSeconds
		}
		if s.MaxOutputBytes != nil {
			cfg.Tools.Shell.MaxOutputBytes = *s.MaxOutputBytes
		}
		if s.MaxBackgroundJobs != nil {
			cfg.Tools.Shell.MaxBackgroundJobs = *s.MaxBackgroundJobs
		}
		if s.BackgroundRetention != nil && *s.BackgroundRetention != "" {
			d, err := time.ParseDuration(*s.BackgroundRetention)
			if err != nil {
				return fmt.Errorf("parse background_retention: %w", err)
			}
			cfg.Tools.Shell.BackgroundRetention = d
		}
		if s.AllowNetwork != nil {
			cfg.Tools.Shell.AllowNetwork = *s.AllowNetwork
		}
		if s.AllowSudo != nil {
			cfg.Tools.Shell.AllowSudo = *s.AllowSudo
		}
		if s.AllowDestructive != nil {
			cfg.Tools.Shell.AllowDestructive = *s.AllowDestructive
		}
		if s.AutoApprove != nil {
			cfg.Tools.Shell.AutoApprove = *s.AutoApprove
		}
		if s.GuardrailDynamicArgv0 != nil {
			cfg.Tools.Shell.GuardrailDynamicArgv0 = *s.GuardrailDynamicArgv0
		}
		if s.Allow != nil && s.Allow.Commands != nil {
			cfg.Tools.Shell.Allow.Commands = s.Allow.Commands
		}
		if s.Confirm != nil && s.Confirm.Commands != nil {
			cfg.Tools.Shell.Confirm.Commands = s.Confirm.Commands
		}
		if s.Deny != nil && s.Deny.Patterns != nil {
			cfg.Tools.Shell.Deny.Patterns = s.Deny.Patterns
		}
		if s.Sandbox != nil {
			if s.Sandbox.Backend != nil {
				cfg.Tools.Shell.Sandbox.Backend = *s.Sandbox.Backend
			}
			if s.Sandbox.MemoryLimitMB != nil {
				cfg.Tools.Shell.Sandbox.MemoryLimitMB = *s.Sandbox.MemoryLimitMB
			}
			if s.Sandbox.CPUSeconds != nil {
				cfg.Tools.Shell.Sandbox.CPUSeconds = *s.Sandbox.CPUSeconds
			}
			if s.Sandbox.MaxProcesses != nil {
				cfg.Tools.Shell.Sandbox.MaxProcesses = *s.Sandbox.MaxProcesses
			}
			if s.Sandbox.FileSizeLimitMB != nil {
				cfg.Tools.Shell.Sandbox.FileSizeLimitMB = *s.Sandbox.FileSizeLimitMB
			}
			if s.Sandbox.ContainerRuntime != nil {
				cfg.Tools.Shell.Sandbox.ContainerRuntime = *s.Sandbox.ContainerRuntime
			}
			if s.Sandbox.ContainerImage != nil {
				cfg.Tools.Shell.Sandbox.ContainerImage = *s.Sandbox.ContainerImage
			}
			if s.Sandbox.AllowFallback != nil {
				cfg.Tools.Shell.Sandbox.AllowFallback = *s.Sandbox.AllowFallback
			}
			if s.Sandbox.EnvAllowlist != nil {
				cfg.Tools.Shell.Sandbox.EnvAllowlist = s.Sandbox.EnvAllowlist
			}
			if s.Sandbox.EnvDenylist != nil {
				cfg.Tools.Shell.Sandbox.EnvDenylist = s.Sandbox.EnvDenylist
			}
		}
	}
	if file.Swarm != nil && file.Swarm.Budget != nil {
		b := file.Swarm.Budget
		if b.MaxFixRounds != nil {
			cfg.Swarm.Budget.MaxFixRounds = *b.MaxFixRounds
		}
		if b.MaxTotalTokens != nil {
			cfg.Swarm.Budget.MaxTotalTokens = *b.MaxTotalTokens
		}
		for role, iters := range b.ToolIters {
			if cfg.Swarm.Budget.ToolIters == nil {
				cfg.Swarm.Budget.ToolIters = map[string]int{}
			}
			cfg.Swarm.Budget.ToolIters[role] = iters
		}
	}
	if file.MCP != nil {
		for name, srv := range file.MCP.Servers {
			if cfg.MCP.Servers == nil {
				cfg.MCP.Servers = map[string]MCPServerConfig{}
			}
			cfgSrv := MCPServerConfig{Env: srv.Env}
			if srv.Command != nil {
				cfgSrv.Command = *srv.Command
			}
			cfgSrv.Args = srv.Args
			cfg.MCP.Servers[name] = cfgSrv
		}
		for k, v := range file.MCP.Policies {
			if cfg.MCP.Policies == nil {
				cfg.MCP.Policies = map[string]string{}
			}
			cfg.MCP.Policies[k] = v
		}
		if file.MCP.DisclosureThresholdTools != nil {
			cfg.MCP.DisclosureThresholdTools = *file.MCP.DisclosureThresholdTools
		}
	}
	if file.Snapshots != nil {
		if file.Snapshots.Enabled != nil {
			cfg.Snapshots.Enabled = *file.Snapshots.Enabled
		}
		if file.Snapshots.RetentionDays != nil {
			cfg.Snapshots.RetentionDays = *file.Snapshots.RetentionDays
		}
		if file.Snapshots.MaxFileBytes != nil {
			cfg.Snapshots.MaxFileBytes = *file.Snapshots.MaxFileBytes
		}
	}
	if file.Permissions != nil && file.Permissions.Rules != nil {
		cfg.Permissions.Rules = file.Permissions.Rules
	}
	if file.Diagnostics != nil && file.Diagnostics.Commands != nil {
		if cfg.Diagnostics.Commands == nil {
			cfg.Diagnostics.Commands = map[string]string{}
		}
		for k, v := range file.Diagnostics.Commands {
			cfg.Diagnostics.Commands[k] = v
		}
	}
	if file.Web != nil {
		if file.Web.Enabled != nil {
			cfg.Web.Enabled = *file.Web.Enabled
		}
		if file.Web.FetchTimeout != nil && *file.Web.FetchTimeout != "" {
			d, err := time.ParseDuration(*file.Web.FetchTimeout)
			if err != nil {
				return fmt.Errorf("parse web.fetch_timeout: %w", err)
			}
			cfg.Web.FetchTimeout = d
		}
		if file.Web.SearchProvider != nil {
			cfg.Web.SearchProvider = *file.Web.SearchProvider
		}
		if file.Web.SearchURL != nil {
			cfg.Web.SearchURL = *file.Web.SearchURL
		}
		if file.Web.SearchKey != nil {
			cfg.Web.SearchKey = *file.Web.SearchKey
		}
	}
	if file.Hooks != nil {
		if file.Hooks.FailClosed != nil {
			cfg.Hooks.FailClosed = *file.Hooks.FailClosed
		}
		if file.Hooks.Entries != nil {
			cfg.Hooks.Entries = nil
			for _, entry := range file.Hooks.Entries {
				var hc HookConfig
				if entry.Event != nil {
					hc.Event = *entry.Event
				}
				if entry.Matcher != nil {
					hc.Matcher = *entry.Matcher
				}
				if entry.Command != nil {
					hc.Command = *entry.Command
				}
				if entry.TimeoutMS != nil {
					hc.TimeoutMS = *entry.TimeoutMS
				}
				cfg.Hooks.Entries = append(cfg.Hooks.Entries, hc)
			}
		}
	}
	return nil
}

func HasConfig(opts LoadOptions) bool {
	home := opts.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return false
		}
	}

	work := opts.WorkingDir
	if work == "" {
		var err error
		work, err = os.Getwd()
		if err != nil {
			return false
		}
	}

	for _, path := range []string{
		filepath.Join(home, ".config", "marshal", "config.toml"),
		filepath.Join(work, ".marshal", "config.toml"),
	} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}
