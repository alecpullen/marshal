package agent

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/llm/schema"
)

// A Question-class task that mutates and then declines to verify gets one
// nudge; its second final is accepted as a normal completion (no salvage).
func TestNativeQuestionTaskWithUnverifiedMutationIsAcceptedAfterNudge(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{"patched", "Answer.", "Still the answer."},
		ToolCalls: [][]schema.ToolCall{
			{{ID: "c1", Name: "file.write_patch", Args: json.RawMessage(`{"patch":"File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"}`)}},
			nil, // final attempt 1: unverified — nudge
			nil, // final attempt 2: accepted but flagged
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	r.VerificationGate = true

	task, err := r.RunTask(context.Background(), "answer the question by editing a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Still the answer." {
		t.Fatalf("Summary = %q, want the third response", task.Summary)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (unverified-after-nudge is accepted, not salvaged)", task.SalvagedReason)
	}
	if !transcriptContains(state, "have not verified") {
		t.Fatal("expected a verification-nudge system message in the transcript")
	}
}

// A Question-class task that mutates and then declines to verify gets one
// nudge; its second final is accepted as a normal completion (no salvage).
func TestJSONQuestionFinalWithUnverifiedMutationIsAcceptedAfterNudge(t *testing.T) {
	state := newTestState(t)
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"edit","action":{"type":"patch","content":"File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"}}`,
			`{"rationale":"done","action":{"type":"final","content":"Answer."}}`,
			`{"rationale":"done","action":{"type":"final","content":"Still the answer."}}`,
		},
	}
	r := NewRunner(p, regPatchAndTest(t), gatePolicy(), state, "test-model")
	r.SetForceClass(string(ClassQuestion))
	r.VerificationGate = true

	task, err := r.RunTask(context.Background(), "answer the bug by editing a.go")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Still the answer." {
		t.Fatalf("Summary = %q", task.Summary)
	}
	if task.SalvagedReason != "" {
		t.Fatalf("SalvagedReason = %q, want empty (unverified-after-nudge is accepted, not salvaged)", task.SalvagedReason)
	}
	if !transcriptContains(state, "have not verified") {
		t.Fatal("expected a verification-nudge system message in the transcript")
	}
}
