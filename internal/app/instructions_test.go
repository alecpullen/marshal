package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestLoadRepoInstructionsPicksFirstPresent(t *testing.T) {
	dir := t.TempDir()
	if _, ok := loadRepoInstructions(dir); ok {
		t.Fatal("expected no instructions in empty dir")
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# later\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# agents wins\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, ok := loadRepoInstructions(dir)
	if !ok {
		t.Fatal("expected instructions after writing AGENTS.md")
	}
	if got != "# agents wins" {
		t.Fatalf("loadRepoInstructions = %q, want %q", got, "# agents wins")
	}
}

func TestLoadRepoInstructionsFallsBackThroughCandidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte("# cursor\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, ok := loadRepoInstructions(dir)
	if !ok {
		t.Fatal("expected instructions from .cursorrules when only it exists")
	}
	if got != "# cursor" {
		t.Fatalf("loadRepoInstructions = %q, want %q", got, "# cursor")
	}
}

func TestLoadRepoInstructionsSkipsBlankFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("   \n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# fallback\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, ok := loadRepoInstructions(dir)
	if !ok {
		t.Fatal("expected fallback to CLAUDE.md when AGENTS.md is blank")
	}
	if got != "# fallback" {
		t.Fatalf("loadRepoInstructions = %q, want %q", got, "# fallback")
	}
}

func TestComposeAddendum(t *testing.T) {
	cases := []struct {
		name string
		repo string
		cust string
		want string
	}{
		{name: "both empty", repo: "", cust: "", want: ""},
		{name: "repo only", repo: "repo rules", cust: "", want: "repo rules"},
		{name: "custom only", repo: "", cust: "be strict", want: "be strict"},
		{
			name: "both present",
			repo: "repo rules",
			cust: "be strict",
			want: "## Repository Instructions\n\nrepo rules\n\n## Agent Instructions\n\nbe strict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := composeAddendum(tc.repo, tc.cust)
			if got != tc.want {
				t.Fatalf("composeAddendum(%q, %q) = %q, want %q", tc.repo, tc.cust, got, tc.want)
			}
		})
	}
}

func TestRoleRunnerLoadsRepoInstructionsFromWorkingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# repo rules\nuse go test"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	resolver := newRoutedProviderResolver(cfg, "")
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	spec := roleRunnerSpec{
		cfg:         cfg,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		pol:         pol,
		state:       session.New(cfg, dir, time.Now(), session.Persistence{}),
	}
	runner, err := spec.newRunner(agent.RoleImplementer, swarm.ScopeReadOnly)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "# repo rules") {
		t.Fatalf("SystemPromptAddendum = %q, want it to contain repo instructions", runner.SystemPromptAddendum)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "use go test") {
		t.Fatalf("SystemPromptAddendum = %q, want it to contain the file body", runner.SystemPromptAddendum)
	}
}

func TestMainRunnerLoadsRepoInstructionsFromWorkingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# repo rules\nuse go test"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := nativeToolAgentConfig("test-provider")
	state := session.New(cfg, dir, time.Now(), session.Persistence{})
	runner, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(context.Background(), cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "# repo rules") {
		t.Fatalf("SystemPromptAddendum = %q, want repo instructions", runner.SystemPromptAddendum)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "use go test") {
		t.Fatalf("SystemPromptAddendum = %q, want file body", runner.SystemPromptAddendum)
	}
}

func TestRoleRunnerComposesRepoInstructionsWithCustomAgent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# repo rules"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleReviewer: {CustomAgent: "strict"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"strict": {Name: "strict", Preset: "fast", SystemPrompt: "be strict"},
	}
	resolver := newRoutedProviderResolver(cfg, "")
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	spec := roleRunnerSpec{
		cfg:         cfg,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		pol:         pol,
		state:       session.New(cfg, dir, time.Now(), session.Persistence{}),
	}
	runner, err := spec.newRunner(agent.RoleReviewer, swarm.ScopeReadOnly)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "# repo rules") {
		t.Fatalf("SystemPromptAddendum = %q, want repo instructions present", runner.SystemPromptAddendum)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "be strict") {
		t.Fatalf("SystemPromptAddendum = %q, want custom agent addendum present", runner.SystemPromptAddendum)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "## Repository Instructions") {
		t.Fatalf("SystemPromptAddendum = %q, want section header", runner.SystemPromptAddendum)
	}
	if !strings.Contains(runner.SystemPromptAddendum, "## Agent Instructions") {
		t.Fatalf("SystemPromptAddendum = %q, want agent instructions header", runner.SystemPromptAddendum)
	}
}
