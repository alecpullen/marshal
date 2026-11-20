package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// TestExplicitIsolatedDispatchRefusesWithoutAService pins the fail-closed half
// of the dispatch contract on a build that wires no workspace service. The
// omitted default keeps the pre-cutover binding such builds have always used,
// but an explicit request for isolation is refused rather than quietly run in
// the parent's checkout: a shared run that reported success would misreport
// where the work happened.
func TestExplicitIsolatedDispatchRefusesWithoutAService(t *testing.T) {
	repo := newDispatchRepo(t)
	state, _ := newDispatchState(t, "unwired-owner", repo, config.Default(), false)
	var built atomic.Int32
	tool := NewSubagentTool(func(SubagentRequest) (*Runner, *session.State, error) {
		built.Add(1)
		return &Runner{}, nil, nil
	}, nil, registry.New(), state,
		WithSubagentExec(func(context.Context, *Runner, string) (string, string, error) {
			return "done", "", nil
		}),
	)

	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID: "call", Name: "agent.run",
		Args: json.RawMessage(`{"prompt":"x","description":"isolated","workspace":"isolated"}`),
	})
	if err == nil {
		t.Fatal("an explicit isolated dispatch succeeded with no workspace service wired")
	}
	if !strings.Contains(err.Error(), "isolated workspaces are unavailable") {
		t.Fatalf("error = %v, want the actionable isolated-unavailable refusal", err)
	}
	if calls := built.Load(); calls != 0 {
		t.Fatalf("factory invocations = %d, want the refusal to build no child", calls)
	}
	if subs := state.Subagents(); len(subs) != 0 {
		t.Fatalf("subagents = %d, want the refusal to register none", len(subs))
	}
}
