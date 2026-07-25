package sdd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePlanExtractsTasks(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	body := `# Some Plan

## Global Constraints

...

### Task 1: Foundation

content

### Task 2: Git ops

content

### Task 3: Verify

content
`
	if err := os.WriteFile(planPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	dag, err := ParsePlan(planPath)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(dag.Tasks) != 3 {
		t.Fatalf("Tasks len = %d, want 3", len(dag.Tasks))
	}
	if dag.Tasks[0].ID != "T1" || dag.Tasks[0].Title != "Foundation" {
		t.Errorf("T1 = %+v", dag.Tasks[0])
	}
	if dag.Tasks[1].ID != "T2" || dag.Tasks[1].Title != "Git ops" {
		t.Errorf("T2 = %+v", dag.Tasks[1])
	}
	if dag.Tasks[2].Status != TaskPending {
		t.Errorf("T3 Status = %q, want pending", dag.Tasks[2].Status)
	}
}

func TestParsePlanNoTasks(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan with no tasks\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dag, err := ParsePlan(planPath)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(dag.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0", len(dag.Tasks))
	}
}

func TestParsePlanMissingFile(t *testing.T) {
	_, err := ParsePlan("/nonexistent/plan.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
