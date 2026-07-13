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
