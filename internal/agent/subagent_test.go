package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// TestSubagentDepthLimit verifies that a session whose own nesting depth
// has already hit the cap is rejected by the depth guard before any child
// runner is constructed. We construct a depth-2 session directly (the
// factory never gets a chance to "build" it into a parent).
func TestSubagentDepthLimit(t *testing.T) {
	called := false
	factory := func(_ string) (*Runner, *session.State, error) {
		called = true
		return &Runner{}, nil, nil
	}
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(2))

	tool := NewSubagentTool(factory, registry.New(), state)
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

// TestSubagentConcurrencyLimit exercises the concurrency guard end-to-end:
// a parent at depth=0 admits two in-flight subagents, then rejects a third
// with ErrSubagentConcurrencyLimit (not ErrSubagentDepthLimit). We spawn
// the first two via direct EnterSubagent/ExitSubagent calls in goroutines
// blocked on a release channel, then attempt a third admission via the
// normal agent.run handler.
func TestSubagentConcurrencyLimit(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	if got := state.SubagentDepth(); got != 0 {
		t.Fatalf("initial depth = %d, want 0", got)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			if err := state.EnterSubagent(); err != nil {
				t.Errorf("first/second EnterSubagent returned %v, want nil", err)
				return
			}
			defer state.ExitSubagent()
			entered <- struct{}{}
			<-release
		}()
	}

	<-entered
	<-entered
	if got := state.SubagentConcurrency(); got != 2 {
		t.Fatalf("concurrency after 2 admissions = %d, want 2", got)
	}

	factoryCalls := 0
	factory := func(_ string) (*Runner, *session.State, error) {
		factoryCalls++
		return &Runner{}, nil, nil
	}
	tool := NewSubagentTool(factory, registry.New(), state)
	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err == nil {
		t.Fatal("expected concurrency-limit error, got nil")
	}
	if !errors.Is(err, session.ErrSubagentConcurrencyLimit) {
		t.Fatalf("error = %v, want session.ErrSubagentConcurrencyLimit", err)
	}
	if errors.Is(err, session.ErrSubagentDepthLimit) {
		t.Fatalf("error = %v leaked depth-limit sentinel inside a concurrency-limit test", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory invocations = %d, want 0", factoryCalls)
	}

	close(release)
	wg.Wait()
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency after release = %d, want 0", got)
	}
}

func TestSubagentGuardCountersRoundTrip(t *testing.T) {
	top := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(0))
	if got := top.SubagentDepth(); got != 0 {
		t.Fatalf("top depth = %d, want 0", got)
	}
	if got := top.SubagentConcurrency(); got != 0 {
		t.Fatalf("top concurrency = %d, want 0", got)
	}
	top.SetSubagentConcurrency(1)
	if got := top.SubagentConcurrency(); got != 1 {
		t.Fatalf("after SetSubagentConcurrency(1) = %d, want 1", got)
	}

	child := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(1))
	if got := child.SubagentDepth(); got != 1 {
		t.Fatalf("child depth = %d, want 1", got)
	}
	if got := child.SubagentConcurrency(); got != 0 {
		t.Fatalf("child concurrency = %d, want 0", got)
	}

	if err := child.EnterSubagent(); !errors.Is(err, session.ErrSubagentDepthLimit) {
		t.Fatalf("child EnterSubagent = %v, want ErrSubagentDepthLimit (depth guard, not concurrency)", err)
	}
	if got := child.SubagentConcurrency(); got != 0 {
		t.Fatalf("child concurrency after rejected EnterSubagent = %d, want 0", got)
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
	mustReg(&registry.Tool{Name: "question.ask", Description: "ask", Schema: []byte(`{}`), Risk: registry.RiskReadOnly, Handler: stubAgentRunHandler})

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
	if !names["shell.run"] {
		t.Fatal("subtask view missing shell.run (implementation tools must be visible to child)")
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
	// A subtask runs in its own orphaned child session.State that no ACP
	// client (or the TUI) ever sees — there is no live user who could
	// possibly answer a question.ask call. The prompt already tells the
	// model "you cannot prompt the user"; the registry must actually
	// enforce it, or the call would block forever waiting for an answer
	// that structurally cannot arrive.
	if names["question.ask"] {
		t.Fatal("subtask view must NOT contain question.ask (no user to answer it)")
	}
	if _, ok := view.Lookup("question.ask"); ok {
		t.Fatal("Lookup(question.ask) must fail in subtask view")
	}
}

func TestNewSubagentToolAgentArgResolves(t *testing.T) {
	called := ""
	factory := func(agentName string) (*Runner, *session.State, error) {
		called = agentName
		r := &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}
		return r, nil, nil
	}
	tool := NewSubagentTool(factory, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d","agent":"my-scout"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if called != "my-scout" {
		t.Fatalf("factory called with %q, want my-scout", called)
	}
	if !strings.Contains(res.Summary, "subagent completed") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

func TestNewSubagentToolNoAgentArgStillWorks(t *testing.T) {
	factory := func(agentName string) (*Runner, *session.State, error) {
		if agentName != "" {
			t.Fatalf("factory called with %q, want empty", agentName)
		}
		return &Runner{RunTaskFunc: func(context.Context, string) (*Task, error) {
			return &Task{Summary: "ok"}, nil
		}}, nil, nil
	}
	tool := NewSubagentTool(factory, registry.New(), session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"prompt":"do it","description":"d"}`),
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}
}

func stubAgentRunHandler(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) {
	return registry.ToolResult{Summary: "stub"}, nil
}
