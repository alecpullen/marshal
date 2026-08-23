package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// regPatchAndTest registers the tools the gate tests need. All are
// RiskReadOnly so the policy engine never intercepts: unverifiedMutation is
// name-based (categorize), so declared risk is irrelevant here.
func regPatchAndTest(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	for _, name := range []string{"file.read", "file.write_patch", "test.run"} {
		if err := reg.Register(registry.Tool{Name: name, Risk: registry.RiskReadOnly, Handler: nopHandler}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	return reg
}

// gatePolicy returns a policy engine that auto-approves shell/test commands
// so the gate tests can actually run test.run without an interactive
// approval prompt.
func gatePolicy() *policy.PolicyEngine {
	cfg := &config.Config{}
	cfg.Tools.Shell.AutoApprove = true
	return policy.NewEngine(cfg, nil)
}

func transcriptContains(state interface{ Messages() []session.Message }, needle string) bool {
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

// A final answer after a patch with no verification run must be nudged once,
// then accepted-but-flagged unverified on the second attempt.
func TestNativeFinalAfterUnverifiedPatchGetsNudgeThenSalvaged(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "Done.", "Still done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"}`)}},
			nil, // first final attempt: unverified — nudge
			nil, // second final attempt: accepted but flagged
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if p.Calls != 3 {
		t.Fatalf("Calls = %d, want 3 (patch, nudge, accept)", p.Calls)
	}
	if task.Summary != "Still done." {
		t.Fatalf("Summary = %q, want the third response", task.Summary)
	}
	if task.SalvagedReason != string(reasonUnverified) {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, reasonUnverified)
	}
	if !transcriptContains(state, "have not verified") {
		t.Fatal("expected a verification-nudge system message in the transcript")
	}
}

// A patch followed by a verification run completes cleanly.
func TestNativeFinalAfterVerifiedPatchIsClean(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "tested", "All done, confirmed."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"p"}`)}},
			{{ID: "c2", Name: "test.run", Args: json.RawMessage(`{"command":"go test ./..."}`)}},
			nil,
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "All done, confirmed." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (verified work must not be flagged)", task.SalvagedReason)
	}
	if transcriptContains(state, "have not verified") {
		t.Fatal("verification nudge must not fire when test.run ran after the mutation")
	}
}

// Read-only work never trips the gate.
func TestNativeReadOnlyTurnIsClean(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"looked", "All read, nothing changed."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.read", Args: json.RawMessage(`{"path":"a.go"}`)}},
			nil,
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty for a read-only turn", task.SalvagedReason)
	}
}

// The JSON-envelope final path gets the same gate: patch action, unverified
// final → nudge, second final → salvaged accept.
func TestJSONFinalAfterUnverifiedPatchGetsNudgeThenSalvaged(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"edit","action":{"type":"patch","content":"File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"}}`,
			`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
			`{"rationale":"done","action":{"type":"final","content":"Still done."}}`,
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if p.Calls != 3 {
		t.Fatalf("Calls = %d, want 3 (patch, nudge, accept)", p.Calls)
	}
	if task.Summary != "Still done." {
		t.Fatalf("Summary = %q, want the third response", task.Summary)
	}
	if task.SalvagedReason != string(reasonUnverified) {
		t.Fatalf("SalvagedReason = %q, want %q", task.SalvagedReason, reasonUnverified)
	}
	if !transcriptContains(state, "have not verified") {
		t.Fatal("expected a verification-nudge system message in the transcript")
	}
}

// The JSON-envelope path also gains the zero-tool-call grounding nudge it
// previously lacked.
func TestJSONEditTaskRequiresToolCallBeforeCompletion(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"fabricated","action":{"type":"final","content":"Fixed it."}}`,
			`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{"path":"a.go"}}}`,
			`{"rationale":"done","action":{"type":"final","content":"Confirmed."}}`,
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.SetForceClass(string(ClassEdit))

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if p.Calls != 3 {
		t.Fatalf("Calls = %d, want 3 (nudge, tool call, accept)", p.Calls)
	}
	if task.Summary != "Confirmed." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (a real read call grounded the answer)", task.SalvagedReason)
	}
	if !transcriptContains(state, "have not made any tool calls") {
		t.Fatal("expected a grounding-nudge system message in the transcript")
	}
}
