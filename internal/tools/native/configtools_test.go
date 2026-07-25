package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

func TestConfigReadReturnsMaskedConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {BaseURL: "https://api.openai.com", APIKey: "sk-secret"},
	}

	reg := registry.New()
	tools, err := newConfigToolSet(toolSet{config: cfg})
	if err != nil {
		t.Fatalf("newConfigToolSet: %v", err)
	}
	if err := reg.Register(tools.configReadTool()); err != nil {
		t.Fatalf("register: %v", err)
	}

	tool, ok := reg.Lookup("config.read")
	if !ok {
		t.Fatal("config.read not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.read", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, "\"APIKey\": \"***\"") {
		t.Fatalf("expected masked APIKey in content, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "sk-secret") {
		t.Fatalf("secret leaked into config.read output: %s", res.Content)
	}
	if !strings.Contains(res.Content, "gpt-4o") {
		t.Fatalf("expected model in output, got: %s", res.Content)
	}
}

func TestConfigAgentSetProjectScope(t *testing.T) {
	dir := t.TempDir()
	// SaveProjectConfig writes to the project config path under .marshal/.
	cfgPath := config.ProjectConfigPath(dir)

	cfg := config.Default()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"

	var reloaded *config.Config
	ts := toolSet{
		config:     cfg,
		configPath: cfgPath,
		configReloader: func(c config.Config) error {
			cc := c
			reloaded = &cc
			return nil
		},
	}
	reg := registry.New()
	tools, err := newConfigToolSet(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.configAgentSetTool()); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Lookup("config.agent.set")
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.agent.set",
		Args: json.RawMessage(`{"model":"claude-3.5-sonnet","max_tool_iterations":30}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Summary, "reloaded") {
		t.Fatalf("expected reloaded receipt, got: %s", res.Summary)
	}
	if reloaded == nil || reloaded.Agent.Model != "claude-3.5-sonnet" {
		t.Fatalf("reloader did not see new model: %+v", reloaded)
	}
	if reloaded.Agent.MaxToolIterations != 30 {
		t.Fatalf("max_tool_iterations not applied: %d", reloaded.Agent.MaxToolIterations)
	}
	if reloaded.Agent.Provider != "openai" {
		t.Fatalf("provider should be preserved, got: %s", reloaded.Agent.Provider)
	}
	// File on disk reflects the change.
	loaded, err := config.Load(config.LoadOptions{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent.Model != "claude-3.5-sonnet" {
		t.Fatalf("disk model = %q, want claude-3.5-sonnet", loaded.Agent.Model)
	}
}
