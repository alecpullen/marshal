package agent

import (
	"context"
	"encoding/json"
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

// registerInterruptTestTool registers a trivial read-only tool so a turn can
// execute a real tool call and populate the per-turn tool-audit buffer.
func registerInterruptTestTool(reg *registry.Registry) {
	reg.Register(registry.Tool{
		Name: "interrupt.test", Description: "test tool", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ran"}, nil
		},
	})
}

// TestInterruptedTurnPersistsMarkerAndClearsAudit pins that an Esc-cancelled
// turn persists exactly one RoleUser marker containing the goal, that the
// marker replays via buildHistoryMessages, and that the dead turn's tool-audit
// buffer is cleared so it does not flush under the next turn's final message.
func TestInterruptedTurnPersistsMarkerAndClearsAudit(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"interrupt.test","args":{}}}`,
		},
		ToolCalls: [][]schema.ToolCall{{{ID: "tc1", Name: "interrupt.test", Args: json.RawMessage(`{}`)}}},
		Errs:      []error{nil, context.Canceled},
	}
	reg := registry.New()
	registerInterruptTestTool(reg)
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}

	// Exactly one persisted RoleUser marker containing the goal.
	var markers []session.Message
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser && strings.Contains(m.Content, "[turn interrupted]") {
			markers = append(markers, m)
		}
	}
	if len(markers) != 1 {
		t.Fatalf("persisted interrupt markers = %d, want exactly 1", len(markers))
	}
	if !strings.Contains(markers[0].Content, "do the thing") {
		t.Fatalf("marker does not contain the goal: %q", markers[0].Content)
	}
	if !strings.Contains(markers[0].Content, "The current request continues from this state.") {
		t.Fatalf("marker missing closing line: %q", markers[0].Content)
	}
	// The tool that ran this turn must be listed in the marker.
	if !strings.Contains(markers[0].Content, "interrupt.test") {
		t.Fatalf("marker missing completed tool: %q", markers[0].Content)
	}

	// The tool-audit buffer must be cleared so the dead turn's entries do not
	// flush under the next turn's final assistant message. TakeToolAuditForInterrupt
	// returns "" when the buffer is already empty, so a second call proves the
	// dead turn's entries were consumed by the marker.
	if again := state.TakeToolAuditForInterrupt(); again != "" {
		t.Fatalf("tool-audit buffer not cleared after interrupt, second take = %q", again)
	}

	// The marker replays via buildHistoryMessages.
	replayed := buildHistoryMessages(state.Messages(), 0, state.Generation(), map[int64][]db.ToolAuditEntry{})
	found := false
	for _, m := range replayed {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "[turn interrupted]") {
			found = true
		}
	}
	if !found {
		t.Fatal("interrupt marker not replayed by buildHistoryMessages")
	}
}

// TestInterruptedTurnCarriesUndeliveredSteering pins that steering queued but
// not yet delivered to the model lands inside the interrupt marker, tagged as
// undelivered user feedback.
//
// The steering must be queued AFTER the final loop-top drain and BEFORE the
// cancellation surfaces: anything pushed before Run is legitimately delivered
// at the first loop-top (as an enveloped "[user steering, mid-turn]" wire
// message), so it must NOT appear in the marker. OnChat(idx 1) fires at the
// start of the second model call — after the last drain, before its cancel
// error is processed — which is exactly the undelivered window.
func TestInterruptedTurnCarriesUndeliveredSteering(t *testing.T) {
	var st *session.State
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"r","action":{"type":"tool_call","tool":"interrupt.test","args":{}}}`,
		},
		ToolCalls: [][]schema.ToolCall{{{ID: "tc1", Name: "interrupt.test", Args: json.RawMessage(`{}`)}}},
		Errs:      []error{nil, context.Canceled},
		OnChat: func(idx int, req schema.ChatRequest) {
			if idx == 1 {
				st.PushSteering("please use a different approach")
			}
		},
	}
	reg := registry.New()
	registerInterruptTestTool(reg)
	pol := policy.NewEngine(&config.Config{}, nil)
	st = newTestState(t)
	runner := NewRunner(p, reg, pol, st, "test-model")
	runner.NativeTools = true
	runner.SetForceClass(string(ClassQuestion))

	if err := runner.Run(context.Background(), "do the thing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}

	var marker string
	for _, m := range st.Messages() {
		if m.Role == session.RoleUser && strings.Contains(m.Content, "[turn interrupted]") {
			marker = m.Content
		}
	}
	if marker == "" {
		t.Fatal("no interrupt marker persisted")
	}
	if !strings.Contains(marker, "Undelivered user feedback: please use a different approach") {
		t.Fatalf("marker missing undelivered steering: %q", marker)
	}
}

// TestMidTurnSteeringDrainWrapsWireContent pins that a steering message
// drained at loop-top is wrapped with the envelope prefix on the model wire.
func TestMidTurnSteeringDrainWrapsWireContent(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{`{"rationale":"r","action":{"type":"answer","content":"done"}}`},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SetForceClass(string(ClassQuestion))

	state.PushSteering("steer me")

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("no chat request captured")
	}
	found := false
	for _, m := range p.Requests[0].Messages {
		if m.Role == schema.RoleUser && strings.Contains(m.Content, "[user steering, mid-turn]: steer me") {
			found = true
		}
	}
	if !found {
		t.Fatal("steering message not wrapped with the envelope prefix on the wire")
	}
}
