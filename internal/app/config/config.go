package config

import (
	"fmt"
	"marshal/internal/llm/routing"
	"os"
	"path/filepath"

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

type ShellToolConfig struct {
	DefaultTimeoutSeconds int          `toml:"default_timeout_seconds"`
	MaxOutputBytes        int          `toml:"max_output_bytes"`
	AllowNetwork          bool         `toml:"allow_network"`
	AllowSudo             bool         `toml:"allow_sudo"`
	AllowDestructive      bool         `toml:"allow_destructive"`
	AutoApprove           bool         `toml:"auto_approve"`
	Allow                 CommandRules `toml:"allow"`
	Confirm               CommandRules `toml:"confirm"`
	Deny                  PatternRules `toml:"deny"`
}

type CommandRules struct {
	Commands []string `toml:"commands"`
}

type PatternRules struct {
	Patterns []string `toml:"patterns"`
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
	Provider          string `toml:"provider"`
	Model             string `toml:"model"`
	MaxToolIterations int    `toml:"max_tool_iterations"`
	MaxRetries        int    `toml:"max_retries"`
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
	Type      string `toml:"type"` // "openai_compatible" is the only supported value in this milestone
	BaseURL   string `toml:"base_url"`
	APIKey    string `toml:"api_key"`     // literal key; wins over APIKeyEnv if both set
	APIKeyEnv string `toml:"api_key_env"` // env var name to resolve at provider-construction time (NOT resolved here)
}

type LoadOptions struct {
	HomeDir    string
	WorkingDir string
}

type configFile struct {
	Project *struct {
		Name      *string  `toml:"name"`
		Languages []string `toml:"languages"`
	} `toml:"project"`
	Commands *struct {
		Test   *string `toml:"test"`
		Format *string `toml:"format"`
		Vet    *string `toml:"vet"`
	} `toml:"commands"`
	Profile *struct {
		Default *string `toml:"default"`
	} `toml:"profile"`
	Agent *struct {
		Provider          *string `toml:"provider"`
		Model             *string `toml:"model"`
		MaxToolIterations *int    `toml:"max_tool_iterations"`
		MaxRetries        *int    `toml:"max_retries"`
	} `toml:"agent"`
	Privacy *struct {
		RemoteProvidersAllowed *bool `toml:"remote_providers_allowed"`
		RedactSecrets          *bool `toml:"redact_secrets"`
		IncludeGitignoredFiles *bool `toml:"include_gitignored_files"`
	} `toml:"privacy"`
	Indexing *struct {
		UseTreesitter  *bool    `toml:"use_treesitter"`
		UseEmbeddings  *bool    `toml:"use_embeddings"`
		SummariseFiles *bool    `toml:"summarise_files"`
		Ignore         []string `toml:"ignore"`
	} `toml:"indexing"`
	Tools *struct {
		Shell *struct {
			DefaultTimeoutSeconds *int          `toml:"default_timeout_seconds"`
			MaxOutputBytes        *int          `toml:"max_output_bytes"`
			AllowNetwork          *bool         `toml:"allow_network"`
			AllowSudo             *bool         `toml:"allow_sudo"`
			AllowDestructive      *bool         `toml:"allow_destructive"`
			AutoApprove           *bool         `toml:"auto_approve"`
			Allow                 *CommandRules `toml:"allow"`
			Confirm               *CommandRules `toml:"confirm"`
			Deny                  *PatternRules `toml:"deny"`
		} `toml:"shell"`
	} `toml:"tools"`
	// Providers, unlike the other configFile fields above, is not a
	// pointer-to-anonymous-struct: a nil map already distinguishes
	// "providers section absent from this file" from "present", so no
	// pointer wrapping is needed.
	Providers map[string]ProviderConfig `toml:"providers"`
	Models    *struct {
		Presets map[string]modelPresetConfig `toml:"presets"`
	} `toml:"models"`
	AgentProfiles map[string]agentProfileConfig `toml:"agent_profiles"`
	Agents        map[routing.AgentRole]struct {
		Context contextBudgetConfig `toml:"context"`
	} `toml:"agents"`
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
				AllowNetwork:          false,
				AllowSudo:             false,
				AllowDestructive:      false,
				AutoApprove:           false,
				Allow:                 CommandRules{Commands: []string{"go test", "git status", "git diff"}},
				Confirm:               CommandRules{Commands: []string{"go get", "npm install"}},
				Deny:                  PatternRules{Patterns: []string{"rm -rf", "sudo", "curl * | sh"}},
			},
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

	for _, path := range []string{
		filepath.Join(home, ".config", "marshal", "config.toml"),
		filepath.Join(work, ".marshal", "config.toml"),
	} {
		next, err := loadFile(path)
		if err != nil {
			return Config{}, err
		}
		merge(&cfg, next)
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

func merge(cfg *Config, file configFile) {
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
		if s.Allow != nil && s.Allow.Commands != nil {
			cfg.Tools.Shell.Allow.Commands = s.Allow.Commands
		}
		if s.Confirm != nil && s.Confirm.Commands != nil {
			cfg.Tools.Shell.Confirm.Commands = s.Confirm.Commands
		}
		if s.Deny != nil && s.Deny.Patterns != nil {
			cfg.Tools.Shell.Deny.Patterns = s.Deny.Patterns
		}
	}
}
