package agent

import (
	"context"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/hooks"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestTodoWriteSkipsWorkspacePreflight(t *testing.T) {
	state := gateState(t)
	reg := registry.New()
	if err := reg.Register(native.TodoWriteTool(state)); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	gate := &recordingGate{}
	snap := &fakeWriteSnapshotter{hash: "snapshot"}
	r.WriteGate, r.Snapshotter = gate, snap
	msgs, err := r.executeToolCall(context.Background(), ModelAction{Type: ActionToolCall, Tool: "todo.write", Args: []byte(`{"todos":[{"content":"work","status":"pending"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(state.Todos()) != 1 || len(state.AuditLog()) != 1 {
		t.Fatalf("todo did not complete: msgs=%v todos=%v audit=%v", msgs, state.Todos(), state.AuditLog())
	}
	if snap.calls != 0 || gate.acquisitions != 0 {
		t.Fatalf("session-only write used workspace preflight: snapshots=%d gate=%d", snap.calls, gate.acquisitions)
	}
}

func TestTodoWriteStillHonorsHooksAndInvalidatesCache(t *testing.T) {
	state := gateState(t)
	reg := registry.New()
	if err := reg.Register(native.TodoWriteTool(state)); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	action := ModelAction{Type: ActionToolCall, Tool: "todo.write", Args: []byte(`{"todos":[{"content":"work","status":"pending"}]}`)}
	r.HookRunner = fakeHookRunner{preOut: hooks.Output{Decision: hooks.DecisionBlock, Reason: "blocked"}}
	if _, err := r.executeToolCall(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if len(state.Todos()) != 0 || len(state.AuditLog()) != 1 || state.AuditLog()[0].Approval != registry.ApprovalDenied {
		t.Fatalf("blocked todo mutated state or lost audit: todos=%v audit=%v", state.Todos(), state.AuditLog())
	}
	r.HookRunner = nil
	state.SetTurnToolResult("demo.read", []byte(`{}`), registry.ToolResult{Content: "stale"})
	invalidations := 0
	r.CacheInvalidator = func() { invalidations++ }
	if _, err := r.executeToolCall(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if _, hit := state.GetTurnToolResult("demo.read", []byte(`{}`)); hit || invalidations != 1 {
		t.Fatal("todo write stopped invalidating cached reads")
	}
}
