package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/llm/routing"
)

func TestDefaultConfigValues(t *testing.T) {
	cfg := Default()

	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want marshal", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown"}) {
		t.Fatalf("Project.Languages = %#v, want go and markdown", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "go test ./..." {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "gofmt -w ." {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Commands.Vet != "go vet ./..." {
		t.Fatalf("Commands.Vet = %q", cfg.Commands.Vet)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want false")
	}
	if !cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = false, want true")
	}
	if cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = true, want false")
	}
}

func TestLoadIgnoresMissingConfigFiles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "marshal" {
		t.Fatalf("Project.Name = %q, want default marshal", cfg.Project.Name)
	}
}

func TestLoadProjectConfigOverridesGlobalConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[project]
name = "global"
languages = ["go"]

[commands]
test = "global test"

[privacy]
remote_providers_allowed = true
redact_secrets = false
`)

	writeFile(t, work+"/.marshal/config.toml", `
[project]
name = "project"
languages = ["go", "markdown", "toml"]

[commands]
test = "project test"
format = "project format"

[privacy]
remote_providers_allowed = false
include_gitignored_files = true
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Project.Name != "project" {
		t.Fatalf("Project.Name = %q, want project", cfg.Project.Name)
	}
	if !reflect.DeepEqual(cfg.Project.Languages, []string{"go", "markdown", "toml"}) {
		t.Fatalf("Project.Languages = %#v", cfg.Project.Languages)
	}
	if cfg.Commands.Test != "project test" {
		t.Fatalf("Commands.Test = %q", cfg.Commands.Test)
	}
	if cfg.Commands.Format != "project format" {
		t.Fatalf("Commands.Format = %q", cfg.Commands.Format)
	}
	if cfg.Privacy.RemoteProvidersAllowed {
		t.Fatal("RemoteProvidersAllowed = true, want project override false")
	}
	if cfg.Privacy.RedactSecrets {
		t.Fatal("RedactSecrets = true, want global override false")
	}
	if !cfg.Privacy.IncludeGitignoredFiles {
		t.Fatal("IncludeGitignoredFiles = false, want project override true")
	}
}

func TestLoadMalformedConfigReturnsPath(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	path := work + "/.marshal/config.toml"
	writeFile(t, path, "[project\nname = broken")

	_, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err == nil {
		t.Fatal("Load returned nil error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not contain path %q", err.Error(), path)
	}
}

func TestDefaultConfigHasNoProviders(t *testing.T) {
	cfg := Default()

	if len(cfg.Providers) != 0 {
		t.Fatalf("Providers = %#v, want nil or empty", cfg.Providers)
	}
}

func TestLoadParsesProvidersBlock(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, work+"/.marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "local-key"

[providers.openrouter]
type = "openai_compatible"
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
tool_calling = true
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Providers) != 2 {
		t.Fatalf("Providers = %#v, want 2 entries", cfg.Providers)
	}

	ollama, ok := cfg.Providers["ollama"]
	if !ok {
		t.Fatalf("Providers[ollama] missing, got %#v", cfg.Providers)
	}
	wantOllama := ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:11434/v1",
		APIKey:  "local-key",
	}
	if !reflect.DeepEqual(ollama, wantOllama) {
		t.Fatalf("Providers[ollama] = %#v, want %#v", ollama, wantOllama)
	}

	openrouter, ok := cfg.Providers["openrouter"]
	if !ok {
		t.Fatalf("Providers[openrouter] missing, got %#v", cfg.Providers)
	}
	wantOpenrouter := ProviderConfig{
		Type:        "openai_compatible",
		BaseURL:     "https://openrouter.ai/api/v1",
		APIKeyEnv:   "OPENROUTER_API_KEY",
		ToolCalling: true,
	}
	if !reflect.DeepEqual(openrouter, wantOpenrouter) {
		t.Fatalf("Providers[openrouter] = %#v, want %#v", openrouter, wantOpenrouter)
	}
	if openrouter.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("Providers[openrouter].APIKeyEnv = %q, want OPENROUTER_API_KEY verbatim", openrouter.APIKeyEnv)
	}
}

func TestLoadProjectProvidersOverwriteGlobalProvidersByKey(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "global-key"

[providers.lmstudio]
type = "openai_compatible"
base_url = "http://localhost:1234/v1"
api_key = "lmstudio-key"
`)

	writeFile(t, work+"/.marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:9999/v1"
api_key = "project-key"

[providers.openrouter]
type = "openai_compatible"
base_url = "https://openrouter.ai/api/v1"
api_key_env = "OPENROUTER_API_KEY"
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(cfg.Providers) != 3 {
		t.Fatalf("Providers = %#v, want 3 entries", cfg.Providers)
	}

	wantOllama := ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:9999/v1",
		APIKey:  "project-key",
	}
	if !reflect.DeepEqual(cfg.Providers["ollama"], wantOllama) {
		t.Fatalf("Providers[ollama] = %#v, want project override %#v", cfg.Providers["ollama"], wantOllama)
	}

	wantLMStudio := ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:1234/v1",
		APIKey:  "lmstudio-key",
	}
	if !reflect.DeepEqual(cfg.Providers["lmstudio"], wantLMStudio) {
		t.Fatalf("Providers[lmstudio] = %#v, want untouched global %#v", cfg.Providers["lmstudio"], wantLMStudio)
	}

	wantOpenrouter := ProviderConfig{
		Type:      "openai_compatible",
		BaseURL:   "https://openrouter.ai/api/v1",
		APIKeyEnv: "OPENROUTER_API_KEY",
	}
	if !reflect.DeepEqual(cfg.Providers["openrouter"], wantOpenrouter) {
		t.Fatalf("Providers[openrouter] = %#v, want %#v", cfg.Providers["openrouter"], wantOpenrouter)
	}
}

func TestLoadParsesAgentSection(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	dir := filepath.Join(home, ".config", "marshal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := "[agent]\nprovider = \"ollama\"\nmodel = \"qwen2.5-coder:14b\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(contents), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Agent.Provider != "ollama" {
		t.Fatalf("Agent.Provider = %q, want %q", cfg.Agent.Provider, "ollama")
	}
	if cfg.Agent.Model != "qwen2.5-coder:14b" {
		t.Fatalf("Agent.Model = %q, want %q", cfg.Agent.Model, "qwen2.5-coder:14b")
	}
}

func TestDefaultLeavesAgentProviderEmpty(t *testing.T) {
	cfg := Default()
	if cfg.Agent.Provider != "" || cfg.Agent.Model != "" {
		t.Fatalf("Default().Agent = %#v, want zero value (local-first: no assumed provider)", cfg.Agent)
	}
}

func TestLoadParsesAgentLimits(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	dir := filepath.Join(home, ".config", "marshal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := `[agent]
provider = "ollama"
model = "qwen3-coder"
max_tool_iterations = 32
max_retries = 5
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(contents), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Agent.MaxToolIterations != 32 {
		t.Fatalf("Agent.MaxToolIterations = %d, want 32", cfg.Agent.MaxToolIterations)
	}
	if cfg.Agent.MaxRetries != 5 {
		t.Fatalf("Agent.MaxRetries = %d, want 5", cfg.Agent.MaxRetries)
	}
}

func TestDefaultAgentLimitsAreZero(t *testing.T) {
	cfg := Default()
	if cfg.Agent.MaxToolIterations != 0 {
		t.Fatalf("Agent.MaxToolIterations = %d, want 0 (runner default applies)", cfg.Agent.MaxToolIterations)
	}
	if cfg.Agent.MaxRetries != 0 {
		t.Fatalf("Agent.MaxRetries = %d, want 0 (runner default applies)", cfg.Agent.MaxRetries)
	}
}

func TestSwarmBudgetDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Swarm.Budget.MaxFixRounds != 3 {
		t.Errorf("MaxFixRounds default = %d, want 3", cfg.Swarm.Budget.MaxFixRounds)
	}
	if cfg.Swarm.Budget.MaxTotalTokens != 120000 {
		t.Errorf("MaxTotalTokens default = %d, want 120000", cfg.Swarm.Budget.MaxTotalTokens)
	}
	if cfg.Swarm.Budget.ToolIters == nil {
		t.Fatal("ToolIters default should be an empty map, got nil")
	}
}

func TestSwarmBudgetMergesFromFile(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[swarm.budget]
max_fix_rounds = 5
max_total_tokens = 90000

[swarm.budget.tool_iters]
implementer = 25
tester = 4
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Swarm.Budget.MaxFixRounds != 5 {
		t.Errorf("MaxFixRounds = %d, want 5", cfg.Swarm.Budget.MaxFixRounds)
	}
	if cfg.Swarm.Budget.MaxTotalTokens != 90000 {
		t.Errorf("MaxTotalTokens = %d, want 90000", cfg.Swarm.Budget.MaxTotalTokens)
	}
	if cfg.Swarm.Budget.ToolIters["implementer"] != 25 {
		t.Errorf("ToolIters[implementer] = %d, want 25", cfg.Swarm.Budget.ToolIters["implementer"])
	}
	if cfg.Swarm.Budget.ToolIters["tester"] != 4 {
		t.Errorf("ToolIters[tester] = %d, want 4", cfg.Swarm.Budget.ToolIters["tester"])
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoadToolsRules(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[tools.shell]
auto_approve = false
allow_destructive = false
[tools.shell.allow]
commands = ["go test"]
[tools.shell.confirm]
commands = ["npm install"]
[tools.shell.deny]
patterns = ["sudo"]
`)

	writeFile(t, work+"/.marshal/config.toml", `
[tools.shell]
auto_approve = true
allow_destructive = true
[tools.shell.allow]
commands = ["go test", "git status"]
[tools.shell.confirm]
commands = ["yarn install"]
[tools.shell.deny]
patterns = ["rm -rf"]
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	s := cfg.Tools.Shell
	if !s.AutoApprove {
		t.Fatal("AutoApprove not merged, want true")
	}
	if !s.AllowDestructive {
		t.Fatal("AllowDestructive not merged, want true")
	}
	if !reflect.DeepEqual(s.Allow.Commands, []string{"go test", "git status"}) {
		t.Errorf("Allow.Commands = %#v", s.Allow.Commands)
	}
	if !reflect.DeepEqual(s.Confirm.Commands, []string{"yarn install"}) {
		t.Errorf("Confirm.Commands = %#v", s.Confirm.Commands)
	}
	if !reflect.DeepEqual(s.Deny.Patterns, []string{"rm -rf"}) {
		t.Errorf("Deny.Patterns = %#v", s.Deny.Patterns)
	}
}

func TestDefaultSandboxBackendIsRestricted(t *testing.T) {
	cfg := Default()
	sb := cfg.Tools.Shell.Sandbox
	if sb.Backend != "restricted" {
		t.Fatalf("default sandbox backend = %q, want restricted", sb.Backend)
	}
	if sb.MemoryLimitMB != 0 {
		t.Fatalf("default memory limit = %d, want 0 (opt-in)", sb.MemoryLimitMB)
	}
	if sb.MaxProcesses != 0 {
		t.Fatalf("default max processes = %d, want 0 (opt-in)", sb.MaxProcesses)
	}
	if sb.ContainerRuntime != "auto" {
		t.Fatalf("default container runtime = %q, want auto", sb.ContainerRuntime)
	}
	if sb.ContainerImage != "" {
		// Empty: the single source of truth for the default image lives
		// in internal/sandbox/container.go (defaultContainerImage) so
		// the fallback never drifts from the configured default.
		t.Fatalf("default container image = %q, want empty", sb.ContainerImage)
	}
	if !reflect.DeepEqual(sb.EnvAllowlist, []string{"PATH", "HOME", "USER", "SHELL", "LANG", "LC_ALL", "TERM", "TMPDIR", "GOPATH", "GOCACHE", "GOMODCACHE"}) {
		t.Errorf("default env allowlist = %#v", sb.EnvAllowlist)
	}
}

func TestLoadSandboxOverridesDefaults(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", `
[tools.shell.sandbox]
backend = "container"
memory_limit_mb = 512
cpu_seconds = 8
max_processes = 64
file_size_limit_mb = 100
container_runtime = "podman"
container_image = "golang:1.22"
allow_fallback = true
env_allowlist = ["PATH", "HOME", "GOPATH"]
env_denylist = ["SECRET_TOKEN"]
`)
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	sb := cfg.Tools.Shell.Sandbox
	if sb.Backend != "container" {
		t.Errorf("backend = %q, want container", sb.Backend)
	}
	if sb.MemoryLimitMB != 512 {
		t.Errorf("memory = %d, want 512", sb.MemoryLimitMB)
	}
	if sb.CPUSeconds != 8 {
		t.Errorf("cpu = %d, want 8", sb.CPUSeconds)
	}
	if sb.MaxProcesses != 64 {
		t.Errorf("max processes = %d, want 64", sb.MaxProcesses)
	}
	if sb.FileSizeLimitMB != 100 {
		t.Errorf("file size = %d, want 100", sb.FileSizeLimitMB)
	}
	if sb.ContainerRuntime != "podman" {
		t.Errorf("runtime = %q, want podman", sb.ContainerRuntime)
	}
	if sb.ContainerImage != "golang:1.22" {
		t.Errorf("image = %q, want golang:1.22", sb.ContainerImage)
	}
	if !reflect.DeepEqual(sb.EnvAllowlist, []string{"PATH", "HOME", "GOPATH"}) {
		t.Errorf("allowlist = %#v", sb.EnvAllowlist)
	}
	if !reflect.DeepEqual(sb.EnvDenylist, []string{"SECRET_TOKEN"}) {
		t.Errorf("denylist = %#v", sb.EnvDenylist)
	}
}

func TestLoadParsesRoutingConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[models.presets.coder]
provider = "ollama"
model = "qwen2.5-coder:14b"
context_window = 32768
max_output_tokens = 4096
temperature = 0.1
top_p = 1.0
tool_calling = "json"
reasoning_effort = "none"
local_only = true

[agent_profiles.local_balanced]
repo_scout = "coder"
implementer = "coder"
reviewer = "coder"

[agents.implementer.context]
max_repo_context_tokens = 48000
max_conversation_tokens = 8000
include_raw_code = true
include_summaries = true
include_symbols = true
include_diff = false
include_tests = true
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	preset := cfg.Models.Presets["coder"]
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" || !preset.LocalOnly {
		t.Fatalf("preset coder = %#v", preset)
	}
	if preset.ContextWindow != 32768 || preset.MaxOutputTokens != 4096 || preset.Temperature != 0.1 || preset.TopP != 1.0 {
		t.Fatalf("preset numeric fields = %#v", preset)
	}
	profile := cfg.AgentProfiles["local_balanced"]
	if profile.Roles[routing.RoleRepoScout] != "coder" || profile.Roles[routing.RoleImplementer] != "coder" {
		t.Fatalf("profile roles = %#v", profile.Roles)
	}
	budget := cfg.Agents[routing.RoleImplementer].Context
	if budget.MaxRepoContextTokens != 48000 || budget.MaxConversationTokens != 8000 || !budget.IncludeRawCode || !budget.IncludeTests {
		t.Fatalf("budget = %#v", budget)
	}
}

func TestLoadRoutingConfigProjectOverridesGlobalByKey(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", `
[models.presets.coder]
provider = "ollama"
model = "global"
local_only = true

[agent_profiles.local_balanced]
implementer = "coder"

[agents.implementer.context]
max_repo_context_tokens = 12000
`)
	writeFile(t, work+"/.marshal/config.toml", `
[models.presets.coder]
provider = "lmstudio"
model = "project"
local_only = true

[models.presets.fast]
provider = "ollama"
model = "fast"
local_only = true

[agent_profiles.local_balanced]
repo_scout = "fast"
implementer = "coder"

[agents.implementer.context]
max_repo_context_tokens = 48000
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Models.Presets["coder"].Provider != "lmstudio" || cfg.Models.Presets["coder"].Model != "project" {
		t.Fatalf("coder preset = %#v", cfg.Models.Presets["coder"])
	}
	if cfg.Models.Presets["fast"].Model != "fast" {
		t.Fatalf("fast preset missing: %#v", cfg.Models.Presets)
	}
	if cfg.AgentProfiles["local_balanced"].Roles[routing.RoleRepoScout] != "fast" {
		t.Fatalf("profile = %#v", cfg.AgentProfiles["local_balanced"])
	}
	if cfg.Agents[routing.RoleImplementer].Context.MaxRepoContextTokens != 48000 {
		t.Fatalf("implementer budget = %#v", cfg.Agents[routing.RoleImplementer].Context)
	}
}

func TestMCPConfigParsesAndMerges(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[mcp.servers.github]
command = "node"
args = ["server.js"]
env = { KEY = "VALUE" }

[mcp.policies]
"mcp.github.list_issues" = "allow"
"mcp.github.create_issue" = "confirm"
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	srv, ok := cfg.MCP.Servers["github"]
	if !ok {
		t.Fatal("github server config missing")
	}
	if srv.Command != "node" || len(srv.Args) != 1 || srv.Args[0] != "server.js" || srv.Env["KEY"] != "VALUE" {
		t.Errorf("invalid server config: %+v", srv)
	}

	if cfg.MCP.Policies["mcp.github.list_issues"] != "allow" {
		t.Errorf("policy list_issues = %q, want allow", cfg.MCP.Policies["mcp.github.list_issues"])
	}
}

func TestHasConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	if HasConfig(LoadOptions{HomeDir: home, WorkingDir: work}) {
		t.Error("expected HasConfig to return false when no config exists")
	}

	if err := os.MkdirAll(work+"/.marshal", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(work+"/.marshal/config.toml", []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	if !HasConfig(LoadOptions{HomeDir: home, WorkingDir: work}) {
		t.Error("expected HasConfig to return true when project config exists")
	}
}
