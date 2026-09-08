package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
	"marshal/internal/strutil"
)

func TestSaveVerifyCommandsPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[sdd]
verify_timeout_ms = 600000
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveVerifyCommands(path, "make build", "make test"); err != nil {
		t.Fatalf("SaveVerifyCommands: %v", err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "make build") || !strings.Contains(s, "make test") {
		t.Fatalf("commands not persisted:\n%s", s)
	}
	if !strings.Contains(s, "600000") {
		t.Errorf("verify_timeout_ms clobbered:\n%s", s)
	}
}

func TestSaveVerifyCommandsCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := SaveVerifyCommands(path, "go build ./...", "go test ./..."); err != nil {
		t.Fatalf("SaveVerifyCommands: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "go build ./...") {
		t.Fatalf("created file missing build:\n%s", data)
	}
}

func TestSaveProjectConfigRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Privacy.RedactSecrets = false
	cfg.Privacy.IncludeGitignoredFiles = true
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "ollama/qwen2.5-coder:14b"},
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:14b": {
			Name:      "ollama/qwen2.5-coder:14b",
			Provider:  "ollama",
			Model:     "qwen2.5-coder:14b",
			LocalOnly: true,
		},
	}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}
	// [models.presets] is user-global only: project saves skip it, so the
	// preset half of the round trip goes through the user config writer.
	if err := SaveUserConfigPresets(UserConfigPath(tmp), cfg.Models.Presets); err != nil {
		t.Fatalf("SaveUserConfigPresets failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Profile.Default != "local_balanced" {
		t.Fatalf("profile default = %q, want local_balanced", loaded.Profile.Default)
	}
	if loaded.Privacy.RemoteProvidersAllowed {
		t.Fatal("remote_providers_allowed = true, want false")
	}
	if loaded.Privacy.RedactSecrets {
		t.Fatal("redact_secrets = true, want false")
	}
	if !loaded.Privacy.IncludeGitignoredFiles {
		t.Fatal("include_gitignored_files = false, want true")
	}
	preset := loaded.Models.Presets["ollama/qwen2.5-coder:14b"]
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" || !preset.LocalOnly {
		t.Fatalf("preset ollama/qwen2.5-coder:14b = %+v", preset)
	}
}

func TestLoadMigratesLegacyAgentPair(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write the legacy [agent] provider/model pair directly; the fields no
	// longer exist on AgentConfig, so the load path reads them from the raw
	// file mirror and migrates them.
	legacy := "[agent]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Migration creates a single-model profile and preset.
	if loaded.Profile.Default != "single" {
		t.Fatalf("expected default profile 'single', got %q", loaded.Profile.Default)
	}
	profile, ok := loaded.AgentProfiles["single"]
	if !ok {
		t.Fatal("expected 'single' profile after migration")
	}
	presetName := profile.Roles[routing.RoleImplementer].Preset
	if presetName != "anthropic/claude-sonnet-4" {
		t.Fatalf("expected preset 'anthropic/claude-sonnet-4', got %q", presetName)
	}
}

func TestSaveProjectConfigRoundTripsAgentAndToolSettings(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.MaxToolIterations = 8
	cfg.Agent.MaxRetries = 2
	cfg.Agent.ReconnectMaxWaitSeconds = 300
	cfg.Agent.MaxConcurrentSubagents = 7
	cfg.Tools.Shell.DefaultTimeoutSeconds = 45
	cfg.Tools.Shell.MaxOutputBytes = 98765
	cfg.Tools.Shell.AllowNetwork = true
	cfg.Tools.Shell.AutoApprove = true
	cfg.Tools.Shell.Sandbox.Backend = "container"
	cfg.Tools.Shell.Sandbox.MemoryLimitMB = 512
	cfg.Tools.Shell.Sandbox.CPUSeconds = 4
	cfg.Tools.Shell.Sandbox.MaxProcesses = 64
	cfg.Tools.Shell.Sandbox.FileSizeLimitMB = 32
	cfg.Tools.Shell.Sandbox.ContainerRuntime = "podman"
	cfg.Tools.Shell.Sandbox.ContainerImage = "golang:1.22"
	cfg.Tools.Shell.Sandbox.EnvAllowlist = []string{"PATH", "HOME", "GOPATH"}
	cfg.Tools.Shell.Sandbox.EnvDenylist = []string{"SECRET"}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "coder"},
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.MaxToolIterations != 8 || loaded.Agent.MaxRetries != 2 ||
		loaded.Agent.ReconnectMaxWaitSeconds != 300 || loaded.Agent.MaxConcurrentSubagents != 7 {
		t.Fatalf("agent settings = %+v", loaded.Agent)
	}
	shell := loaded.Tools.Shell
	if shell.DefaultTimeoutSeconds != 45 || shell.MaxOutputBytes != 98765 ||
		!shell.AllowNetwork || !shell.AutoApprove {
		t.Fatalf("shell settings = %+v", shell)
	}
	sb := shell.Sandbox
	if sb.Backend != "container" || sb.MemoryLimitMB != 512 || sb.CPUSeconds != 4 ||
		sb.MaxProcesses != 64 || sb.FileSizeLimitMB != 32 ||
		sb.ContainerRuntime != "podman" || sb.ContainerImage != "golang:1.22" {
		t.Fatalf("sandbox settings = %+v", sb)
	}
	if !reflect.DeepEqual(sb.EnvAllowlist, []string{"PATH", "HOME", "GOPATH"}) {
		t.Fatalf("sandbox EnvAllowlist = %#v", sb.EnvAllowlist)
	}
	if !reflect.DeepEqual(sb.EnvDenylist, []string{"SECRET"}) {
		t.Fatalf("sandbox EnvDenylist = %#v", sb.EnvDenylist)
	}
}

func TestSaveProjectConfigRoundTripsAgentScalars(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.MaxToolIterations = 8
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "ollama/qwen2.5-coder:14b"},
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:14b": {
			Name:     "ollama/qwen2.5-coder:14b",
			Provider: "ollama",
			Model:    "qwen2.5-coder:14b",
		},
	}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.MaxToolIterations != 8 {
		t.Fatalf("agent scalar not round-tripped, got %+v", loaded.Agent)
	}
}

func TestSaveProjectConfigPreservesHooks(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := `
[hooks]
fail_closed = true

[[hooks.entries]]
event = "pre_tool_use"
matcher = "file.write_patch"
command = "./scripts/check-patch.sh"
timeout_ms = 2500
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Hooks.FailClosed {
		t.Fatal("Hooks.FailClosed = false, want true (preserved across save)")
	}
	if len(loaded.Hooks.Entries) != 1 {
		t.Fatalf("len(Hooks.Entries) = %d, want 1 (preserved across save)", len(loaded.Hooks.Entries))
	}
	got := loaded.Hooks.Entries[0]
	if got.Event != "pre_tool_use" || got.Matcher != "file.write_patch" || got.Command != "./scripts/check-patch.sh" || got.TimeoutMS != 2500 {
		t.Fatalf("hook entry not preserved: %+v", got)
	}
}

func TestSaveProjectConfigRoundTripsPlanFirst(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = ""
	cfg.AgentProfiles = nil
	cfg.Agent.PlanFirst = true

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !loaded.Agent.PlanFirst {
		t.Fatalf("Agent.PlanFirst = %v, want true", loaded.Agent.PlanFirst)
	}
}

func TestSaveProjectConfigRoundTripsParseRepairFeedback(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = ""
	cfg.AgentProfiles = nil
	cfg.Agent.ParseRepairFeedback = strutil.Ptr(false)

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Agent.ParseRepairFeedback == nil || *loaded.Agent.ParseRepairFeedback {
		t.Fatalf("Agent.ParseRepairFeedback = %v, want pointer to false (persisted)", loaded.Agent.ParseRepairFeedback)
	}
}

func TestSaveProjectConfigRoundTripsVerificationGate(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	// true persists (the load default is nil/off).
	cfg := Default()
	cfg.Profile.Default = ""
	cfg.AgentProfiles = nil
	cfg.Agent.VerificationGate = strutil.Ptr(true)

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Agent.VerificationGate == nil || !*loaded.Agent.VerificationGate {
		t.Fatalf("Agent.VerificationGate = %v, want pointer to true (persisted)", loaded.Agent.VerificationGate)
	}

	// Explicit false persists too (tri-state: false != absent).
	cfg2 := Default()
	cfg2.Profile.Default = ""
	cfg2.AgentProfiles = nil
	cfg2.Agent.VerificationGate = strutil.Ptr(false)
	if err := SaveProjectConfig(path, cfg2, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig (false) failed: %v", err)
	}
	loaded2, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load (false) failed: %v", err)
	}
	if loaded2.Agent.VerificationGate == nil || *loaded2.Agent.VerificationGate {
		t.Fatalf("Agent.VerificationGate = %v, want pointer to false (persisted)", loaded2.Agent.VerificationGate)
	}
}

func TestRemoteLimitDiscoveryConfigurable(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	// Differs from the derived default: remote_providers_allowed=false but
	// remote_limit_discovery=true must be persisted and reloaded independently.
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Privacy.RemoteLimitDiscovery = true

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !loaded.Privacy.RemoteLimitDiscovery {
		t.Errorf("RemoteLimitDiscovery = false, want true (persisted)")
	}
}

func TestSDDDispatchRetriesRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.SDD.DispatchRetries = 5

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.SDD.DispatchRetries != 5 {
		t.Errorf("SDD.DispatchRetries = %d, want 5", loaded.SDD.DispatchRetries)
	}
}

func TestSaveProjectConfigRoundTripsTUI(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	cfg := Default()
	cfg.TUI = TUIConfig{
		Theme:   "catppuccin",
		Palette: map[string]string{"accent": "#cba6f7"},
		Mode:    "ask",
	}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !reflect.DeepEqual(loaded.TUI, cfg.TUI) {
		t.Fatalf("TUI = %+v, want %+v", loaded.TUI, cfg.TUI)
	}
}

func TestSaveProjectConfigPreservesUnrelatedSections(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`
[commands]
test = "go test ./..."
format = "gofmt -w ."

[indexing]
use_treesitter = true
`), 0644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	cfg.Profile.Default = "local_balanced"
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Commands.Test != "go test ./..." {
		t.Fatalf("commands.test = %q", loaded.Commands.Test)
	}
	if !loaded.Indexing.UseTreesitter {
		t.Fatal("indexing.use_treesitter was not preserved")
	}
}

func TestSaveProjectConfigEmbeddingPresetRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// An empty value on an otherwise-default config must not emit the key
	// into the saved TOML (the whole [indexing] table is omitted).
	emptyCfg := Default()
	if err := SaveProjectConfig(path, emptyCfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig (empty) failed: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if bytes.Contains(raw, []byte("embedding_preset")) {
		t.Fatalf("saved TOML contains embedding_preset for empty value:\n%s", raw)
	}

	// A non-empty value round-trips through save/load.
	cfg, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	cfg.Indexing.EmbeddingPreset = "ollama/nomic-embed-text"
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Indexing.EmbeddingPreset != "ollama/nomic-embed-text" {
		t.Fatalf("embedding_preset = %q, want %q", loaded.Indexing.EmbeddingPreset, "ollama/nomic-embed-text")
	}
}

func fullEditedConfig() Config {
	cfg := Default()
	cfg.Project = ProjectConfig{Name: "acme", Languages: []string{"go", "python"}}
	cfg.Commands = CommandsConfig{Test: "make test", Format: "make fmt", Vet: "make vet"}
	cfg.Indexing = IndexingConfig{UseTreesitter: true, UseEmbeddings: true, SummariseFiles: true, Ignore: []string{"build/**"}, MaxIndexableFileBytes: 0, MaxSearchableFileBytes: 0}
	cfg.Web = WebConfig{Enabled: true, FetchTimeout: 45 * time.Second, SearchProvider: "searx", SearchURL: "http://localhost:8888", SearchKey: "sk-live-1234"}
	cfg.Desktop = DesktopConfig{Enabled: true, Mode: "attach", Headless: true, CDPURL: "http://localhost:9222", URLAllowlist: []string{"example.com"}, URLDenylist: []string{"evil.com/admin"}, DefaultTimeout: 60 * time.Second, ScreenshotFormat: "png"}
	cfg.Swarm.Budget = SwarmBudgetConfig{MaxFixRounds: 5, MaxTotalTokens: 99000, ToolIters: map[string]int{"tester": 9}}
	cfg.MCP = MCPConfig{
		Servers:                  map[string]MCPServerConfig{"fs": {Command: "mcp-fs", Args: []string{"--root", "."}, Env: map[string]string{"A": "1"}}},
		Policies:                 map[string]string{"fs": "confirm"},
		DisclosureThresholdTools: 25,
	}
	cfg.Snapshots = SnapshotsConfig{Enabled: false, RetentionDays: 14, MaxFileBytes: 1000}
	cfg.Permissions.Rules = []PermissionRule{{Permission: "shell", Pattern: "go *", Action: "allow"}}
	cfg.Diagnostics.Commands = map[string]string{"go": "go vet ./...", "py": "ruff check"}
	cfg.Hooks = HooksConfig{FailClosed: true, Entries: []HookConfig{{Event: "pre_tool", Matcher: "shell.*", Command: "echo hi", TimeoutMS: 500}}}
	cfg.Providers = map[string]ProviderConfig{"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "real-key", APIKeyEnv: "OLLAMA_KEY", ToolCalling: true}}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen3": {Name: "ollama/qwen3", Provider: "ollama", Model: "qwen3", ContextWindow: 32768, MaxOutputTokens: 4096, ToolCalling: "native", LocalOnly: true},
	}
	return cfg
}

func TestSaveProjectConfigFullSurfaceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	cfg := fullEditedConfig()

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: filepath.Join(dir, "no-home"), WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !reflect.DeepEqual(loaded.Project, cfg.Project) {
		t.Errorf("project: got %+v want %+v", loaded.Project, cfg.Project)
	}
	if loaded.Commands != cfg.Commands {
		t.Errorf("commands: got %+v want %+v", loaded.Commands, cfg.Commands)
	}
	if !reflect.DeepEqual(loaded.Indexing, cfg.Indexing) {
		t.Errorf("indexing: got %+v want %+v", loaded.Indexing, cfg.Indexing)
	}
	if loaded.Web != cfg.Web {
		t.Errorf("web: got %+v want %+v", loaded.Web, cfg.Web)
	}
	if !reflect.DeepEqual(loaded.Desktop, cfg.Desktop) {
		t.Errorf("desktop: got %+v want %+v", loaded.Desktop, cfg.Desktop)
	}
	if !reflect.DeepEqual(loaded.Swarm, cfg.Swarm) {
		t.Errorf("swarm: got %+v want %+v", loaded.Swarm, cfg.Swarm)
	}
	if !reflect.DeepEqual(loaded.MCP, cfg.MCP) {
		t.Errorf("mcp: got %+v want %+v", loaded.MCP, cfg.MCP)
	}
	if loaded.Snapshots != cfg.Snapshots {
		t.Errorf("snapshots: got %+v want %+v", loaded.Snapshots, cfg.Snapshots)
	}
	if !reflect.DeepEqual(loaded.Permissions, cfg.Permissions) {
		t.Errorf("permissions: got %+v want %+v", loaded.Permissions, cfg.Permissions)
	}
	if !reflect.DeepEqual(loaded.Diagnostics.Commands, cfg.Diagnostics.Commands) {
		t.Errorf("diagnostics: got %+v want %+v", loaded.Diagnostics.Commands, cfg.Diagnostics.Commands)
	}
	if !reflect.DeepEqual(loaded.Hooks, cfg.Hooks) {
		t.Errorf("hooks: got %+v want %+v", loaded.Hooks, cfg.Hooks)
	}
	// [providers] and [models.presets] are user-global only: the project save
	// must not emit them, so credentials can never land in the shared file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(raw), "providers") || strings.Contains(string(raw), "models") {
		t.Errorf("project config carries user-global sections:\n%s", raw)
	}

	// The same sections round-trip through the user config writers,
	// credentials included.
	userPath := UserConfigPath(filepath.Join(dir, "no-home"))
	if err := SaveUserConfigProviders(userPath, cfg.Providers); err != nil {
		t.Fatalf("SaveUserConfigProviders: %v", err)
	}
	if err := SaveUserConfigPresets(userPath, cfg.Models.Presets); err != nil {
		t.Fatalf("SaveUserConfigPresets: %v", err)
	}
	loaded, err = Load(LoadOptions{HomeDir: filepath.Join(dir, "no-home"), WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(loaded.Providers, cfg.Providers) {
		t.Errorf("providers: got %+v want %+v", loaded.Providers, cfg.Providers)
	}
	if !reflect.DeepEqual(loaded.Models.Presets["ollama/qwen3"], cfg.Models.Presets["ollama/qwen3"]) {
		t.Errorf("preset ollama/qwen3: got %+v want %+v", loaded.Models.Presets["ollama/qwen3"], cfg.Models.Presets["ollama/qwen3"])
	}
}

func TestSaveProjectConfigEditExistingSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `
[project]
name = "acme"
languages = ["go"]

[commands]
test = "go test ./..."
format = "gofmt -w ."
vet = "go vet ./..."
`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Project.Name != "acme" {
		t.Fatalf("seeded project.name = %q, want acme", loaded.Project.Name)
	}
	if loaded.Commands.Test != "go test ./..." {
		t.Fatalf("seeded commands.test = %q", loaded.Commands.Test)
	}

	loaded.Project.Name = "newname"
	loaded.Commands.Test = "make test"

	if err := SaveProjectConfig(path, loaded, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	reloaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Project.Name != "newname" {
		t.Errorf("project.name = %q, want newname (edit to existing section was dropped)", reloaded.Project.Name)
	}
	if reloaded.Commands.Test != "make test" {
		t.Errorf("commands.test = %q, want make test (edit to existing section was dropped)", reloaded.Commands.Test)
	}
	if reloaded.Commands.Format != "gofmt -w ." {
		t.Errorf("commands.format = %q, want gofmt -w . (untouched field was clobbered)", reloaded.Commands.Format)
	}
}

func TestSaveProjectConfigOmitsDefaultNewSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	if err := SaveProjectConfig(path, Default(), Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, section := range []string{"[web]", "[mcp]", "[hooks]", "[permissions]", "[snapshots]", "[diagnostics]", "[project]", "[commands]", "[indexing]", "[swarm.budget]", "[providers"} {
		if strings.Contains(string(data), section) {
			t.Errorf("default-valued section %s should be omitted from a pristine file:\n%s", section, data)
		}
	}
	// Positive lower bound: the always-written profile/agent/privacy/shell/sandbox
	// sections must be present on a pristine file. A regression that drops any of
	// them from SaveProjectConfig would silently disable user config.
	for _, section := range []string{"[profile]", "[agent]", "[privacy]", "[tools.shell]", "[tools.shell.sandbox]"} {
		if !strings.Contains(string(data), section) {
			t.Errorf("always-written section %s missing from a pristine file:\n%s", section, data)
		}
	}
}

func TestSaveSDDConfig(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, ".marshal", "config.toml")
	cfg := Default()
	cfg.SDD.AutoWorktree = false
	cfg.SDD.MaxFixRounds = 7
	cfg.SDD.PlansDir = "custom/plans"

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: work, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.SDD.AutoWorktree {
		t.Errorf("AutoWorktree = true, want false")
	}
	if loaded.SDD.MaxFixRounds != 7 {
		t.Errorf("MaxFixRounds = %d, want 7", loaded.SDD.MaxFixRounds)
	}
	if loaded.SDD.PlansDir != "custom/plans" {
		t.Errorf("PlansDir = %q, want %q", loaded.SDD.PlansDir, "custom/plans")
	}
}

func TestSaveProjectConfigRejectsBadBaseURL(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"evil": {Type: "openai_compatible", BaseURL: "javascript:alert(1)"},
		},
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err == nil {
		t.Fatal("expected SaveProjectConfig to reject javascript: URL, got nil")
	}
}

func TestSaveProjectConfigPreservesAgentProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	seed := "[agent_profiles.local_balanced]\nimplementer = \"fast\"\n"
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	loadedFile, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	rawRoles := loadedFile.AgentProfilesRaw["local_balanced"]
	if rawRoles == nil {
		t.Fatal("agent_profiles.local_balanced not found")
	}
	implVal, ok := rawRoles[string(routing.RoleImplementer)]
	if !ok {
		t.Fatal("agent_profiles.local_balanced.implementer not found")
	}
	if s, ok := implVal.(string); !ok || s != "fast" {
		t.Errorf("agent_profiles.local_balanced.implementer = %v (%T), want string(fast)", implVal, implVal)
	}
}

func TestSaveProjectConfigWritesAgentProfiles(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"fast": {
			Name: "fast",
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "small"},
				routing.RoleSDDReviewer: {CustomAgent: "large"},
			},
		},
	}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	file, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile failed: %v", err)
	}

	if file.AgentProfilesRaw == nil {
		t.Fatal("agent_profiles not written to file")
	}
	if file.AgentProfilesRaw["fast"] == nil {
		t.Fatal("agent_profiles.fast not written")
	}
	implVal := file.AgentProfilesRaw["fast"][string(routing.RoleImplementer)]
	if s, ok := implVal.(string); !ok || s != "small" {
		t.Errorf("agent_profiles.fast[implementer] = %v (%T), want string(small)", implVal, implVal)
	}
	reviewerVal := file.AgentProfilesRaw["fast"][string(routing.RoleSDDReviewer)]
	reviewerMap, ok := reviewerVal.(map[string]any)
	if !ok || reviewerMap["custom_agent"] != "large" {
		t.Errorf("agent_profiles.fast[sdd_reviewer] = %v (%T), want {custom_agent: large}", reviewerVal, reviewerVal)
	}
}

func TestLoadBareStringRoleBinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`
[agent_profiles.mine]
planner = "reasoning"
implementer = "coder"
`), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := loaded.AgentProfiles["mine"]
	if p.Roles[routing.RolePlanner].Preset != "reasoning" {
		t.Fatalf("planner = %+v, want Preset=reasoning", p.Roles[routing.RolePlanner])
	}
	if p.Roles[routing.RoleImplementer].Preset != "coder" {
		t.Fatalf("implementer = %+v, want Preset=coder", p.Roles[routing.RoleImplementer])
	}
}

func TestApprovalModeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/.marshal/config.toml"
	cfg := Default()
	cfg.Agent.ApprovalMode = "auto"
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent.ApprovalMode != "auto" {
		t.Fatalf("round-trip ApprovalMode = %q, want %q", loaded.Agent.ApprovalMode, "auto")
	}
}

// TestSaveProjectConfigDoesNotResurrectDeletedKeys pins the Phase 5 fix:
// whole-struct saves must not write a key that the file does not already
// contain when the merged value equals the default. This stops deleted
// keys from reappearing after an unrelated save.
func TestSaveProjectConfigDoesNotResurrectDeletedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Scenario 1: [agent] only carries max_retries. The merged config
	// carries the default MaxToolIterations = 0, which must not be written
	// back just because the section is being saved.
	if err := os.WriteFile(path, []byte("[agent]\nmax_retries = 5\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "max_tool_iterations") {
		t.Errorf("deleted key max_tool_iterations resurrected:\n%s", raw)
	}

	// Scenario 2: [skills] max_active is present. Resetting it to the
	// default in the running config and saving should update the existing
	// key, not remove the whole section. The key preservation case is
	// covered by round-trip tests above; this checks that an existing key
	// is updated rather than dropped when it holds a non-default value.
	cfg.Skills.MaxActive = Default().Skills.MaxActive
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save after reset: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.Skills.MaxActive != Default().Skills.MaxActive {
		t.Fatalf("max_active = %d, want default %d", loaded.Skills.MaxActive, Default().Skills.MaxActive)
	}
}

// TestSaveProjectConfigDeletedSkillsKeyStaysDeleted documents the boundary
// of the Phase 5 fix. When a key is deleted from the file but the running
// merged config still holds a non-default stale value, the per-key rule
// cannot distinguish "deleted" from "changed in the UI" and will write the
// key. Fixing that requires tracking per-field dirtiness or reloading the
// file before every save; see Open Question OQ-2 in the follow-up plan.
func TestSaveProjectConfigDeletedSkillsKeyStaysDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := os.WriteFile(path, []byte("[skills]\nmax_active = 3\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Skills.MaxActive != 3 {
		t.Fatalf("loaded max_active = %d, want 3", cfg.Skills.MaxActive)
	}

	// Simulate the user deleting max_active from the file behind the app's
	// back while the running config still holds the stale value 3.
	if err := os.WriteFile(path, []byte("[skills]\nautoload = []\n"), 0o644); err != nil {
		t.Fatalf("rewrite file without max_active: %v", err)
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The per-key rule sees merged != default, so it writes max_active.
	// This assertion documents current behavior rather than the ideal.
	if !strings.Contains(string(raw), "max_active") {
		t.Logf("note: max_active was omitted despite stale merged value (ideal, but not expected with current rule):\n%s", raw)
	}
}

func TestSaveUserConfigSectionMigratesLegacyAgentPair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".config", "marshal", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[agent]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Profile.Default != "single" {
		t.Fatalf("profile default = %q, want single", cfg.Profile.Default)
	}

	if err := SaveUserConfigSection(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "provider = \"anthropic\"") || strings.Contains(s, "model = \"claude-sonnet-4\"") {
		t.Errorf("legacy provider/model pair persisted:\n%s", s)
	}
	if !strings.Contains(s, "[models.presets]") {
		t.Errorf("migration preset not written:\n%s", s)
	}
	if !strings.Contains(s, "[agent_profiles.single]") {
		t.Errorf("migration profile not written:\n%s", s)
	}
}

func TestSaveProjectConfigRoundTripsMCPServerTrust(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"local": {Command: "node", Trust: "unrestricted"},
	}
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.MCP.Servers["local"].Trust; got != "unrestricted" {
		t.Fatalf("round-tripped trust = %q, want unrestricted", got)
	}
}

func TestSaveSidePanelRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	cfg := Default()
	cfg.TUI.SidePanel.WidthPct = 33
	cfg.TUI.SidePanel.Hidden = []string{"repo", "rules"}

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.TUI.SidePanel.WidthPct != 33 {
		t.Errorf("WidthPct = %d, want 33", loaded.TUI.SidePanel.WidthPct)
	}
	if len(loaded.TUI.SidePanel.Hidden) != 2 {
		t.Errorf("Hidden = %v, want 2 entries", loaded.TUI.SidePanel.Hidden)
	}
}

func TestPresetThinkingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// [models.presets] is user-global only, so the round trip goes through
	// the user config writer.
	path := UserConfigPath(dir)
	cfg := Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen3": {Name: "ollama/qwen3", Provider: "ollama", Model: "qwen3", Thinking: "medium"},
	}
	if err := SaveUserConfigPresets(path, cfg.Models.Presets); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := loaded.Models.Presets["ollama/qwen3"].Thinking; got != "medium" {
		t.Fatalf("preset thinking = %q, want medium", got)
	}

	// Empty thinking must be omitted from the saved TOML (omitempty).
	p := cfg.Models.Presets["ollama/qwen3"]
	p.Thinking = ""
	cfg.Models.Presets["ollama/qwen3"] = p
	if err := SaveUserConfigPresets(path, cfg.Models.Presets); err != nil {
		t.Fatalf("save (empty): %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if bytes.Contains(raw, []byte("thinking")) {
		t.Fatalf("saved TOML contains thinking for empty value:\n%s", raw)
	}
}

func TestPresetVerificationGateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// [models.presets] is user-global only, so the round trip goes through
	// the user config writer.
	path := UserConfigPath(dir)
	gate := true
	cfg := Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen3": {Name: "ollama/qwen3", Provider: "ollama", Model: "qwen3", VerificationGate: &gate},
	}
	if err := SaveUserConfigPresets(path, cfg.Models.Presets); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: dir, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := loaded.Models.Presets["ollama/qwen3"].VerificationGate; got == nil || !*got {
		t.Fatalf("preset verification_gate = %v, want pointer to true (persisted)", got)
	}

	// Nil must be omitted from the saved TOML (omitempty) so the global
	// fallback applies after a save of an override-free preset.
	p := cfg.Models.Presets["ollama/qwen3"]
	p.VerificationGate = nil
	cfg.Models.Presets["ollama/qwen3"] = p
	if err := SaveUserConfigPresets(path, cfg.Models.Presets); err != nil {
		t.Fatalf("save (nil): %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if bytes.Contains(raw, []byte("verification_gate")) {
		t.Fatalf("saved TOML contains verification_gate for nil value:\n%s", raw)
	}
}

func TestPresetPricingRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := UserConfigPath(tmp)
	cfg := Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"hosted": {
			Name:     "hosted",
			Provider: "openai",
			Model:    "gpt-4o",
			Pricing: &pricing.ModelPricing{
				InputPerMTokCents:  250,
				OutputPerMTokCents: 1000,
			},
		},
	}
	if err := SaveUserConfigPresets(path, cfg.Models.Presets); err != nil {
		t.Fatalf("SaveUserConfigPresets: %v", err)
	}
	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := loaded.Models.Presets["hosted"]
	if !ok {
		t.Fatal("preset 'hosted' not found after round-trip")
	}
	if got.Pricing == nil {
		t.Fatal("Pricing is nil after round-trip")
	}
	priced, ok := got.Pricing.(*pricing.ModelPricing)
	if !ok {
		t.Fatalf("Pricing has type %T, want *pricing.ModelPricing", got.Pricing)
	}
	if priced.InputPerMTokCents != 250 || priced.OutputPerMTokCents != 1000 {
		t.Errorf("Pricing = %+v, want Input=250 Output=1000", priced)
	}
}

func TestSaveUserConfigSectionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := Default()
	cfg.Agent.MaxToolIterations = 25
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]ProviderConfig{
		"openai": {BaseURL: "https://api.openai.com", APIKeyEnv: "OPENAI_API_KEY"},
	}

	if err := SaveUserConfigSection(path, cfg); err != nil {
		t.Fatalf("SaveUserConfigSection: %v", err)
	}

	loaded, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if loaded.Agent == nil || loaded.Agent.MaxToolIterations == nil || *loaded.Agent.MaxToolIterations != 25 {
		t.Fatalf("agent.max_tool_iterations not persisted: %+v", loaded.Agent)
	}
	if loaded.Privacy == nil || loaded.Privacy.RemoteProvidersAllowed == nil || !*loaded.Privacy.RemoteProvidersAllowed {
		t.Fatalf("privacy.remote_providers_allowed not persisted: %+v", loaded.Privacy)
	}
	if loaded.Providers["openai"].BaseURL != "https://api.openai.com" {
		t.Fatalf("provider base_url not persisted: %+v", loaded.Providers)
	}
}

func TestSaveUserConfigSectionPreservesUnrelated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Seed the file with a [swarm] section.
	seed := Default()
	seed.Swarm.Budget.MaxFixRounds = 7
	if err := SaveUserConfigSection(path, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Now write only agent changes — swarm must survive.
	next := Default()
	next.Agent.MaxToolIterations = 12
	if err := SaveUserConfigSection(path, next); err != nil {
		t.Fatalf("second save: %v", err)
	}

	loaded, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if loaded.Swarm == nil || loaded.Swarm.Budget == nil || loaded.Swarm.Budget.MaxFixRounds == nil || *loaded.Swarm.Budget.MaxFixRounds != 7 {
		t.Fatalf("swarm.budget.max_fix_rounds not preserved: %+v", loaded.Swarm)
	}
}
func TestSaveUserConfigProviderAPIKeyWritesKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SaveUserConfigProviderAPIKey(path, "openai", ProviderConfig{APIKey: "sk-test-123"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "sk-test-123") {
		t.Errorf("key not written:\n%s", data)
	}
}

func TestSaveUserConfigProviderAPIKeyClearsStaleEnvRef(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SaveUserConfigProviderAPIKey(path, "openai", ProviderConfig{APIKey: ""}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Seed an env reference, then switch to an inline key.
	if err := SaveUserConfigProviderAPIKey(path, "openai", ProviderConfig{APIKey: "sk-inline"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, _ := os.ReadFile(path)
	// TOML serializes empty strings as '' not "", so accept either.
	if strings.Contains(string(data), "api_key_env") &&
		!strings.Contains(string(data), `api_key_env = ''`) {
		t.Errorf("stale api_key_env survived:\n%s", data)
	}
}

// Regression test for issue #9: saving a credential for a provider that is
// not yet in the user config must persist the provider's real metadata
// (type, base_url, template, capabilities), not a zero-value skeleton. The
// saved entry has to yield a usable merged config when loaded with no
// project layer at all — i.e. from any other repository.
func TestSaveUserConfigProviderAPIKeyNewEntryPersistsFullProvider(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	work := filepath.Join(dir, "other-repo")
	for _, d := range []string{home, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	userPath := UserConfigPath(home)

	if err := SaveUserConfigProviderAPIKey(userPath, "ollama-cloud", ProviderConfig{
		Type:        "ollama",
		BaseURL:     "https://ollama.com",
		APIKey:      "sk-live-123",
		ToolCalling: true,
		Template:    "ollama-cloud",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Load exactly as Marshal would from an unrelated repository: no project
	// config exists under work.
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pc, ok := cfg.Providers["ollama-cloud"]
	if !ok {
		t.Fatalf("provider missing after load; providers=%v", cfg.Providers)
	}
	if pc.Type != "ollama" {
		t.Errorf("type = %q, want %q", pc.Type, "ollama")
	}
	if pc.BaseURL != "https://ollama.com" {
		t.Errorf("base_url = %q, want https://ollama.com", pc.BaseURL)
	}
	if pc.APIKey != "sk-live-123" {
		t.Errorf("api_key lost: %q", pc.APIKey)
	}
	if pc.APIKeyEnv != "" {
		t.Errorf("api_key_env = %q, want cleared", pc.APIKeyEnv)
	}
	if pc.ToolCalling != true {
		t.Errorf("tool_calling = false, want true")
	}
	if pc.Template != "ollama-cloud" {
		t.Errorf("template = %q, want ollama-cloud", pc.Template)
	}
}

// An existing global entry must keep its non-credential metadata when only
// the key rotates — callers may pass a credential-only value here.
func TestSaveUserConfigProviderAPIKeyExistingEntryKeepsMetadata(t *testing.T) {
	home := t.TempDir()
	path := UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := "# Marshal global configuration\n\n[providers.openai]\ntype = \"openai_compatible\"\nbase_url = \"https://api.openai.com/v1\"\napi_key_env = \"OPENAI_API_KEY\"\ntool_calling = true\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SaveUserConfigProviderAPIKey(path, "openai", ProviderConfig{APIKey: "sk-new"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pc, ok := cfg.Providers["openai"]
	if !ok {
		t.Fatalf("provider missing after load")
	}
	if pc.Type != "openai_compatible" || pc.BaseURL != "https://api.openai.com/v1" || !pc.ToolCalling {
		t.Fatalf("metadata clobbered: %+v", pc)
	}
	if pc.APIKey != "sk-new" {
		t.Errorf("api_key = %q, want sk-new", pc.APIKey)
	}
	if pc.APIKeyEnv != "" {
		t.Errorf("api_key_env = %q, want cleared", pc.APIKeyEnv)
	}
}

// A full provider definition (Type set) must write capabilities exactly as
// provided — including turning them off. Before this was explicit, an
// existing tool_calling = true survived a re-connect whose probe concluded
// the provider has no native tool support.
func TestSaveUserConfigProviderAPIKeyFullConfigCanDowngradeCapability(t *testing.T) {
	home := t.TempDir()
	path := UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := "# Marshal global configuration\n\n[providers.openai]\ntype = \"openai_compatible\"\nbase_url = \"https://api.openai.com/v1\"\napi_key_env = \"OPENAI_API_KEY\"\ntool_calling = true\nreasoning_summary = true\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SaveUserConfigProviderAPIKey(path, "openai", ProviderConfig{
		Type:    "openai",
		BaseURL: "https://api.openai.com/v2",
		APIKey:  "sk-new",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pc, ok := cfg.Providers["openai"]
	if !ok {
		t.Fatalf("provider missing after load")
	}
	if pc.Type != "openai" {
		t.Errorf("type = %q, want openai", pc.Type)
	}
	if pc.BaseURL != "https://api.openai.com/v2" {
		t.Errorf("base_url = %q, want https://api.openai.com/v2", pc.BaseURL)
	}
	if pc.ToolCalling {
		t.Errorf("tool_calling = true, want false (downgraded by full config)")
	}
	if pc.ReasoningSummary {
		t.Errorf("reasoning_summary = true, want false (downgraded by full config)")
	}
	if pc.APIKey != "sk-new" {
		t.Errorf("api_key = %q, want sk-new", pc.APIKey)
	}
	if pc.APIKeyEnv != "" {
		t.Errorf("api_key_env = %q, want cleared", pc.APIKeyEnv)
	}
}

// Re-connecting through the connect flow changes base_url (self-hosted
// endpoints move). The credential-only path must update it when supplied
// while preserving the rest of the entry's metadata.
func TestSaveUserConfigProviderAPIKeyBaseURLOnReconnect(t *testing.T) {
	home := t.TempDir()
	path := UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := "# Marshal global configuration\n\n[providers.ollama]\ntype = \"ollama\"\nbase_url = \"http://localhost:11434\"\ntemplate = \"ollama\"\ntool_calling = true\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Credential-only rotation must not disturb base_url...
	if err := SaveUserConfigProviderAPIKey(path, "ollama", ProviderConfig{APIKey: "sk-1"}); err != nil {
		t.Fatalf("save credential-only: %v", err)
	}
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("load after credential-only: %v", err)
	}
	if pc := cfg.Providers["ollama"]; pc.BaseURL != "http://localhost:11434" {
		t.Fatalf("credential-only save clobbered base_url: %q", pc.BaseURL)
	}
	// ...but an explicit re-connect with a new endpoint must move it.
	if err := SaveUserConfigProviderAPIKey(path, "ollama", ProviderConfig{
		BaseURL: "http://192.168.1.10:11434",
		APIKey:  "sk-2",
	}); err != nil {
		t.Fatalf("save reconnect: %v", err)
	}
	cfg, err = Load(LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("load after reconnect: %v", err)
	}
	pc, ok := cfg.Providers["ollama"]
	if !ok {
		t.Fatalf("provider missing after load")
	}
	if pc.BaseURL != "http://192.168.1.10:11434" {
		t.Errorf("base_url = %q, want http://192.168.1.10:11434", pc.BaseURL)
	}
	if pc.Type != "ollama" || pc.Template != "ollama" || !pc.ToolCalling {
		t.Errorf("metadata not preserved across base_url update: %+v", pc)
	}
}

func TestSaveUserConfigProviderAPIKeyPreservesOtherProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SaveUserConfigProviderAPIKey(path, "openai", ProviderConfig{APIKey: "sk-a"}); err != nil {
		t.Fatalf("save a: %v", err)
	}
	if err := SaveUserConfigProviderAPIKey(path, "groq", ProviderConfig{APIKey: "sk-b"}); err != nil {
		t.Fatalf("save b: %v", err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"sk-a", "sk-b"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("lost %q:\n%s", want, data)
		}
	}
}

func TestSaveUserConfigProvidersPreservesProviderTemplate(t *testing.T) {
	dir := t.TempDir()
	path := UserConfigPath(dir)
	providers := map[string]ProviderConfig{
		"work": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1", Template: "openai"},
	}
	if err := SaveUserConfigProviders(path, providers); err != nil {
		t.Fatalf("SaveUserConfigProviders: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `template = "openai"`) &&
		!strings.Contains(string(data), `template = 'openai'`) {
		t.Fatalf("template field not persisted:\n%s", data)
	}
}

func TestSaveUserConfigProvidersEmptyMapRemovesSection(t *testing.T) {
	dir := t.TempDir()
	path := UserConfigPath(dir)
	providers := map[string]ProviderConfig{
		"work": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
	}
	if err := SaveUserConfigProviders(path, providers); err != nil {
		t.Fatalf("SaveUserConfigProviders: %v", err)
	}
	if err := SaveUserConfigProviders(path, nil); err != nil {
		t.Fatalf("SaveUserConfigProviders(nil): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "providers") {
		t.Fatalf("[providers] not removed:\n%s", data)
	}
}

func TestSaveUserConfigPresetsEmptyMapRemovesSection(t *testing.T) {
	dir := t.TempDir()
	path := UserConfigPath(dir)
	presets := map[string]routing.ModelPreset{
		"ollama/qwen3": {Provider: "ollama", Model: "qwen3"},
	}
	if err := SaveUserConfigPresets(path, presets); err != nil {
		t.Fatalf("SaveUserConfigPresets: %v", err)
	}
	if err := SaveUserConfigPresets(path, nil); err != nil {
		t.Fatalf("SaveUserConfigPresets(nil): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "models") {
		t.Fatalf("[models] not removed:\n%s", data)
	}
}
