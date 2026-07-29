package pipeline

import (
	"strings"
	"testing"
)

func TestRenderImplementer(t *testing.T) {
	got, err := RenderImplementer(ImplementerPrompt{
		TaskN:      2,
		Title:      "Scratch directory layout",
		Placement:  "This is the second of 14 tasks building internal/pipeline.",
		BriefPath:  "/run/task-2-brief.md",
		ReportPath: "/run/task-2-report.md",
		WorkDir:    "/run/worktrees/pipeline-my-plan",
		Interfaces: "Task 1 produced ParsePlan(path string) (*Plan, error).",
	})
	if err != nil {
		t.Fatalf("RenderImplementer: %v", err)
	}
	for _, want := range []string{
		"Task 2", "Scratch directory layout",
		"/run/task-2-brief.md", "/run/task-2-report.md",
		"/run/worktrees/pipeline-my-plan",
		"ParsePlan(path string) (*Plan, error)",
		"do NOT commit", "STATUS:", "TESTS:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "git commit -m") {
		t.Error("prompt shows the implementer how to commit; the controller commits")
	}
}

func TestRenderImplementerIncludesAnswer(t *testing.T) {
	got, err := RenderImplementer(ImplementerPrompt{
		TaskN: 1, Title: "T", BriefPath: "b", ReportPath: "r", WorkDir: "w",
		Answer: "User level, at ~/.config/marshal/hooks/.",
	})
	if err != nil {
		t.Fatalf("RenderImplementer: %v", err)
	}
	if !strings.Contains(got, "~/.config/marshal/hooks/") {
		t.Errorf("answer to the earlier question was dropped:\n%s", got)
	}
}

func TestRenderFixListsEveryFinding(t *testing.T) {
	got, err := RenderFix(FixPrompt{
		TaskN: 3, BriefPath: "b", ReportPath: "r", WorkDir: "w",
		Reason: "the task reviewer requested changes",
		Findings: []Finding{
			{Severity: SeverityCritical, Text: "nil deref on single-task plans"},
			{Severity: SeverityImportant, Text: "magic number 100"},
		},
		CoveringTests: "internal/pipeline/plan_test.go",
	})
	if err != nil {
		t.Fatalf("RenderFix: %v", err)
	}
	for _, want := range []string{"nil deref on single-task plans", "magic number 100", "internal/pipeline/plan_test.go", "do NOT commit"} {
		if !strings.Contains(got, want) {
			t.Errorf("fix prompt missing %q:\n%s", want, got)
		}
	}
}

func TestRenderReviewCarriesConstraintsVerbatim(t *testing.T) {
	constraints := "- minTranscriptRows is a package constant with value 3."
	got, err := RenderReview(ReviewPrompt{
		TaskN: 4, Title: "Git seam", BriefPath: "b", ReportPath: "r",
		PackagePath: "/run/task-4-review.md", ReviewPath: "/run/task-4-verdict.md",
		GlobalConstraints: constraints,
	})
	if err != nil {
		t.Fatalf("RenderReview: %v", err)
	}
	for _, want := range []string{constraints, "/run/task-4-review.md", "SPEC:", "QUALITY:", "FINDINGS:"} {
		if !strings.Contains(got, want) {
			t.Errorf("review prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "do not flag") {
		t.Error("review prompt pre-judges findings")
	}
}

func TestRenderBranchReviewIncludesMinors(t *testing.T) {
	got, err := RenderBranchReview(BranchReviewPrompt{
		PlanPath: "docs/superpowers/plans/p.md", PackagePath: "/run/branch-review.md",
		ReviewPath: "/run/branch-verdict.md", Range: "base..head",
		Minors: []string{"Task 1 (minor): magic number 100"},
	})
	if err != nil {
		t.Fatalf("RenderBranchReview: %v", err)
	}
	for _, want := range []string{"docs/superpowers/plans/p.md", "/run/branch-review.md", "base..head", "magic number 100"} {
		if !strings.Contains(got, want) {
			t.Errorf("branch review prompt missing %q:\n%s", want, got)
		}
	}
}
