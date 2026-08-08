// internal/pipeline/inspect_test.go
package pipeline

import (
	"testing"

	"marshal/internal/worktree"
)

func TestInspectPlanAutoSelectsAdaptiveForExecutableBlocks(t *testing.T) {
	root := t.TempDir()
	planPath := writeFile(t, root, "plan.md", "# Plan\n\n## Task 1: Add file\n\n"+
		"```marshal.file path=\"created.txt\"\nhello\n```\n")

	got, err := InspectPlan(planPath, root, StrategyAuto)
	if err != nil {
		t.Fatalf("InspectPlan: %v", err)
	}
	if got.EffectiveStrategy != StrategyAdaptive {
		t.Fatalf("EffectiveStrategy = %q, want adaptive", got.EffectiveStrategy)
	}
	if got.Report.Total.DetOps != 1 || got.HasMarshalBlocks == false {
		t.Fatalf("unexpected report: %+v", got.Report)
	}
}

func TestInspectPlanAutoSelectsAgentForLegacyPlan(t *testing.T) {
	root := t.TempDir()
	planPath := writeFile(t, root, "legacy.md", "# Plan\n\n## Task 1: Explain\n\nProse only.\n")

	got, err := InspectPlan(planPath, root, StrategyAuto)
	if err != nil {
		t.Fatalf("InspectPlan: %v", err)
	}
	if got.EffectiveStrategy != StrategyAgent || got.HasMarshalBlocks {
		t.Fatalf("inspection = %+v, want legacy agent classification", got)
	}
}

func TestInspectPlanStrictBlocksAgentAndProseTasks(t *testing.T) {
	root := t.TempDir()
	planPath := writeFile(t, root, "strict.md", "# Plan\n\n## Task 1: Resolve\n\n"+
		"```marshal.agent\nscope = [\"internal/app\"]\nreason = \"needs design judgment\"\n```\n\n"+
		"## Task 2: Legacy\n\nProse only.\n")

	got, err := InspectPlan(planPath, root, StrategyStrict)
	if err != nil {
		t.Fatalf("InspectPlan: %v", err)
	}
	if !got.StrictBlocked {
		t.Fatal("strict inspection must block unresolved work")
	}
}

func TestControllerInspectionMatchesInspectPlan(t *testing.T) {
	root := t.TempDir()
	planPath := writeFile(t, root, "plan.md", "# Plan\n\n## Task 1: Add file\n\n"+
		"```marshal.file path=\"created.txt\"\nhello\n```\n")

	c, err := NewController(ControllerOpts{
		PlanPath: planPath,
		RepoRoot: root,
		Git:      worktree.NewFakeGitOps(),
		Strategy: StrategyAuto,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	got, err := c.Inspect()
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got.EffectiveStrategy != StrategyAdaptive {
		t.Fatalf("controller EffectiveStrategy = %q, want adaptive", got.EffectiveStrategy)
	}
	if c.PlanIR == nil {
		t.Fatal("controller PlanIR must be populated by Inspect")
	}
	if c.Strategy != StrategyAdaptive {
		t.Fatalf("controller Strategy = %q, want adaptive", c.Strategy)
	}
}
