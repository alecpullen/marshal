package agent

import (
	"context"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func newSalvageRunner(t *testing.T, p *agenttest.ScriptedProvider, executed *[]string) *Runner {
	t.Helper()
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "noop.tool", Description: "does nothing", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			*executed = append(*executed, "noop.tool")
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	return r
}

// A weak model that writes its tool call as OpenAI-style prose JSON must have
// the call executed, not shown to the user as the final answer.
func TestSalvageProseToolCallFlatShape(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"name": "noop.tool", "arguments": {}}`,
			"all done",
		},
		FinishReasons: []string{"stop", "stop"},
	}
	var executed []string
	r := newSalvageRunner(t, p, &executed)

	if err := r.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("tool executed %d times, want 1 (prose tool call was not salvaged)", len(executed))
	}
}

// The marshal envelope shape emitted as text must salvage too.
func TestSalvageProseToolCallEnvelopeShape(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"action": {"type": "tool_call", "tool": "noop.tool", "args": {}}}`,
			"all done",
		},
		FinishReasons: []string{"stop", "stop"},
	}
	var executed []string
	r := newSalvageRunner(t, p, &executed)

	if err := r.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("tool executed %d times, want 1 (envelope tool call was not salvaged)", len(executed))
	}
}

// Prose without a tool call must keep the existing accept-as-answer path.
func TestSalvageLeavesPlainProseAlone(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"the answer is 42"},
		FinishReasons: []string{"stop"},
	}
	var executed []string
	r := newSalvageRunner(t, p, &executed)

	if err := r.Run(context.Background(), "what is the answer"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(executed) != 0 {
		t.Fatalf("plain prose triggered %d tool executions, want 0", len(executed))
	}
}

func TestSalvageTextToolCallsUnit(t *testing.T) {
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "noop.tool", Description: "does nothing", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	r := NewRunner(nil, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "m")

	cases := []struct {
		name     string
		text     string
		wantCall bool
		wantArgs string
	}{
		{"flat shape", `{"name": "noop.tool", "arguments": {"a": 1}}`, true, `{"a": 1}`},
		{"nested function shape", `{"function": {"name": "noop.tool", "arguments": {"a": 2}}}`, true, `{"a": 2}`},
		{"arguments as JSON string", `{"name": "noop.tool", "arguments": "{\"a\": 3}"}`, true, `{"a": 3}`},
		{"tool_calls wrapper", `{"tool_calls": [{"name": "noop.tool", "arguments": {}}]}`, true, `{}`},
		{"tool/args shape", `{"tool": "noop.tool", "args": {"a": 4}}`, true, `{"a": 4}`},
		{"prose around the JSON", "Let me check.\n" + `{"name": "noop.tool", "arguments": {}}`, true, `{}`},
		{"unknown tool is not salvaged", `{"name": "does.not.exist", "arguments": {}}`, false, ""},
		{"plain prose", "no JSON here at all", false, ""},
		{"JSON but not a tool call", `{"answer": "42"}`, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, ok := r.salvageTextToolCalls(tc.text)
			if ok != tc.wantCall {
				t.Fatalf("salvageTextToolCalls ok = %v, want %v (calls: %+v)", ok, tc.wantCall, calls)
			}
			if !ok {
				return
			}
			if len(calls) != 1 || calls[0].Name != "noop.tool" {
				t.Fatalf("calls = %+v, want one noop.tool call", calls)
			}
			if calls[0].ID == "" {
				t.Fatal("salvaged call needs a synthesized ID for the tool-result message")
			}
			if string(calls[0].Args) != tc.wantArgs {
				t.Fatalf("args = %s, want %s", calls[0].Args, tc.wantArgs)
			}
		})
	}
}
