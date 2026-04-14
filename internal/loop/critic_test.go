package loop

import (
	"testing"
)

func TestCritic_ParsePassVerdict(t *testing.T) {
	content := `{"verdict": "PASS", "summary": "Good code", "issue": "", "fix": "", "concerns": []}`
	// Note: This JSON should parse correctly. If there are issues with the array syntax,
	// the critic.go ParseVerdict function may need adjustment.

	verdict, err := ParseVerdict(content)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !verdict.IsPass() {
		t.Errorf("expected PASS, got %s", verdict.Verdict)
	}
}

func TestCritic_ParseFailVerdict(t *testing.T) {
	content := `{"verdict": "FAIL", "summary": "Bad code", "issue": "syntax error", "fix": "add semicolon"}`

	verdict, err := ParseVerdict(content)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if verdict.IsPass() {
		t.Errorf("expected FAIL, got %s", verdict.Verdict)
	}
	if verdict.Issue != "syntax error" {
		t.Errorf("expected 'syntax error', got %s", verdict.Issue)
	}
}

func TestCritic_ParseWithThinkingTags(t *testing.T) {
	content := `Let me think about this code...

It has some issues...

{"verdict": "FAIL", "summary": "Needs work", "issue": "bug", "fix": "fix it"}`

	verdict, err := ParseVerdict(content)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if verdict.IsPass() {
		t.Error("expected FAIL after thinking tags")
	}
}

func TestCritic_InvalidVerdictDefaultsFail(t *testing.T) {
	content := `not valid json`

	verdict, err := ParseVerdict(content)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if verdict != nil {
		t.Error("expected nil verdict on error")
	}
}

func TestCritic_EmptyJSON(t *testing.T) {
	content := `{}`

	verdict, err := ParseVerdict(content)
	if err == nil {
		t.Error("expected error for empty verdict value")
	}
	if verdict != nil {
		t.Error("expected nil verdict on error")
	}
}
