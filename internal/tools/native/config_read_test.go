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
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {BaseURL: "https://api.openai.com", APIKey: "sk-secret"},
	}

	reg := registry.New()
	tools := toolSet{config: cfg}
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
	if !strings.Contains(res.Content, "api.openai.com") {
		t.Fatalf("expected provider base URL in output, got: %s", res.Content)
	}
}

func TestConfigReadFiltersSections(t *testing.T) {
	cfg := config.Default()
	tools := toolSet{config: cfg}
	res, err := tools.configReadTool().Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.read", Args: json.RawMessage(`{"sections":["Agent"]}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, `"Agent"`) || strings.Contains(res.Content, `"Providers"`) {
		t.Fatalf("filtered output must contain only Agent: %s", res.Content)
	}
}
