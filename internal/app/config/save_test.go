package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/llm/routing"
)

func TestSaveProjectConfigRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.Provider = "ollama"
	cfg.Agent.Model = "qwen2.5-coder:14b"
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Privacy.RedactSecrets = false
	cfg.Privacy.IncludeGitignoredFiles = true
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {
			Name:      "coder",
			Provider:  "ollama",
			Model:     "qwen2.5-coder:14b",
			LocalOnly: true,
		},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Profile.Default != "local_balanced" {
		t.Fatalf("profile default = %q, want local_balanced", loaded.Profile.Default)
	}
	if loaded.Agent.Provider != "" || loaded.Agent.Model != "" {
		t.Fatalf("agent section should be omitted when preset is active, got %+v", loaded.Agent)
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
	preset := loaded.Models.Presets["coder"]
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" || !preset.LocalOnly {
		t.Fatalf("preset coder = %+v", preset)
	}
}

func TestSaveProjectConfigRoundTripLegacyAgent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = ""
	cfg.Agent.Provider = "anthropic"
	cfg.Agent.Model = "claude-sonnet-4"
	cfg.AgentProfiles = nil

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.Provider != "anthropic" || loaded.Agent.Model != "claude-sonnet-4" {
		t.Fatalf("agent = %+v", loaded.Agent)
	}
}

func TestSaveProjectConfigRoundTripsAgentAndToolSettings(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.MaxToolIterations = 8
	cfg.Agent.MaxRetries = 2
	cfg.Tools.Shell.DefaultTimeoutSeconds = 45
	cfg.Tools.Shell.MaxOutputBytes = 98765
	cfg.Tools.Shell.AllowNetwork = true
	cfg.Tools.Shell.AllowSudo = true
	cfg.Tools.Shell.AllowDestructive = true
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
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.MaxToolIterations != 8 || loaded.Agent.MaxRetries != 2 {
		t.Fatalf("agent settings = %+v", loaded.Agent)
	}
	shell := loaded.Tools.Shell
	if shell.DefaultTimeoutSeconds != 45 || shell.MaxOutputBytes != 98765 ||
		!shell.AllowNetwork || !shell.AllowSudo || !shell.AllowDestructive || !shell.AutoApprove {
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

func TestSaveProjectConfigOmitsAgentWhenPresetActive(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")

	cfg := Default()
	cfg.Profile.Default = "local_balanced"
	cfg.Agent.Provider = "ollama"
	cfg.Agent.Model = "qwen2.5-coder:14b"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {
			Name:     "coder",
			Provider: "ollama",
			Model:    "qwen2.5-coder:14b",
		},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	loaded, err := Load(LoadOptions{HomeDir: tmp, WorkingDir: tmp})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Agent.Provider != "" || loaded.Agent.Model != "" {
		t.Fatalf("agent section should be omitted when preset is active, got %+v", loaded.Agent)
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
	if err := SaveProjectConfig(path, cfg); err != nil {
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

	if err := SaveProjectConfig(path, cfg); err != nil {
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

func TestSaveProjectConfigRoundTripsTUI(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".marshal", "config.toml")
	cfg := Default()
	cfg.TUI = TUIConfig{
		Theme:   "catppuccin",
		Palette: map[string]string{"accent": "#cba6f7"},
		Mode:    "ask",
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
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
	if err := SaveProjectConfig(path, cfg); err != nil {
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
		"fast": {Name: "fast", Provider: "ollama", Model: "qwen3", ContextWindow: 32768, MaxOutputTokens: 4096, ToolCalling: "native", LocalOnly: true},
	}
	return cfg
}

func TestSaveProjectConfigFullSurfaceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")
	cfg := fullEditedConfig()

	if err := SaveProjectConfig(path, cfg); err != nil {
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
	if !reflect.DeepEqual(loaded.Providers, cfg.Providers) {
		t.Errorf("providers: got %+v want %+v", loaded.Providers, cfg.Providers)
	}
	if !reflect.DeepEqual(loaded.Models.Presets["fast"], cfg.Models.Presets["fast"]) {
		t.Errorf("preset fast: got %+v want %+v", loaded.Models.Presets["fast"], cfg.Models.Presets["fast"])
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

	if err := SaveProjectConfig(path, loaded); err != nil {
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

	if err := SaveProjectConfig(path, Default()); err != nil {
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
	cfg.SDD.MaxTotalTokens = 50000
	cfg.SDD.PlansDir = "custom/plans"

	if err := SaveProjectConfig(path, cfg); err != nil {
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
	if loaded.SDD.MaxTotalTokens != 50000 {
		t.Errorf("MaxTotalTokens = %d, want 50000", loaded.SDD.MaxTotalTokens)
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
	if err := SaveProjectConfig(path, cfg); err == nil {
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
	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loadedFile, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if got := loadedFile.AgentProfiles["local_balanced"].Implementer; got != "fast" {
		t.Errorf("agent_profiles dropped by save: implementer=%q want %q", got, "fast")
	}
}

func TestSaveProjectConfigRoundTripsMCPServerTrust(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"local": {Command: "node", Trust: "unrestricted"},
	}
	if err := SaveProjectConfig(path, cfg); err != nil {
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
