package mcp

import (
	"context"
	"os"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

func TestManagerRegistersAndInvokesTools(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockServerMain()
		return
	}

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"mock": {
			Command: exe,
			Args:    []string{"-test.run=TestManagerRegistersAndInvokesTools"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
		},
	}

	mgr := NewManager(&cfg)
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Close()

	reg := registry.New()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}

	tool, ok := reg.Lookup("mcp.mock.hello")
	if !ok {
		t.Fatal("tool mcp.mock.hello not registered")
	}

	if tool.Description != "says hello" {
		t.Errorf("description = %q, want 'says hello'", tool.Description)
	}

	res, err := tool.Handler(ctx, registry.ToolCall{Name: "mcp.mock.hello", Args: []byte("{}")})
	if err != nil {
		t.Fatalf("Handler error: %v", err)
	}

	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("content = %q, want containing 'hello world'", res.Content)
	}
}
