package agent

import (
	"context"
	"errors"
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
	replayed := buildHistoryMessages(state.Messages(), 0, state.Generation(), map[int64][]db.ToolAuditEntry{}, "")
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

// TestSendNowResumesSameTurnAtCheckpoint pins the feature's core promise:
// steering accepted while a tool batch runs is delivered at the next checkpoint
// INSIDE the same turn — one goal, one turn index — with the batch's complete
// tool results ahead of it, so the model reads what actually happened before it
// reads the user's reaction to it.
func TestSendNowResumesSameTurnAtCheckpoint(t *testing.T) {
	state := newTestState(t)
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
	})

	var submitErr error
	reg := registry.New()
	reg.Register(registry.Tool{
		Name:        "interrupt.steer",
		Description: "issues a mid-batch Send now",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			ref, ok := session.OperationFromContext(ctx)
			if !ok {
				submitErr = errors.New("tool ran outside a steering operation")
				return registry.ToolResult{Content: "tool output marker", Summary: "ran"}, nil
			}
			_, submitErr = state.SubmitSendNow(session.SendNowRequest{
				TurnID:    ref.TurnID,
				RequestID: "req-1",
				Text:      "use a different approach",
			})
			return registry.ToolResult{Content: "tool output marker", Summary: "ran"}, nil
		},
	})
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"inspect","action":{"type":"tool_call","tool":"interrupt.steer","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
		},
	}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.MaxToolIterations = 5
	runner.SetForceClass(string(ClassEdit))

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if submitErr != nil {
		t.Fatalf("SubmitSendNow from the tool batch: %v", submitErr)
	}
	// Same turn: steering continues the interrupted turn, it does not start a
	// replacement one.
	if got := state.TurnIndex(); got != 1 {
		t.Fatalf("turn index = %d, want 1 (steering must not open a new turn)", got)
	}
	var goals int
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.Content == "do the thing" {
			goals++
		}
	}
	if goals != 1 {
		t.Fatalf("goal persisted %d times, want exactly 1", goals)
	}
	if len(p.Requests) < 2 {
		t.Fatalf("provider saw %d requests, want >= 2", len(p.Requests))
	}
	// The second request carries the batch's real tool result, then the
	// steering envelope — results before reaction.
	second := p.Requests[1].Messages
	toolIdx, steerIdx := -1, -1
	for i, m := range second {
		if strings.Contains(m.Content, "tool output marker") {
			toolIdx = i
		}
		if m.Role == schema.RoleUser && m.Content == "[user steering, mid-turn]: use a different approach" {
			steerIdx = i
		}
	}
	if toolIdx < 0 {
		t.Fatalf("second request lost the batch's tool result: %v", second)
	}
	if steerIdx < 0 {
		t.Fatalf("steering was not delivered inside the same turn: %v", second)
	}
	if steerIdx < toolIdx {
		t.Fatalf("steering (index %d) preceded the tool result (index %d)", steerIdx, toolIdx)
	}
	// Persisted exactly once, so restart replay matches the wire byte-for-byte.
	var persisted int
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && m.ContentType == session.ContentTypeSteering {
			persisted++
		}
	}
	if persisted != 1 {
		t.Fatalf("persisted steering messages = %d, want exactly 1", persisted)
	}
}

// TestSendNowChildRunnersDoNotRegisterParentControl pins the eligibility
// predicate: an enabled runner that is not a root general conversation — here a
// non-general role — never opens an eligible turn, so a child's work can never
// satisfy a parent's steering request.
func TestSendNowChildRunnersDoNotRegisterParentControl(t *testing.T) {
	state := newTestState(t)
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
	})

	var sawActive bool
	var submitErr error
	reg := registry.New()
	reg.Register(registry.Tool{
		Name:        "interrupt.probe",
		Description: "probes steering eligibility",
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			sawActive = state.SteeringSnapshot().Active
			_, submitErr = state.SubmitSendNow(session.SendNowRequest{
				TurnID: "any", RequestID: "req-1", Text: "steer",
			})
			return registry.ToolResult{Content: "ok", Summary: "ok"}, nil
		},
	})
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"inspect","action":{"type":"tool_call","tool":"interrupt.probe","args":{}}}`,
			`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
		},
	}
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.EnableSteering = true
	runner.Role = RoleImplementer
	runner.MaxToolIterations = 5
	runner.SetForceClass(string(ClassEdit))

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sawActive {
		t.Fatal("a non-general runner opened an eligible steering turn")
	}
	if submitErr == nil {
		t.Fatal("Send now was accepted for a turn no eligible runner opened")
	}
}
