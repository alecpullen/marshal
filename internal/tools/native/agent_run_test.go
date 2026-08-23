package native

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// TestAgentRunToolDrafting verifies the agent.run tool returned by
// NewSubagentTool (via the agent package) wires together the registry, the
// session state's depth/concurrency counters, and the factory. The
// factory is stubbed so no real provider is required.
func TestAgentRunToolRegistersAndDispatchesToChild(t *testing.T) {
	reg := registry.New()
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})

	var (
		gotPrompt string
		gotChild  *agent.Runner
		calls     int
	)
	factory := func(_ agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		calls++
		return &agent.Runner{State: state}, state, nil
	}
	exec := func(ctx context.Context, child *agent.Runner, prompt string) (string, string, error) {
		gotChild = child
		gotPrompt = prompt
		return "child summary text", "", nil
	}

	tool := agent.NewSubagentTool(factory, nil, reg, state, agent.WithSubagentExec(exec))

	if err := reg.Register(tool); err != nil {
		t.Fatalf("register agent.run: %v", err)
	}
	invoked, ok := reg.Lookup("agent.run")
	if !ok {
		t.Fatal("agent.run missing from registry after Register")
	}

	res, err := invoked.Handler(context.Background(), registry.ToolCall{
		Name: "agent.run",
		Args: []byte(`{"prompt":"find the parse function","description":"parse lookup"}`),
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("factory calls = %d, want 1", calls)
	}
	if gotChild == nil {
		t.Fatal("exec did not receive child runner")
	}
	if gotPrompt != "find the parse function" {
		t.Fatalf("prompt = %q, want %q", gotPrompt, "find the parse function")
	}
	if !strings.HasPrefix(res.Summary, "subagent completed: parse lookup") {
		t.Fatalf("summary = %q, want subagent-completed prefix", res.Summary)
	}
	if res.Content != "child summary text" {
		t.Fatalf("content = %q, want %q", res.Content, "child summary text")
	}
	// Counters must be back to zero after a successful invocation.
	if got := state.SubagentDepth(); got != 0 {
		t.Fatalf("depth after = %d, want 0", got)
	}
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency after = %d, want 0", got)
	}
}

func TestAgentRunToolRejectsWhenDepthLimitReached(t *testing.T) {
	reg := registry.New()
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(2))

	factoryCalls := 0
	factory := func(_ agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		factoryCalls++
		return &agent.Runner{}, nil, nil
	}
	exec := func(ctx context.Context, child *agent.Runner, prompt string) (string, string, error) {
		t.Fatal("exec must not be called when the depth guard rejects")
		return "", "", nil
	}

	tool := agent.NewSubagentTool(factory, nil, reg, state, agent.WithSubagentExec(exec))
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "agent.run",
		Args: []byte(`{"prompt":"x","description":"y"}`),
	})
	if err == nil {
		t.Fatal("expected depth-limit error")
	}
	if !errors.Is(err, session.ErrSubagentDepthLimit) {
		t.Fatalf("err = %v, want ErrSubagentDepthLimit", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls = %d, want 0", factoryCalls)
	}
}

func TestAgentRunToolRejectsWhenConcurrencyLimitReached(t *testing.T) {
	reg := registry.New()
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithSubagentMaxConcurrency(2))
	state.SetSubagentConcurrency(2)

	factory := func(_ agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		return &agent.Runner{}, nil, nil
	}
	tool := agent.NewSubagentTool(factory, nil, reg, state)
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		Name: "agent.run",
		Args: []byte(`{"prompt":"x","description":"y"}`),
	})
	if err == nil {
		t.Fatal("expected concurrency-limit error")
	}
	if !errors.Is(err, session.ErrSubagentConcurrencyLimit) {
		t.Fatalf("err = %v, want ErrSubagentConcurrencyLimit", err)
	}
}

func TestAgentRunToolRejectsMissingArgs(t *testing.T) {
	reg := registry.New()
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := agent.NewSubagentTool(func(_ agent.SubagentRequest) (*agent.Runner, *session.State, error) { return &agent.Runner{}, nil, nil }, nil, reg, state)

	cases := []struct {
		name string
		args string
		want string
	}{
		{"empty prompt", `{"prompt":"","description":"y"}`, "prompt"},
		{"empty description", `{"prompt":"x","description":""}`, "description"},
		{"invalid json", `{not json`, "decode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), registry.ToolCall{Name: "agent.run", Args: []byte(c.args)})
			if err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want substring %q", err, c.want)
			}
		})
	}
}
