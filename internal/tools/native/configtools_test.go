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
