// internal/pipeline/compile_test.go
package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestCompilePlanWithMarshalBlocks(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Test Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: First\n\n" +
		"```marshal.patch file=\"foo.go\"\n" +
		"<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n" +
		"```\n\n" +
		"```marshal.run\ncommand = [\"go\", \"test\"]\nphase = \"verify\"\n```\n\n" +
		"## Task 2: Second\n\nProse only, no blocks.\n"
	path := writeFile(t, dir, "test-plan.md", planContent)

	plan, err := ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	ir, diags, err := CompilePlan(plan)
	if err != nil {
		t.Fatalf("CompilePlan: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
	if len(ir.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(ir.Tasks))
	}
	if len(ir.Tasks[0].Operations) != 2 {
		t.Fatalf("task 1 ops = %d, want 2", len(ir.Tasks[0].Operations))
	}
	if ir.Tasks[0].Operations[0].OpKind() != "patch" {
		t.Errorf("task 1 op 0 = %s, want patch", ir.Tasks[0].Operations[0].OpKind())
	}
	if ir.Tasks[0].Operations[1].OpKind() != "run" {
		t.Errorf("task 1 op 1 = %s, want run", ir.Tasks[0].Operations[1].OpKind())
	}
	if len(ir.Tasks[1].Operations) != 0 {
		t.Errorf("task 2 ops = %d, want 0 (prose only)", len(ir.Tasks[1].Operations))
	}
}

func TestCompilePlanLegacyNoBlocks(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Legacy Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: First\n\nProse only.\n"
	path := writeFile(t, dir, "legacy-plan.md", planContent)

	plan, err := ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	ir, diags, err := CompilePlan(plan)
	if err != nil {
		t.Fatalf("CompilePlan: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
	if len(ir.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(ir.Tasks))
	}
	if ir.Tasks[0].DerivedStatus() != TaskAgentOnly {
		t.Errorf("task 1 status = %q, want %q", ir.Tasks[0].DerivedStatus(), TaskAgentOnly)
	}
}

func TestCompilePlanDiagnosticForMalformedBlock(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: First\n\n" +
		"```marshal.unknown\nfoo = bar\n```\n"
	path := writeFile(t, dir, "bad-plan.md", planContent)

	plan, err := ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	_, diags, err := CompilePlan(plan)
	if err != nil {
		t.Fatalf("CompilePlan should not hard-error on a bad block: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("expected at least one diagnostic for unknown kind")
	}
	found := false
	for _, d := range diags {
		if d.Severity == DiagError && contains(d.Message, "unknown") {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics did not include an error about unknown kind: %v", diags)
	}
}

func TestCompilePlanDetectsDependencyCycle(t *testing.T) {
	dir := t.TempDir()
	planContent := "# Plan\n\n## Global Constraints\n\n- None.\n\n---\n\n" +
		"## Task 1: First\n\n```marshal.agent\nscope = []\n```\n\n" +
		"## Task 2: Second\n\n```marshal.agent\nscope = []\n```\n"
	path := writeFile(t, dir, "dep-plan.md", planContent)

	plan, err := ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	ir, _, err := CompilePlan(plan)
	if err != nil {
		t.Fatalf("CompilePlan: %v", err)
	}
	// Manually create a cycle for testing.
	ir.Tasks[0].Dependencies = []string{"task-2"}
	ir.Tasks[1].Dependencies = []string{"task-1"}
	err = ValidateIR(ir)
	if err == nil {
		t.Fatal("ValidateIR should detect the cycle")
	}
}
