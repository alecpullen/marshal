package sdd

import (
	"os"
	"path/filepath"
	"testing"
)

const samplePlan = `# Feature Plan Implementation Plan

**Goal:** Add a feature.

## Global Constraints

- Go 1.24+
- No new dependencies
- TDD

---

### Task 1: Hook installation

**Files:**
- Create: src/hook.go

- [ ] **Step 1: Write the failing test**

` + "```go" + `
func testHook() {}
` + "```" + `

- [ ] **Step 2: Run it**

### Task 2: Recovery modes

**Files:**
- Modify: src/hook.go

This task depends on Task 1.
`

func TestParsePlanExtractsTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(samplePlan), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if plan.Title != "Feature Plan Implementation Plan" {
		t.Errorf("Title = %q, want %q", plan.Title, "Feature Plan Implementation Plan")
	}
	if !contains(plan.GlobalConstraints, "Go 1.24+") {
		t.Errorf("GlobalConstraints missing Go 1.24+: %q", plan.GlobalConstraints)
	}
	if len(plan.Tasks) != 2 {
		t.Fatalf("Tasks = %d, want 2", len(plan.Tasks))
	}
	if plan.Tasks[0].Number != 1 || plan.Tasks[0].Title != "Hook installation" {
		t.Errorf("Task 0 = %+v", plan.Tasks[0])
	}
	if plan.Tasks[1].Number != 2 || plan.Tasks[1].Title != "Recovery modes" {
		t.Errorf("Task 1 = %+v", plan.Tasks[1])
	}
	if !contains(plan.Tasks[0].Body, "Write the failing test") {
		t.Errorf("Task 0 body missing step text: %q", plan.Tasks[0].Body)
	}
}

func TestParsePlanIgnoresHeadingsInFencedBlocks(t *testing.T) {
	planText := `# Plan

## Global Constraints

- C1

---

### Task 1: Real task

` + "```" + `
### Task 99: Fake task inside fence
` + "```" + `

Done.
`
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(planText), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("Tasks = %d, want 1 (fenced heading must be ignored)", len(plan.Tasks))
	}
	if plan.Tasks[0].Number != 1 {
		t.Errorf("Task number = %d, want 1", plan.Tasks[0].Number)
	}
}

func TestParsePlanNoTasksReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte("# Plan\n\n## Global Constraints\n\n- C1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ParsePlan(path)
	if err == nil {
		t.Fatal("ParsePlan should error when no tasks found")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
