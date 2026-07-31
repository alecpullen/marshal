package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// TestAgentRunIsWriteRisk verifies the safety invariant from B-14:
// agent.run must not be RiskReadOnly because its child runner can execute
// write tools and shell commands.
func TestAgentRunIsWriteRisk(t *testing.T) {
	factory := func(_ string) (*Runner, error) {
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil
	}
	tool := NewSubagentTool(factory, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if tool.Risk != registry.RiskWorkspaceWrite {
		t.Fatalf("agent.run Risk = %q, want %q", tool.Risk, registry.RiskWorkspaceWrite)
	}
}

// TestAgentRunRejectedFromParallelReadOnlyBatch verifies that a model action
// batch containing agent.run is not admitted to the parallel read-only path,
// because the subagent may perform writes.
func TestAgentRunRejectedFromParallelReadOnlyBatch(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name:        "agent.run",
		Description: "delegate",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler:     func(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) { return registry.ToolResult{}, nil },
	}); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}
	if err := reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "read",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Risk:        registry.RiskReadOnly,
		Handler:     func(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) { return registry.ToolResult{}, nil },
	}); err != nil {
		t.Fatalf("register file.read: %v", err)
	}

	r := NewRunner(nil, reg, nil, state, "test-model")
	err := r.allReadOnly([]ModelAction{
		{Type: ActionToolCall, Tool: "file.read", Args: json.RawMessage(`{})`)},
		{Type: ActionToolCall, Tool: "agent.run", Args: json.RawMessage(`{"prompt":"x","description":"y"}`)},
	})
	if err == nil {
		t.Fatal("expected allReadOnly to reject agent.run")
	}
}
