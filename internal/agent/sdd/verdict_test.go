package sdd

import (
	"testing"
)

func TestParseTaskVerdictSpecCompliant(t *testing.T) {
	text := `### Spec Compliance
- ✅ Spec compliant

### Strengths
Good test coverage.

### Issues
#### Critical (Must Fix)
#### Important (Should Fix)
#### Minor (Nice to Have)
- parser.go:42 — name could be clearer

### Assessment
**Task quality:** Approved

**Reasoning:** Clean implementation.`
	v := ParseTaskVerdict(text)
	if v.SpecCompliance != VerdictPass {
		t.Errorf("SpecCompliance = %v, want %v", v.SpecCompliance, VerdictPass)
	}
	if v.TaskQuality != VerdictApproved {
		t.Errorf("TaskQuality = %v, want %v", v.TaskQuality, VerdictApproved)
	}
}

func TestParseTaskVerdictSpecFailed(t *testing.T) {
	text := `### Spec Compliance
- ❌ Issues found: missing progress reporting

### Assessment
**Task quality:** Needs fixes`
	v := ParseTaskVerdict(text)
	if v.SpecCompliance != VerdictFail {
		t.Errorf("SpecCompliance = %v, want %v", v.SpecCompliance, VerdictFail)
	}
	if v.TaskQuality != VerdictNeedsFixes {
		t.Errorf("TaskQuality = %v, want %v", v.TaskQuality, VerdictNeedsFixes)
	}
}

func TestParseTaskVerdictCannotVerify(t *testing.T) {
	text := `### Spec Compliance
- ⚠️ Cannot verify from diff: progress reporting lives in unchanged code

### Assessment
**Task quality:** Approved`
	v := ParseTaskVerdict(text)
	if v.SpecCompliance != VerdictWarning {
		t.Errorf("SpecCompliance = %v, want %v", v.SpecCompliance, VerdictWarning)
	}
}

func TestParseTaskVerdictExtractsFindings(t *testing.T) {
	text := `### Issues
#### Critical (Must Fix)
- main.go:15 — nil pointer risk on line 42
- missing timeout in config parsing

#### Important (Should Fix)
- handler.go:88 — log before return swallows error

#### Minor (Nice to Have)
- parser.go:42 — name could be clearer

### Assessment
**Task quality:** Approved`
	v := ParseTaskVerdict(text)
	if len(v.Findings) != 4 {
		t.Fatalf("Findings count = %d, want 4", len(v.Findings))
	}
	expect := []Finding{
		{Severity: "Critical", Text: "main.go:15 — nil pointer risk on line 42"},
		{Severity: "Critical", Text: "missing timeout in config parsing"},
		{Severity: "Important", Text: "handler.go:88 — log before return swallows error"},
		{Severity: "Minor", Text: "parser.go:42 — name could be clearer"},
	}
	for i, want := range expect {
		if v.Findings[i].Severity != want.Severity || v.Findings[i].Text != want.Text {
			t.Errorf("Finding[%d] = {%q, %q}, want {%q, %q}",
				i, v.Findings[i].Severity, v.Findings[i].Text, want.Severity, want.Text)
		}
	}
}

func TestParseBranchVerdictReady(t *testing.T) {
	text := `### Branch Verdict
- ✅ Ready to merge

### Assessment
**Branch quality:** Ready to merge`
	v := ParseBranchVerdict(text)
	if !v.Ready {
		t.Error("Ready = false, want true")
	}
}

func TestParseBranchVerdictNotReady(t *testing.T) {
	text := `### Branch Verdict
- ❌ Not ready: missing capability X

### Assessment
**Branch quality:** Needs fixes`
	v := ParseBranchVerdict(text)
	if v.Ready {
		t.Error("Ready = true, want false")
	}
}

func TestParseTaskVerdictExtractsDeviations(t *testing.T) {
	text := `### Issues
#### Important (Should Fix)
- Deviated: changed assertion from wantErr to wantErrMsg in foo_test.go:42

#### Minor (Nice to Have)
- parser.go:42 — name could be clearer

### Assessment
**Task quality:** Approved`
	v := ParseTaskVerdict(text)
	if len(v.Deviations) != 1 {
		t.Fatalf("Deviations count = %d, want 1", len(v.Deviations))
	}
	want := "Deviated: changed assertion from wantErr to wantErrMsg in foo_test.go:42"
	if v.Deviations[0] != want {
		t.Errorf("Deviation = %q, want %q", v.Deviations[0], want)
	}
	if len(v.Findings) != 2 {
		t.Fatalf("Findings count = %d, want 2", len(v.Findings))
	}
}
