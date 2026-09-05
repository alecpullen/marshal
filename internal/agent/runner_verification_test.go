package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// regPatchAndTest registers the tools the gate tests need. test.run is
// declared RiskCommand to mirror its production classification, so the tests
// exercise the real policy-approval path (gatePolicy auto-approves) rather
// than bypassing it with RiskReadOnly. unverifiedMutation is name-based
// (categorize), so the declared risk of the other tools is irrelevant to it.
func regPatchAndTest(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	for _, name := range []string{"file.read", "file.write_patch"} {
		if err := reg.Register(registry.Tool{Name: name, Risk: registry.RiskReadOnly, Handler: nopHandler}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	if err := reg.Register(registry.Tool{Name: "test.run", Risk: registry.RiskCommand, Handler: nopHandler}); err != nil {
		t.Fatalf("register test.run: %v", err)
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

func transcriptCount(state interface{ Messages() []session.Message }, needle string) int {
	n := 0
	for _, m := range state.Messages() {
		n += strings.Count(m.Content, needle)
	}
	return n
}

// A final answer after a patch with no verification run is nudged once;
// if the model finals again without verifying, the answer is accepted as a
// NORMAL completion — the nudge is the whole enforcement. Salvaging the
// final punished models that used the nudge's escape hatch and added
// transcript noise without changing behavior.
func TestNativeFinalAfterUnverifiedPatchGetsNudgeThenAccepted(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "Done.", "Still done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"}`)}},
			nil, // first final attempt: unverified — nudge
			nil, // second final attempt: accepted normally
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.VerificationGate = true

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
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (unverified-after-nudge is accepted, not salvaged)", task.SalvagedReason)
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
	r.VerificationGate = true

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
	r.VerificationGate = true

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty for a read-only turn", task.SalvagedReason)
	}
}

// The JSON-envelope final path applies the same rule: nudge once, then
// accept normally.
func TestJSONFinalAfterUnverifiedPatchGetsNudgeThenAccepted(t *testing.T) {
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
	r.VerificationGate = true

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
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (unverified-after-nudge is accepted, not salvaged)", task.SalvagedReason)
	}
	if !transcriptContains(state, "have not verified") {
		t.Fatal("expected a verification-nudge system message in the transcript")
	}
}

// The nudge fires at most once per turn. A model that is nudged and then
// edits again is NOT nudged a second time; its next final is accepted
// normally. Re-arming produced repeated transcript noise without changing
// model behavior.
func TestNativeSecondMutationAfterNudgeStaysCapped(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "Done.", "patched again", "Still done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"}`)}},
			nil, // final attempt 1: unverified — nudge (the only one)
			{{ID: "c2", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"File: b.go\n<<<<<<< SEARCH\na\n=======\nb\n>>>>>>> REPLACE"}`)}},
			nil, // final attempt 2: still unverified — accepted, no second nudge
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.VerificationGate = true

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Still done." {
		t.Fatalf("Summary = %q, want the fourth response", task.Summary)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty", task.SalvagedReason)
	}
	if n := transcriptCount(state, "have not verified"); n != 1 {
		t.Fatalf("verification nudge count = %d, want exactly 1 per turn", n)
	}
}

// The JSON-envelope path also gains zero-tool-call grounding nudge it
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
	r.VerificationGate = true

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

// Edit → test.run → git commit → final is clean: the commit is
// housekeeping and must not re-arm the gate after verification.
func TestNativeVerificationThenCommitIsClean(t *testing.T) {
	state := newTestState(t)
	reg := regPatchAndTest(t)
	if err := reg.Register(registry.Tool{Name: "shell.run", Risk: registry.RiskCommand, Handler: nopHandler}); err != nil {
		t.Fatalf("register shell.run: %v", err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "tested", "committed", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"p"}`)}},
			{{ID: "c2", Name: "test.run", Args: json.RawMessage(`{"command":"go test ./..."}`)}},
			{{ID: "c3", Name: "shell.run", Args: json.RawMessage(`{"command":"git commit -m done"}`)}},
			nil,
		},
	}
	r := NewRunner(p, reg, gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.VerificationGate = true

	task, err := r.RunTask(context.Background(), "fix, test, and commit")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (commit after verification is housekeeping)", task.SalvagedReason)
	}
	if transcriptContains(state, "have not verified") {
		t.Fatal("gate must not fire when verification ran before a housekeeping commit")
	}
}

// A mutating shell.run (sed -i) at the runner level must trigger the
// verification nudge — the runner-level gate must not only cover
// file.write_patch (the existing tests) but also shell mutators that
// arm through the allowlist.
func TestNativeMutatingShellRunTriggersNudge(t *testing.T) {
	state := newTestState(t)
	reg := regPatchAndTest(t)
	if err := reg.Register(registry.Tool{Name: "shell.run", Risk: registry.RiskCommand, Handler: nopHandler}); err != nil {
		t.Fatalf("register shell.run: %v", err)
	}
	p := &agenttest.ScriptedProvider{
		Responses: []string{"edited file", "Done.", "Still done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "shell.run", Args: json.RawMessage(`{"command":"sed -i 's/a/b/' f.go"}`)}},
			nil, // final attempt 1: unverified — nudge
			nil, // final attempt 2: accepted
		},
	}
	r := NewRunner(p, reg, gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.VerificationGate = true

	task, err := r.RunTask(context.Background(), "fix the file with sed")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if p.Calls != 3 {
		t.Fatalf("Calls = %d, want 3 (rm, nudge, accept)", p.Calls)
	}
	if task.Summary != "Still done." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	if !transcriptContains(state, "have not verified") {
		t.Fatal("sed -i via shell.run must trigger the verification nudge at runner level")
	}
}

// Default-off: an unverified mutation followed by a final answer is
// accepted immediately with no nudge when the gate is not enabled.
func TestNativeFinalAfterUnverifiedPatchAcceptedSilentlyWhenGateOff(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"}`)}},
			nil, // final: accepted immediately — no nudge
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	// No r.VerificationGate = true: default must be off.

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if p.Calls != 2 {
		t.Fatalf("Calls = %d, want 2 (patch, accept) — a nudge would add a third", p.Calls)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want the second response", task.Summary)
	}
	if transcriptContains(state, "have not verified") {
		t.Fatal("no verification-nudge message expected when the gate is off")
	}
}

// Per-preset override: an explicit verification_gate on the resolved
// preset enables the nudge even when the global fallback is off.
func TestPresetVerificationGateEnablesNudge(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "Done.", "Still done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"p"}`)}},
			nil,
			nil,
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	gate := true
	r.RouteResolver = &staticResolver{
		route: routing.Route{Preset: routing.ModelPreset{VerificationGate: &gate}},
	}

	task, err := r.RunTask(context.Background(), "fix the bug in a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if p.Calls != 3 {
		t.Fatalf("Calls = %d, want 3 (patch, nudge, accept)", p.Calls)
	}
	if !transcriptContains(state, "have not verified") {
		t.Fatal("expected a verification-nudge message when the preset enables the gate")
	}
	if task.Summary != "Still done." {
		t.Fatalf("Summary = %q, want the third response", task.Summary)
	}
}

// Per-preset explicit false beats a globally-enabled gate.
func TestPresetVerificationGateFalseOverridesGlobal(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "Done."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"p"}`)}},
			nil,
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassEdit))
	r.VerificationGate = true // global on…
	gate := false
	r.RouteResolver = &staticResolver{
		route: routing.Route{Preset: routing.ModelPreset{VerificationGate: &gate}},
	}

	if _, err := r.RunTask(context.Background(), "fix the bug in a.go"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if p.Calls != 2 {
		t.Fatalf("Calls = %d, want 2 — preset false must silence the nudge", p.Calls)
	}
	if transcriptContains(state, "have not verified") {
		t.Fatal("preset verification_gate=false must override the global gate")
	}
}
