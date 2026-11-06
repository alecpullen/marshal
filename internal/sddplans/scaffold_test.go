package sddplans

import (
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/pipeline"
)

func TestScaffoldWritesAParseablePlan(t *testing.T) {
	root := t.TempDir()
	plansDir := config.Default().SDD.PlansDir

	path, existed, err := Scaffold(root, plansDir)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}
	if existed {
		t.Error("Scaffold reported the plan already existed in an empty directory")
	}
	want := filepath.Join(root, plansDir, StarterPlanName)
	if path != want {
		t.Errorf("Scaffold path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("scaffold did not write a plan file: %v", err)
	}
	// The whole point: what we write must be runnable.
	plan, err := pipeline.ParsePlan(path)
	if err != nil {
		t.Fatalf("scaffolded plan does not parse — the empty state is still a dead end: %v", err)
	}
	if len(plan.Tasks) == 0 {
		t.Error("scaffolded plan has no tasks")
	}
	if plan.GlobalConstraints == "" {
		t.Error("scaffolded plan should demonstrate the Global Constraints section")
	}
}

func TestScaffoldDoesNotClobberAnExistingFile(t *testing.T) {
	root := t.TempDir()
	plansDir := config.Default().SDD.PlansDir
	dir := filepath.Join(root, plansDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, StarterPlanName)
	if err := os.WriteFile(path, []byte("PRIOR CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, existed, err := Scaffold(root, plansDir)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}
	if !existed {
		t.Error("Scaffold must report an existing plan")
	}
	if got != path {
		t.Errorf("Scaffold path = %q, want %q", got, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "PRIOR CONTENT" {
		t.Error("scaffold must never overwrite an existing plan")
	}
}

func TestScaffoldPlanCompilesWithExecutableBlocks(t *testing.T) {
	root := t.TempDir()
	path, _, err := Scaffold(root, config.Default().SDD.PlansDir)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	plan, err := pipeline.ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	ir, diags, err := pipeline.CompilePlan(plan)
	if err != nil {
		t.Fatalf("CompilePlan: %v", err)
	}
	for _, d := range diags {
		if d.Severity == pipeline.DiagError {
			t.Fatalf("scaffolded plan has compile error: %s", d.Message)
		}
	}
	hasFile := false
	hasAssert := false
	for _, task := range ir.Tasks {
		for _, op := range task.Operations {
			if op.OpKind() == "file" {
				hasFile = true
			}
			if op.OpKind() == "assert" {
				hasAssert = true
			}
		}
	}
	if !hasFile || !hasAssert {
		t.Errorf("scaffolded plan must contain at least one file and one assert operation (file=%v assert=%v)", hasFile, hasAssert)
	}
}
