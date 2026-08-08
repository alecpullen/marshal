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
	constraints := "- minTranscriptRows is a package constant with value 1."
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

func TestRenderImplementerUsesRunAlias(t *testing.T) {
	prompt, err := RenderImplementer(ImplementerPrompt{
		TaskN:          1,
		Title:          "First",
		Placement:      "Task 1 of 2",
		BriefBasename:  "task-1-brief.md",
		ReportBasename: "task-1-report.md",
		WorkDir:        "/tmp/worktree",
	})
	if err != nil {
		t.Fatalf("RenderImplementer: %v", err)
	}
	if !strings.Contains(prompt, "@run/task-1-brief.md") {
		t.Errorf("implementer prompt should reference @run/task-1-brief.md:\n%s", prompt)
	}
	if !strings.Contains(prompt, "@run/task-1-report.md") {
		t.Errorf("implementer prompt should reference @run/task-1-report.md:\n%s", prompt)
	}
}

func TestRenderReviewIncludesInputsField(t *testing.T) {
	prompt, err := RenderReview(ReviewPrompt{
		TaskN:           1,
		Title:           "First",
		BriefBasename:   "task-1-brief.md",
		ReportBasename:  "task-1-report.md",
		PackageBasename: "task-1-review.md",
		VerdictBasename: "task-1-review-verdict.md",
	})
	if err != nil {
		t.Fatalf("RenderReview: %v", err)
	}
	if !strings.Contains(prompt, "INPUTS:") {
		t.Errorf("review prompt should include INPUTS field:\n%s", prompt)
	}
}

func TestPromptsExplainTheArtifactAlias(t *testing.T) {
	impl, err := RenderImplementer(ImplementerPrompt{
		TaskN: 1, Title: "T", BriefBasename: "task-1-brief.md",
		ReportBasename: "task-1-report.md", WorkDir: "/wt",
	})
	if err != nil {
		t.Fatalf("RenderImplementer: %v", err)
	}
	review, err := RenderReview(ReviewPrompt{
		TaskN: 1, Title: "T", BriefBasename: "task-1-brief.md",
		ReportBasename: "task-1-report.md", PackageBasename: "task-1-review.md",
		VerdictBasename: "task-1-review-verdict.md",
	})
	if err != nil {
		t.Fatalf("RenderReview: %v", err)
	}
	for name, prompt := range map[string]string{"implementer": impl, "review": review} {
		if !strings.Contains(prompt, "real, resolvable paths") {
			t.Errorf("%s prompt uses @run/ without explaining it:\n%s", name, prompt)
		}
	}
	if !strings.Contains(review, "only after a file tool has actually failed") {
		t.Errorf("review prompt lets INPUTS: BLOCKED be reported without trying:\n%s", review)
	}
}
