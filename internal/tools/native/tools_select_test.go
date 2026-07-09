package native

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestToolsSelectLoadsDeferredTools(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{Name: "mcp.foo", Description: "foo", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Deferred: true, Handler: stubHandler}); err != nil {
		t.Fatalf("Register mcp.foo: %v", err)
	}
	if err := reg.Register(registry.Tool{Name: "file.read", Description: "read", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubHandler}); err != nil {
		t.Fatalf("Register file.read: %v", err)
	}

	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{registry: reg, sessionState: state}
	tool := tools.toolsSelectTool()

	args, _ := json.Marshal(map[string]any{"names": []string{"mcp.foo"}})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.Summary != "tools selected" {
		t.Fatalf("summary = %q, want %q", res.Summary, "tools selected")
	}
	loaded := state.LoadedToolNames()
	if len(loaded) != 1 || loaded[0] != "mcp.foo" {
		t.Fatalf("loaded = %v, want [mcp.foo]", loaded)
	}
}

func TestToolsSelectRejectsUnknownOrNonDeferred(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{Name: "mcp.foo", Description: "foo", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Deferred: true, Handler: stubHandler}); err != nil {
		t.Fatalf("Register mcp.foo: %v", err)
	}
	if err := reg.Register(registry.Tool{Name: "file.read", Description: "read", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubHandler}); err != nil {
		t.Fatalf("Register file.read: %v", err)
	}

	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{registry: reg, sessionState: state}
	tool := tools.toolsSelectTool()

	args, _ := json.Marshal(map[string]any{"names": []string{"file.read"}})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for non-deferred tool")
	}
	if loaded := state.LoadedToolNames(); len(loaded) != 0 {
		t.Fatalf("loaded = %v, want none", loaded)
	}
}

func stubHandler(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
	return registry.ToolResult{}, nil
}
