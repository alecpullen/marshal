package pipeline

import "testing"

func TestEventPayloadIsOptional(t *testing.T) {
	ev := Event{TaskN: 1, Phase: PhaseImplementing}
	if ev.Payload != nil {
		t.Errorf("zero Event must have a nil Payload, got %#v", ev.Payload)
	}
}

func TestVerifyFailedPayloadCarriesOutput(t *testing.T) {
	ev := Event{
		TaskN: 3,
		Phase: PhaseFixing,
		Payload: VerifyFailedPayload{
			Command:   "go test ./...",
			Output:    "--- FAIL: TestRetryBackoff\n    retry_test.go:44: got 3, want 5",
			Round:     1,
			MaxRounds: 3,
		},
	}
	p, ok := ev.Payload.(VerifyFailedPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want VerifyFailedPayload", ev.Payload)
	}
	if p.Command != "go test ./..." {
		t.Errorf("Command = %q, want %q", p.Command, "go test ./...")
	}
	if p.Output == "" {
		t.Error("Output must carry the verifier's combined output verbatim")
	}
	if p.Round != 1 || p.MaxRounds != 3 {
		t.Errorf("round = %d/%d, want 1/3", p.Round, p.MaxRounds)
	}
}

func TestReviewPayloadDistinguishesCleanFromFindings(t *testing.T) {
	clean := ReviewPayload{Clean: true}
	if len(clean.Findings) != 0 {
		t.Error("a clean review carries no findings")
	}
	dirty := ReviewPayload{Findings: []Finding{{Severity: SeverityImportant, Text: "backoff overflows"}}}
	if dirty.Clean {
		t.Error("a review with findings must not be marked Clean")
	}
	if dirty.Findings[0].Severity != SeverityImportant {
		t.Errorf("Severity = %q, want Important", dirty.Findings[0].Severity)
	}
}

func TestCommitAndRetryAndConcernPayloads(t *testing.T) {
	c := CommitPayload{SHA: "a3f9e21", Subject: "plan: task 3 — Add retry helper"}
	if c.SHA != "a3f9e21" || c.Subject == "" {
		t.Errorf("CommitPayload = %#v", c)
	}
	r := RetryPayload{Attempt: 2, MaxAttempts: 3, Err: "connection reset"}
	if r.Attempt != 2 || r.MaxAttempts != 3 || r.Err == "" {
		t.Errorf("RetryPayload = %#v", r)
	}
	if (ConcernPayload{Text: "no test covers the overflow path"}).Text == "" {
		t.Error("ConcernPayload must carry text")
	}
	if (GateSkippedPayload{Reason: "no build or test command configured"}).Reason == "" {
		t.Error("GateSkippedPayload must carry a reason")
	}
}
