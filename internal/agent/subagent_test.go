package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestSubagentDepthLimit(t *testing.T) {
	called := false
	factory := func() (*Runner, error) {
		called = true
		return &Runner{}, nil
	}
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	state.SetSubagentDepth(1)

	tool := NewSubagentTool(factory, registry.New(), state, 2)
	if tool.Name != "agent.run" {
		t.Fatalf("Name = %q, want %q", tool.Name, "agent.run")
	}

	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err == nil {
		t.Fatal("expected depth-limit error, got nil")
	}
	if !errors.Is(err, session.ErrSubagentDepthLimit) {
		t.Fatalf("error = %v, want session.ErrSubagentDepthLimit", err)
	}
	if called {
		t.Fatal("factory must not be invoked when depth guard rejects")
	}
}

func TestSubagentConcurrencyLimit(t *testing.T) {
	called := 0
	factory := func() (*Runner, error) {
		called++
		return &Runner{}, nil
	}
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	state.SetSubagentConcurrency(2)

	tool := NewSubagentTool(factory, registry.New(), state, 2)
	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err == nil {
		t.Fatal("expected concurrency-limit error, got nil")
	}
	if !errors.Is(err, session.ErrSubagentConcurrencyLimit) {
		t.Fatalf("error = %v, want session.ErrSubagentConcurrencyLimit", err)
	}
	if called != 0 {
		t.Fatalf("factory invocations = %d, want 0", called)
	}
}

func TestSubagentGuardCountersRoundTrip(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	if got := state.SubagentDepth(); got != 0 {
		t.Fatalf("initial depth = %d, want 0", got)
	}
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("initial concurrency = %d, want 0", got)
	}
	state.SetSubagentDepth(1)
	state.SetSubagentConcurrency(1)
	if got := state.SubagentDepth(); got != 1 {
		t.Fatalf("after set depth = %d, want 1", got)
	}
	if got := state.SubagentConcurrency(); got != 1 {
		t.Fatalf("after set concurrency = %d, want 1", got)
	}
}

func TestSubtaskScopeViewFiltersTools(t *testing.T) {
	reg := registry.New()
	mustReg := func(tool *registry.Tool) {
		t.Helper()
		if err := reg.Register(*tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	mustReg(&registry.Tool{Name: "file.read", Description: "read", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "agent.run", Description: "delegate", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "web.fetch", Description: "fetch", Schema: []byte(`{}`), Risk: registry.RiskNetwork, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "shell.run", Description: "shell", Schema: []byte(`{}`), Risk: registry.RiskCommand, Handler: stubAgentRunHandler})
	mustReg(&registry.Tool{Name: "diagnostics.check", Description: "diag", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Deferred: true, Handler: stubAgentRunHandler})

	view := SubtaskScopeView(reg)
	names := make(map[string]bool, len(view.List()))
	for _, tool := range view.List() {
		names[tool.Name] = true
	}
	if !names["file.read"] {
		t.Fatal("subtask view missing file.read")
	}
	if !names["web.fetch"] {
		t.Fatal("subtask view missing web.fetch (network must be allowed)")
	}
	if names["agent.run"] {
		t.Fatal("subtask view must NOT contain agent.run (no nested subagents)")
	}
	if names["shell.run"] {
		t.Fatal("subtask view must NOT contain shell.run (write/command tools excluded)")
	}
	if names["diagnostics.check"] {
		t.Fatal("subtask view must NOT contain deferred tools (no MCP autoloading)")
		// diagnostics.check is RiskReadOnly AND not Deferred for native, but our test
		// set Deferred=true to keep the rule general; a future deferred read tool
		// stays out of the subtask view.
	}
	if _, ok := view.Lookup("agent.run"); ok {
		t.Fatal("Lookup(agent.run) must fail in subtask view")
	}
}

func stubAgentRunHandler(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) {
	return registry.ToolResult{Summary: "stub"}, nil
}
