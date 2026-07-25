package sdd

import "testing"

func TestGateDecisionConstants(t *testing.T) {
	cases := []struct {
		name string
		want GateDecision
	}{
		{"accept", DecisionAccept},
		{"block", DecisionBlock},
		{"skip", DecisionSkip},
		{"reject", DecisionReject},
	}
	for _, c := range cases {
		if string(c.want) != c.name {
			t.Fatalf("%s = %q, want %q", c.name, c.want, c.name)
		}
	}
}

func TestGateResultCarriesProgressContext(t *testing.T) {
	r := GateResult{Decision: DecisionBlock, Event: "AUDIT_FAIL", Reason: "audit failed", KV: []string{"task", "T1", "finding", "unused import"}}
	if r.Decision != DecisionBlock || r.Event != "AUDIT_FAIL" || r.Reason != "audit failed" {
		t.Fatalf("fields wrong: %+v", r)
	}
	if len(r.KV) != 4 || r.KV[0] != "task" || r.KV[3] != "unused import" {
		t.Fatalf("KV = %v", r.KV)
	}
}

func TestGuardResultFiles(t *testing.T) {
	r := GuardResult{Pass: false, Event: "EDIT_GUARD", Reason: "orchestrator edited source", Files: []string{"internal/foo.go", "internal/bar.go"}}
	if r.Pass || len(r.Files) != 2 {
		t.Fatalf("result = %+v", r)
	}
}

func TestVerifyResultOutput(t *testing.T) {
	r := VerifyResult{Pass: true, Event: "VERIFY_PASS", Output: "ok marshal/internal/sdd\n", Violations: nil}
	if !r.Pass || r.Event != "VERIFY_PASS" || r.Output == "" {
		t.Fatalf("result = %+v", r)
	}
}

func TestHealthAlertEvidence(t *testing.T) {
	a := HealthAlert{Kind: "LOOP", Detail: "dispatch signature repeated 3x", Evidence: []string{"2026-07-25T19:00:00Z T1 DISPATCH_IMPL branch=sdd/T1"}}
	if a.Kind != "LOOP" || len(a.Evidence) != 1 {
		t.Fatalf("alert = %+v", a)
	}
}

func TestMergeResult(t *testing.T) {
	r := MergeResult{Merged: true, TaskID: "T1", Event: "MERGED", Reason: ""}
	if !r.Merged || r.TaskID != "T1" || r.Event != "MERGED" {
		t.Fatalf("result = %+v", r)
	}
}
