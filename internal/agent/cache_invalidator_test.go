package agent

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestChildWriteInvalidatesParentToolCache guards the cross-session cache
// seam: a background child's write must drop the PARENT session's cached
// reads, or the parent's next file.read serves pre-write content.
func TestChildWriteInvalidatesParentToolCache(t *testing.T) {
	parentState := newTestState(t)
	parentState.SetTurnToolResult("file.read", []byte(`{"path":"x.go"}`), registry.ToolResult{Summary: "cached", Content: "stale"})

	reg := registry.New()
	writeTool := registry.Tool{
		Name:   "file.write",
		Risk:   registry.RiskWorkspaceWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
	}
	writeTool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "wrote", Content: "ok"}, nil
	}
	if err := reg.Register(writeTool); err != nil {
		t.Fatalf("register: %v", err)
	}

	childRunner := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	childRunner.CacheInvalidator = parentState.ClearToolCache

	if _, err := childRunner.executeToolCall(context.Background(), ModelAction{
		Type: ActionToolCall,
		Tool: "file.write",
		Args: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("executeToolCall: %v", err)
	}

	if _, ok := parentState.GetTurnToolResult("file.read", []byte(`{"path":"x.go"}`)); ok {
		t.Fatal("parent tool cache still holds the stale read after the child write")
	}
}
