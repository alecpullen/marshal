package pipeline

import "testing"

func TestParseImplementerReportDone(t *testing.T) {
	r, err := ParseImplementerReport("STATUS: DONE\nTESTS: go test ./internal/pipeline/... — 12/12 pass\n")
	if err != nil {
		t.Fatalf("ParseImplementerReport: %v", err)
	}
	if r.Status != StatusDone {
		t.Errorf("Status = %q", r.Status)
	}
	if r.Tests != "go test ./internal/pipeline/... — 12/12 pass" {
		t.Errorf("Tests = %q", r.Tests)
	}
}

func TestParseImplementerReportBlockedRequiresQuestion(t *testing.T) {
	if _, err := ParseImplementerReport("STATUS: BLOCKED\nTESTS: none\n"); err == nil {
		t.Fatal("BLOCKED with no QUESTION: want error, got nil")
	}
	r, err := ParseImplementerReport("STATUS: NEEDS_CONTEXT\nQUESTION: user or system level?\n")
	if err != nil {
		t.Fatalf("ParseImplementerReport: %v", err)
	}
	if r.Question != "user or system level?" {
		t.Errorf("Question = %q", r.Question)
	}
}

func TestParseImplementerReportMalformed(t *testing.T) {
	for _, in := range []string{"", "I finished the task!\n", "STATUS: FINISHED\n"} {
		if _, err := ParseImplementerReport(in); err == nil {
			t.Errorf("ParseImplementerReport(%q): want error, got nil", in)
		}
	}
}

func TestParseReviewReport(t *testing.T) {
	text := "SPEC: FAIL\nQUALITY: CHANGES_REQUESTED\nFINDINGS:\n" +
		"- [Critical] nil deref when the plan has one task\n" +
		"- [Minor] magic number 100\n" +
		"\nThe missing progress reporting is required by the brief.\n"
	r, err := ParseReviewReport(text)
	if err != nil {
		t.Fatalf("ParseReviewReport: %v", err)
	}
	if r.SpecPass || r.QualityApproved {
		t.Errorf("verdicts = %v/%v, want both false", r.SpecPass, r.QualityApproved)
	}
	if len(r.Findings) != 2 {
		t.Fatalf("len(Findings) = %d, want 2", len(r.Findings))
	}
	if r.Findings[0].Severity != SeverityCritical {
		t.Errorf("Findings[0].Severity = %q", r.Findings[0].Severity)
	}
	blocking := r.Blocking()
	if len(blocking) != 1 || blocking[0].Severity != SeverityCritical {
		t.Errorf("Blocking() = %v, want just the Critical", blocking)
	}
	if r.Raw == "" {
		t.Error("Raw is empty; the fixer needs the reviewer's prose")
	}
}

func TestParseReviewReportCleanRequiresBothVerdicts(t *testing.T) {
	r, err := ParseReviewReport("SPEC: PASS\nQUALITY: APPROVED\nFINDINGS:\n- none\n")
	if err != nil {
		t.Fatalf("ParseReviewReport: %v", err)
	}
	if !r.SpecPass || !r.QualityApproved {
		t.Errorf("verdicts = %v/%v, want both true", r.SpecPass, r.QualityApproved)
	}
	if len(r.Blocking()) != 0 {
		t.Errorf("Blocking() = %v, want empty", r.Blocking())
	}
	if _, err := ParseReviewReport("SPEC: PASS\n"); err == nil {
		t.Fatal("missing QUALITY verdict: want error, got nil")
	}
}
