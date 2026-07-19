package sdd

import (
	"strings"
	"testing"
)

func TestBuildImplementerPrompt(t *testing.T) {
	task := PlanTask{Number: 1, Title: "Hook installation", Body: "Implement the hook."}
	prompt := BuildImplementerPrompt(task, "/repo/.marshal/sdd/task-1-brief.md", "/repo/.marshal/sdd/task-1-report.md", "This is the first task in the project.")
	if !strings.Contains(prompt, "Task 1: Hook installation") {
		t.Errorf("prompt missing task title: %s", prompt)
	}
	if !strings.Contains(prompt, "/repo/.marshal/sdd/task-1-brief.md") {
		t.Errorf("prompt missing brief path: %s", prompt)
	}
	if !strings.Contains(prompt, "/repo/.marshal/sdd/task-1-report.md") {
		t.Errorf("prompt missing report path: %s", prompt)
	}
	if !strings.Contains(prompt, "This is the first task in the project.") {
		t.Errorf("prompt missing context: %s", prompt)
	}
}

func TestBuildTaskReviewerPrompt(t *testing.T) {
	prompt := BuildTaskReviewerPrompt(
		"/repo/.marshal/sdd/task-1-brief.md",
		"/repo/.marshal/sdd/task-1-report.md",
		"/repo/.marshal/sdd/review-abc1234..def5678.diff",
		"- Go 1.24+\n- No new deps",
		"abc1234",
		"def5678",
	)
	for _, want := range []string{
		"/repo/.marshal/sdd/task-1-brief.md",
		"/repo/.marshal/sdd/task-1-report.md",
		"/repo/.marshal/sdd/review-abc1234..def5678.diff",
		"Go 1.24+",
		"abc1234",
		"def5678",
		"Spec Compliance",
		"Task quality",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("reviewer prompt missing %q", want)
		}
	}
}

func TestBuildBranchReviewerPrompt(t *testing.T) {
	prompt := BuildBranchReviewerPrompt(
		"/repo/docs/plans/feature-plan.md",
		"/repo/.marshal/sdd",
		"/repo/.marshal/sdd/review-aaa0000..zzz9999.diff",
		"- Go 1.24+",
		"aaa0000",
		"zzz9999",
		[]string{"parser.go:42 — name could be clearer", "hook.go:10 — minor style"},
	)
	for _, want := range []string{
		"/repo/docs/plans/feature-plan.md",
		"/repo/.marshal/sdd",
		"aaa0000",
		"zzz9999",
		"name could be clearer",
		"minor style",
		"Branch Verdict",
		"Whole-Plan Coverage",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("branch reviewer prompt missing %q", want)
		}
	}
}

func TestBuildFixPrompt(t *testing.T) {
	task := PlanTask{Number: 2, Title: "Recovery modes", Body: "Add recovery modes."}
	prompt := BuildFixPrompt("/repo/.marshal/sdd/task-2-report.md", "#### Important\n- Missing progress reporting at parser.go:55", task)
	if !strings.Contains(prompt, "fix") || !strings.Contains(prompt, "Missing progress reporting") {
		t.Errorf("fix prompt missing findings: %s", prompt)
	}
	if !strings.Contains(prompt, "/repo/.marshal/sdd/task-2-report.md") {
		t.Errorf("fix prompt missing report path")
	}
}

func TestBuildBranchFixPrompt(t *testing.T) {
	p := BuildBranchFixPrompt("finding one\nfinding two")
	if !strings.Contains(p, "finding one") {
		t.Fatalf("prompt missing findings:\n%s", p)
	}
	if strings.Contains(p, "Task 0") {
		t.Fatalf("prompt reuses per-task framing with a zero-value task:\n%s", p)
	}
	if strings.Contains(p, "previous report") {
		t.Fatalf("branch fix prompt must not reference a per-task report:\n%s", p)
	}
}
