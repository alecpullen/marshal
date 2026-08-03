package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/llm/routing"
	"marshal/internal/trust"

	"github.com/pelletier/go-toml/v2"
)

type staticTrustResolver struct {
	decision trust.Decision
}

func TestSet(t *testing.T) {
	dst := "keep"
	set(&dst, (*string)(nil))
	if dst != "keep" {
		t.Fatalf("nil src overwrote dst: %q", dst)
	}
	v := "new"
	set(&dst, &v)
	if dst != "new" {
		t.Fatalf("set did not assign: %q", dst)
	}
}

func (s staticTrustResolver) Resolve(workingDir string, hasProjectConfig bool) (trust.Decision, error) {
	return s.decision, nil
}

func (s staticTrustResolver) Record(workingDir string, decision trust.Decision) error {
	return nil
}

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

	// The project entry wins for non-credential fields, but a project entry
	// that names no credentials inherits them from the global file — project
	// configs are written without keys, so wholesale replacement would hide
	// the user's key.
	wantOllama := ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:9999/v1",
		APIKey:  "global-key",
	}
	if !reflect.DeepEqual(cfg.Providers["ollama"], wantOllama) {
		t.Fatalf("Providers[ollama] = %#v, want project override with inherited key %#v", cfg.Providers["ollama"], wantOllama)
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

func TestLoadProjectProviderWithOwnCredentialsDoesNotInherit(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "global-key"
api_key_env = "GLOBAL_OLLAMA_KEY"
`)

	// The project entry sets its own env-var credential: nothing is inherited
	// from the global entry (in particular the literal key, which would win
	// over the env var at provider-construction time).
	writeFile(t, work+"/.marshal/config.toml", `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:9999/v1"
api_key_env = "PROJECT_OLLAMA_KEY"
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := ProviderConfig{
		Type:      "openai_compatible",
		BaseURL:   "http://localhost:9999/v1",
		APIKeyEnv: "PROJECT_OLLAMA_KEY",
	}
	if !reflect.DeepEqual(cfg.Providers["ollama"], want) {
		t.Fatalf("Providers[ollama] = %#v, want %#v", cfg.Providers["ollama"], want)
	}
}

func TestSaveProjectConfigStripsProviderAPIKeys(t *testing.T) {
	work := t.TempDir()
	path := ProjectConfigPath(work)

	cfg := Default()
	cfg.Providers = map[string]ProviderConfig{
		"openai": {
			Type:    "openai_compatible",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "sk-secret-should-not-be-committed",
		},
	}

	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(data), "sk-secret-should-not-be-committed") {
		t.Fatalf("literal API key written to project config:\n%s", data)
	}
	if !strings.Contains(string(data), "api.openai.com") {
		t.Fatalf("provider entry missing from project config:\n%s", data)
	}
	// The caller's in-memory config must keep the key — the runtime resolves
	// credentials from it, not from the file just written.
	if cfg.Providers["openai"].APIKey != "sk-secret-should-not-be-committed" {
		t.Fatalf("SaveProjectConfig mutated the caller's provider entry: %#v", cfg.Providers["openai"])
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
	// The [agent] provider/model pair is deprecated: Load migrates it into a
	// preset plus a single-model profile, then clears the legacy fields. What
	// this test guards is that the section is still understood, not that the
	// old shape survives.
	if cfg.Agent.Provider != "" || cfg.Agent.Model != "" {
		t.Fatalf("legacy pair survived migration: %q/%q", cfg.Agent.Provider, cfg.Agent.Model)
	}
	const presetName = "ollama/qwen2.5-coder:14b"
	preset, ok := cfg.Models.Presets[presetName]
	if !ok {
		t.Fatalf("no preset %q after migration; presets = %v", presetName, cfg.Models.Presets)
	}
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" {
		t.Fatalf("preset = %s/%s, want ollama/qwen2.5-coder:14b", preset.Provider, preset.Model)
	}
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		t.Fatalf("default profile %q does not exist", cfg.Profile.Default)
	}
	if got := profile.Roles[routing.RoleImplementer].Preset; got != presetName {
		t.Fatalf("implementer bound to %q, want %q", got, presetName)
	}
}

func TestDefaultLeavesAgentProviderEmpty(t *testing.T) {
	cfg := Default()
	if cfg.Agent.Provider != "" || cfg.Agent.Model != "" {
		t.Fatalf("Default().Agent = %#v, want zero value (local-friendly: no assumed provider)", cfg.Agent)
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

func TestLoadParsesPlanFirst(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	dir := filepath.Join(home, ".config", "marshal")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := `[agent]
plan_first = true
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(contents), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Agent.PlanFirst {
		t.Fatalf("Agent.PlanFirst = %v, want true", cfg.Agent.PlanFirst)
	}
}

func TestDefaultPlanFirstIsFalse(t *testing.T) {
	cfg := Default()
	if cfg.Agent.PlanFirst {
		t.Fatal("Agent.PlanFirst = true, want false by default")
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

func TestSDDConfigDefaults(t *testing.T) {
	cfg := Default()
	if !cfg.SDD.AutoWorktree {
		t.Errorf("AutoWorktree default = false, want true")
	}
	if cfg.SDD.MaxFixRounds != 3 {
		t.Errorf("MaxFixRounds default = %d, want 3", cfg.SDD.MaxFixRounds)
	}
	if cfg.SDD.PlansDir != ".marshal/plans" {
		t.Errorf("PlansDir default = %q, want %q", cfg.SDD.PlansDir, ".marshal/plans")
	}
}

func TestSDDConfigMergesFromFile(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[sdd]
auto_worktree = false
max_fix_rounds = 5
plans_dir = "docs/plans"
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SDD.AutoWorktree {
		t.Errorf("AutoWorktree = true, want false")
	}
	if cfg.SDD.MaxFixRounds != 5 {
		t.Errorf("MaxFixRounds = %d, want 5", cfg.SDD.MaxFixRounds)
	}
	if cfg.SDD.PlansDir != "docs/plans" {
		t.Errorf("PlansDir = %q, want %q", cfg.SDD.PlansDir, "docs/plans")
	}
}

func TestScratchpadConfigDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Scratchpad.MaxEntries != 32 {
		t.Errorf("MaxEntries default = %d, want 32", cfg.Scratchpad.MaxEntries)
	}
	if cfg.Scratchpad.MaxTotalTokens != 8000 {
		t.Errorf("MaxTotalTokens default = %d, want 8000", cfg.Scratchpad.MaxTotalTokens)
	}
	if cfg.Scratchpad.MaxEntryTokens != 4000 {
		t.Errorf("MaxEntryTokens default = %d, want 4000", cfg.Scratchpad.MaxEntryTokens)
	}
	if cfg.Scratchpad.ProjectionMaxTokens != 1000 {
		t.Errorf("ProjectionMaxTokens default = %d, want 1000", cfg.Scratchpad.ProjectionMaxTokens)
	}
}

func TestScratchpadConfigMergesFromFile(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[scratchpad]
max_entries = 16
max_total_tokens = 4000
max_entry_tokens = 2000
projection_max_tokens = 500
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Scratchpad.MaxEntries != 16 {
		t.Errorf("MaxEntries = %d, want 16", cfg.Scratchpad.MaxEntries)
	}
	if cfg.Scratchpad.MaxTotalTokens != 4000 {
		t.Errorf("MaxTotalTokens = %d, want 4000", cfg.Scratchpad.MaxTotalTokens)
	}
	if cfg.Scratchpad.MaxEntryTokens != 2000 {
		t.Errorf("MaxEntryTokens = %d, want 2000", cfg.Scratchpad.MaxEntryTokens)
	}
	if cfg.Scratchpad.ProjectionMaxTokens != 500 {
		t.Errorf("ProjectionMaxTokens = %d, want 500", cfg.Scratchpad.ProjectionMaxTokens)
	}
}

func TestScratchpadApplyDefaultsPreservesExplicitValues(t *testing.T) {
	cfg := ScratchpadConfig{
		MaxEntries:          64,
		MaxTotalTokens:      16000,
		MaxEntryTokens:      8000,
		ProjectionMaxTokens: 2000,
	}
	cfg.ApplyDefaults()
	if cfg.MaxEntries != 64 {
		t.Errorf("MaxEntries = %d, want 64", cfg.MaxEntries)
	}
	if cfg.MaxTotalTokens != 16000 {
		t.Errorf("MaxTotalTokens = %d, want 16000", cfg.MaxTotalTokens)
	}
	if cfg.MaxEntryTokens != 8000 {
		t.Errorf("MaxEntryTokens = %d, want 8000", cfg.MaxEntryTokens)
	}
	if cfg.ProjectionMaxTokens != 2000 {
		t.Errorf("ProjectionMaxTokens = %d, want 2000", cfg.ProjectionMaxTokens)
	}
}

func TestScratchpadApplyDefaultsFillsZeroValues(t *testing.T) {
	cfg := ScratchpadConfig{}
	cfg.ApplyDefaults()
	if cfg.MaxEntries != 32 {
		t.Errorf("MaxEntries = %d, want 32", cfg.MaxEntries)
	}
	if cfg.MaxTotalTokens != 8000 {
		t.Errorf("MaxTotalTokens = %d, want 8000", cfg.MaxTotalTokens)
	}
	if cfg.MaxEntryTokens != 4000 {
		t.Errorf("MaxEntryTokens = %d, want 4000", cfg.MaxEntryTokens)
	}
	if cfg.ProjectionMaxTokens != 1000 {
		t.Errorf("ProjectionMaxTokens = %d, want 1000", cfg.ProjectionMaxTokens)
	}
}

func TestScratchpadApplyDefaultsReplacesNegativeValues(t *testing.T) {
	cfg := ScratchpadConfig{
		MaxEntries:          -1,
		MaxTotalTokens:      -1,
		MaxEntryTokens:      -1,
		ProjectionMaxTokens: -1,
	}
	cfg.ApplyDefaults()
	if cfg.MaxEntries != 32 {
		t.Errorf("MaxEntries = %d, want 32", cfg.MaxEntries)
	}
	if cfg.MaxTotalTokens != 8000 {
		t.Errorf("MaxTotalTokens = %d, want 8000", cfg.MaxTotalTokens)
	}
	if cfg.MaxEntryTokens != 4000 {
		t.Errorf("MaxEntryTokens = %d, want 4000", cfg.MaxEntryTokens)
	}
	if cfg.ProjectionMaxTokens != 1000 {
		t.Errorf("ProjectionMaxTokens = %d, want 1000", cfg.ProjectionMaxTokens)
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
guardrail_dynamic_argv0 = "off"
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
	if !reflect.DeepEqual(s.Allow.Commands, []string{"go test", "git status"}) {
		t.Errorf("Allow.Commands = %#v", s.Allow.Commands)
	}
	if !reflect.DeepEqual(s.Confirm.Commands, []string{"yarn install"}) {
		t.Errorf("Confirm.Commands = %#v", s.Confirm.Commands)
	}
	if !reflect.DeepEqual(s.Deny.Patterns, []string{"rm -rf"}) {
		t.Errorf("Deny.Patterns = %#v", s.Deny.Patterns)
	}
	if s.GuardrailDynamicArgv0 != "off" {
		t.Fatalf("GuardrailDynamicArgv0 not merged, want off, got %q", s.GuardrailDynamicArgv0)
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
	// Memory/CPU/file-size stay opt-in (a cap that kills a legitimate build
	// trains users to disable the sandbox), but the process cap is on by
	// default: with every limit unset the restricted backend emitted no
	// ulimit at all, leaving a fork bomb unguarded.
	if sb.MaxProcesses != 2048 {
		t.Fatalf("default max processes = %d, want 2048", sb.MaxProcesses)
	}
	if sb.CPUSeconds != 0 {
		t.Fatalf("default cpu seconds = %d, want 0 (opt-in)", sb.CPUSeconds)
	}
	if sb.FileSizeLimitMB != 0 {
		t.Fatalf("default file size limit = %d, want 0 (opt-in)", sb.FileSizeLimitMB)
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
	if cfg.Tools.Shell.GuardrailDynamicArgv0 != "deny" {
		t.Fatalf("default guardrail_dynamic_argv0 = %q, want deny", cfg.Tools.Shell.GuardrailDynamicArgv0)
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
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	preset := cfg.Models.Presets["coder"]
	if preset.Provider != "ollama" || preset.Model != "qwen2.5-coder:14b" || !preset.LocalOnly {
		t.Fatalf("preset coder = %#v", preset)
	}
	if preset.ContextWindow != 32768 || preset.MaxOutputTokens != 4096 {
		t.Fatalf("preset numeric fields = %#v", preset)
	}
	profile := cfg.AgentProfiles["local_balanced"]
	if profile.Roles[routing.RoleRepoScout].Preset != "coder" || profile.Roles[routing.RoleImplementer].Preset != "coder" {
		t.Fatalf("profile roles = %#v", profile.Roles)
	}
	budget := cfg.Agents[routing.RoleImplementer].Context
	if budget.MaxRepoContextTokens != 48000 {
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
	if cfg.AgentProfiles["local_balanced"].Roles[routing.RoleRepoScout].Preset != "fast" {
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

func TestMCPConfigParsesServerTrust(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[mcp.servers.local]
command = "node"
trust = "unrestricted"
`)
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	srv, ok := cfg.MCP.Servers["local"]
	if !ok {
		t.Fatal("local server config missing")
	}
	if srv.Trust != "unrestricted" {
		t.Errorf("trust = %q, want unrestricted (was silently discarded)", srv.Trust)
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

func TestDefaultConfigHasBackgroundJobDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Tools.Shell.MaxBackgroundJobs != 25 {
		t.Fatalf("MaxBackgroundJobs = %d, want 25", cfg.Tools.Shell.MaxBackgroundJobs)
	}
	if cfg.Tools.Shell.BackgroundRetention != 8*time.Hour {
		t.Fatalf("BackgroundRetention = %s, want 8h", cfg.Tools.Shell.BackgroundRetention)
	}
}

func TestLoadParsesBackgroundJobConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, work+"/.marshal/config.toml", `
[tools.shell]
max_background_jobs = 10
background_retention = "30m"
`)

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Tools.Shell.MaxBackgroundJobs != 10 {
		t.Fatalf("MaxBackgroundJobs = %d, want 10", cfg.Tools.Shell.MaxBackgroundJobs)
	}
	if cfg.Tools.Shell.BackgroundRetention != 30*time.Minute {
		t.Fatalf("BackgroundRetention = %s, want 30m", cfg.Tools.Shell.BackgroundRetention)
	}
}

func TestLoadHooksConfig(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, ".marshal", "config.toml"), `
[hooks]
fail_closed = true

[[hooks.entries]]
event = "pre_tool_use"
matcher = "file.write_patch"
command = "./scripts/check-patch.sh"
timeout_ms = 2500
`)

	cfg, err := Load(LoadOptions{
		WorkingDir:    tmp,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Hooks.FailClosed {
		t.Fatal("Hooks.FailClosed = false, want true")
	}
	if len(cfg.Hooks.Entries) != 1 {
		t.Fatalf("len(Hooks.Entries) = %d, want 1", len(cfg.Hooks.Entries))
	}
	entry := cfg.Hooks.Entries[0]
	if entry.Event != "pre_tool_use" || entry.Matcher != "file.write_patch" || entry.Command != "./scripts/check-patch.sh" || entry.TimeoutMS != 2500 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestTUITomlRoundTrip(t *testing.T) {
	var cfg Config
	err := toml.Unmarshal([]byte(`
[tui]
theme = "dracula"
palette = { accent_primary = "#ff00ff" }
`), &cfg)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.TUI.Theme != "dracula" {
		t.Fatalf("Theme = %q, want dracula", cfg.TUI.Theme)
	}
	if cfg.TUI.Palette["accent_primary"] != "#ff00ff" {
		t.Fatalf("Palette[accent_primary] = %q, want #ff00ff", cfg.TUI.Palette["accent_primary"])
	}
}

func TestTUIModeRoundTrip(t *testing.T) {
	var cfg Config
	err := toml.Unmarshal([]byte(`
[tui]
mode = "light"
`), &cfg)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.TUI.Mode != "light" {
		t.Fatalf("Mode = %q, want light", cfg.TUI.Mode)
	}
}

func TestTUIDefaultsAreEmpty(t *testing.T) {
	cfg := Default()
	if cfg.TUI.Theme != "" {
		t.Fatalf("Default().TUI.Theme = %q, want empty", cfg.TUI.Theme)
	}
	if cfg.TUI.Palette != nil {
		t.Fatalf("Default().TUI.Palette = %#v, want nil", cfg.TUI.Palette)
	}
}

func TestLoadReportsTrustedDecision(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, ".marshal", "config.toml"), `[project]
name = "trusted"
`)
	trusted := false
	_, err := Load(LoadOptions{
		WorkingDir:    tmp,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustSession},
		Trusted:       &trusted,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !trusted {
		t.Fatal("Trusted = false, want true for session trust")
	}
}

// TestLoadReturnsUserConfigMergeError: a bad value in the *user* config
// must surface with the file path, not be silently swallowed (previously
// merge's error was ignored at config.go:656, dropping the rest of the
// user's settings without a word).
func TestLoadReturnsUserConfigMergeError(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", `
[tools.shell]
background_retention = "banana"
`)

	_, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err == nil {
		t.Fatal("Load succeeded with an invalid user config, want error")
	}
	if !strings.Contains(err.Error(), "background_retention") {
		t.Fatalf("error = %v, want it to name the bad field", err)
	}
	if !strings.Contains(err.Error(), ".config/marshal/config.toml") {
		t.Fatalf("error = %v, want it to name the user config path", err)
	}
}

// TestAgentProfilesDecodeRoleKeys verifies that agent_profiles decode
// arbitrary AgentRole keys directly, without a fixed struct. A role
// added after the old 12-field struct (e.g. sdd_branch_reviewer) must
// still decode correctly.
func TestApprovalModeMerge(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeFile(t, home+"/.config/marshal/config.toml", `
[agent]
approval_mode = "edit"
`)
	writeFile(t, work+"/.marshal/config.toml", `
[agent]
approval_mode = "copilot"
`)
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.ApprovalMode != "copilot" {
		t.Fatalf("ApprovalMode = %q, want %q (project wins)", cfg.Agent.ApprovalMode, "copilot")
	}
}

func TestApprovalModeDefault(t *testing.T) {
	cfg := Default()
	if cfg.Agent.ApprovalMode != "default" {
		t.Fatalf("default ApprovalMode = %q, want %q", cfg.Agent.ApprovalMode, "default")
	}
}

func TestAgentProfilesDecodeRoleKeys(t *testing.T) {
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[agent_profiles.mine]
router = "fast"
sdd_branch_reviewer = "big"
`)
	cfg, err := Load(LoadOptions{HomeDir: work, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.AgentProfiles["mine"]
	if p.Roles[routing.RoleRouter].Preset != "fast" {
		t.Errorf("router role = %+v", p.Roles[routing.RoleRouter])
	}
	if p.Roles[routing.RoleSDDBranchReviewer].Preset != "big" {
		t.Errorf("sdd_branch_reviewer role = %+v", p.Roles[routing.RoleSDDBranchReviewer])
	}
}

func TestSDDConfigProductionFieldsMerge(t *testing.T) {
	cfg := Default()
	if cfg.SDD.VerifyTimeoutMS != 300000 {
		t.Fatalf("default VerifyTimeoutMS = %d, want 300000", cfg.SDD.VerifyTimeoutMS)
	}
	if !cfg.SDD.CleanupAtStart {
		t.Fatal("default CleanupAtStart should be true")
	}

	// Project-local TOML overrides via the established Load path.
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[sdd]
verify_timeout_ms = 60000
cleanup_at_start = false
max_total_tokens = 1000000
`)
	merged, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if merged.SDD.VerifyTimeoutMS != 60000 {
		t.Fatalf("VerifyTimeoutMS = %d", merged.SDD.VerifyTimeoutMS)
	}
	if merged.SDD.CleanupAtStart {
		t.Fatal("CleanupAtStart should be false after override")
	}
	if merged.SDD.MaxTotalTokens != 1000000 {
		t.Fatalf("MaxTotalTokens = %d", merged.SDD.MaxTotalTokens)
	}
}

func TestSDDVerifyCommandsMerge(t *testing.T) {
	cfg := Default()
	if cfg.SDD.Verify.Build != "" || cfg.SDD.Verify.Test != "" {
		t.Fatalf("defaults ship verify commands: %+v", cfg.SDD.Verify)
	}
	build := "make build"
	test := "make test"
	file := configFile{SDD: &fileSDD{Verify: &fileSDDVerify{Build: &build, Test: &test}}}
	merge(&cfg, file)
	if cfg.SDD.Verify.Build != build || cfg.SDD.Verify.Test != test {
		t.Errorf("verify = %+v, want the file values", cfg.SDD.Verify)
	}
}

func TestDefaultSidePanel(t *testing.T) {
	sp := Default().TUI.SidePanel
	if !sp.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if sp.MinWidth != 120 {
		t.Errorf("MinWidth = %d, want 120", sp.MinWidth)
	}
	if sp.WidthPct != 25 {
		t.Errorf("WidthPct = %d, want 25", sp.WidthPct)
	}
	if sp.MinCols != 30 {
		t.Errorf("MinCols = %d, want 30", sp.MinCols)
	}
	if sp.MaxCols != 60 {
		t.Errorf("MaxCols = %d, want 60", sp.MaxCols)
	}
	if len(sp.Hidden) != 0 {
		t.Errorf("Hidden = %v, want empty", sp.Hidden)
	}
}

func TestMergeSidePanel(t *testing.T) {
	cfg := Default()
	pct := 30
	enabled := false
	if err := merge(&cfg, configFile{TUI: &fileTUI{
		SidePanel: &fileSidePanel{Enabled: &enabled, WidthPct: &pct, Hidden: []string{"repo"}},
	}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if cfg.TUI.SidePanel.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	if cfg.TUI.SidePanel.WidthPct != 30 {
		t.Errorf("WidthPct = %d, want 30", cfg.TUI.SidePanel.WidthPct)
	}
	if cfg.TUI.SidePanel.MinWidth != 120 {
		t.Errorf("MinWidth = %d, want 120 (unset fields keep defaults)", cfg.TUI.SidePanel.MinWidth)
	}
	if len(cfg.TUI.SidePanel.Hidden) != 1 || cfg.TUI.SidePanel.Hidden[0] != "repo" {
		t.Errorf("Hidden = %v, want [repo]", cfg.TUI.SidePanel.Hidden)
	}
}

func TestRemoteLimitDiscoveryDefaultsToRemoteProvidersAllowed(t *testing.T) {
	cfg := Default()
	if cfg.Privacy.RemoteLimitDiscovery != cfg.Privacy.RemoteProvidersAllowed {
		t.Errorf("RemoteLimitDiscovery = %v, want %v", cfg.Privacy.RemoteLimitDiscovery, cfg.Privacy.RemoteProvidersAllowed)
	}
}

func TestHistoryDefaults(t *testing.T) {
	cfg := Default()
	if !cfg.History.Enabled {
		t.Errorf("History.Enabled default = false, want true")
	}
}

func TestHistoryMergesFromFile(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", `
[history]
enabled = false
`)
	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.History.Enabled {
		t.Errorf("History.Enabled = true after file override, want false")
	}
}

func TestLoadSkipProjectConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, work+"/.marshal/config.toml", "[project]\nname = \"skipped\"\n")

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work, SkipProjectConfig: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project.Name == "skipped" {
		t.Fatal("project config was applied despite SkipProjectConfig")
	}

	trusted := true
	cfg, err = Load(LoadOptions{HomeDir: home, WorkingDir: work, SkipProjectConfig: true, Trusted: &trusted})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if trusted {
		t.Fatal("Trusted should be reported false when the project config is skipped")
	}

	// Sanity: without the flag, existing behavior is unchanged.
	cfg, err = Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project.Name != "skipped" {
		t.Fatal("nil-resolver default must still apply project config (unchanged)")
	}
}

// skills.autoload has to survive the file layer in both directions: merge
// (so the config is honoured at startup) and writeSections (so editing the
// list in /settings actually persists).
func TestSkillsAutoloadMergesFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[skills]\nautoload = ['using-superpowers', 'brainstorming']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	file, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := merge(&cfg, file); err != nil {
		t.Fatal(err)
	}
	want := []string{"using-superpowers", "brainstorming"}
	if !reflect.DeepEqual(cfg.Skills.Autoload, want) {
		t.Fatalf("Autoload = %v, want %v", cfg.Skills.Autoload, want)
	}
}

func TestSkillsAutoloadRoundTripsThroughSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Default()
	cfg.Skills.Autoload = []string{"using-superpowers"}
	if err := SaveProjectConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded := Default()
	file, err := loadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := merge(&reloaded, file); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Skills.Autoload, cfg.Skills.Autoload) {
		t.Fatalf("Autoload = %v, want %v", reloaded.Skills.Autoload, cfg.Skills.Autoload)
	}
}

// The default must stay empty: nothing autoloads unless asked for.
func TestSkillsAutoloadDefaultsEmpty(t *testing.T) {
	if got := Default().Skills.Autoload; len(got) != 0 {
		t.Fatalf("default Autoload = %v, want empty", got)
	}
}
