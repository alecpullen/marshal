package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

type fakeSteeringState struct {
	queue []string
}

func (f *fakeSteeringState) DrainSteering() []string {
	out := f.queue
	f.queue = nil
	return out
}

func TestRunTaskInjectsSteeringBeforeNextModelCall(t *testing.T) {
	state := newTestState(t)
	steering := &fakeSteeringState{queue: []string{"also update the README"}}

	// First response asks for a read-only tool so the loop iterates and
	// drains steering before the second model call. Second response
	// finalizes the turn.
	p := &scriptedProvider{
		responses: []string{
			`{"rationale":"inspect","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
			`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
		},
	}
	reg := registry.New()
	reg.Register(registry.Tool{
		Name:        "file.read",
		Description: "stub",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Content: "ok", Summary: "ok"}, nil
		},
	})
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.SteeringProvider = steering
	runner.MaxToolIterations = 5

	task, err := runner.RunTask(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	// Steering is injected into the live model context (the request sent
	// to the provider) but is not persisted into the session transcript —
	// it's an ephemeral mid-turn nudge. The second chat call must see it
	// appended after the original user message.
	if len(p.requests) < 2 {
		t.Fatalf("provider saw %d chat calls, want >= 2", len(p.requests))
	}
	var sawSteering bool
	for _, m := range p.requests[1].Messages {
		if m.Role == "user" && strings.Contains(m.Content, "also update the README") {
			sawSteering = true
			break
		}
	}
	if !sawSteering {
		t.Fatalf("steering message never sent to the LLM; second request messages:\n%v", p.requests[1].Messages)
	}
	if task == nil || task.Summary == "" {
		t.Fatal("no task summary")
	}
}

// Sanity check removed; scriptedProvider satisfies provider.Provider
// structurally.
