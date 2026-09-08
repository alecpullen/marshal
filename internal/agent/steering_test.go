package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestRunTaskInjectsSteeringBeforeNextModelCall(t *testing.T) {
	state := newTestState(t)

	// First response asks for a read-only tool so the loop iterates and
	// drains steering before the second model call. Second response
	// finalizes the turn.
	p := &agenttest.ScriptedProvider{
		Responses: []string{
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
	runner.MaxToolIterations = 5
	state.PushSteering("also update the README")

	task, err := runner.RunTask(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	// Steering is injected into the live model context (the request sent
	// to the provider) AND persisted into the session transcript as a
	// RoleUser ContentTypeSteering message — the durable copy that
	// buildHistoryMessages replays across restart. The second chat call
	// must see it appended after the original user message.
	if len(p.Requests) < 2 {
		t.Fatalf("provider saw %d chat calls, want >= 2", len(p.Requests))
	}
	var sawSteering bool
	for _, m := range p.Requests[1].Messages {
		if m.Role == "user" && strings.Contains(m.Content, "also update the README") {
			sawSteering = true
			break
		}
	}
	if !sawSteering {
		t.Fatalf("steering message never sent to the LLM; second request messages:\n%v", p.Requests[1].Messages)
	}
	if task == nil || task.Summary == "" {
		t.Fatal("no task summary")
	}

	// The drained steering message must be persisted with the envelope
	// prefix, so restart replay matches the live wire byte-for-byte.
	var persisted int
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.ContentType == session.ContentTypeSteering {
			persisted++
			if m.Content != "[user steering, mid-turn]: also update the README" {
				t.Fatalf("persisted steering content = %q, want enveloped form", m.Content)
			}
		}
	}
	if persisted != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1: %#v", persisted, state.Messages())
	}

	// And it must replay via buildHistoryMessages.
	replayed := buildHistoryMessages(state.Messages(), 0, state.Generation(), map[int64][]db.ToolAuditEntry{})
	var sawReplay bool
	for _, m := range replayed {
		if m.Role == schema.RoleUser && m.Content == "[user steering, mid-turn]: also update the README" {
			sawReplay = true
		}
	}
	if !sawReplay {
		t.Fatal("persisted steering message not replayed by buildHistoryMessages")
	}
}

// Sanity check removed; agenttest.ScriptedProvider satisfies provider.Provider
// structurally.
