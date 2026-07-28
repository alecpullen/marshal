package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Built by concatenation rather than as one raw string so that no line of
// this test file — or of the plan that carries it — begins with a task
// heading that a plan parser would pick up.
const samplePlan = "# Sample Plan\n\n" +
	"**Goal:** Do the thing.\n\n" +
	"## Global Constraints\n\n" +
	"- Constraint one.\n" +
	"- Constraint two.\n\n" +
	"---\n\n" +
	"### Task 1: First thing\n\n" +
	"**Files:**\n" +
	"- Create: `a.go`\n\n" +
	"- [ ] **Step 1: Write the failing test**\n\n" +
	"### Nested heading inside task 1\n\n" +
	"More body.\n\n" +
	"### Task 2: Second thing\n\n" +
	"Body of two.\n"

func TestParsePlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-07-27-sample-plan.md")
	if err := os.WriteFile(path, []byte(samplePlan), 0644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	p, err := ParsePlan(path)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if p.Slug != "2026-07-27-sample-plan" {
		t.Errorf("Slug = %q, want %q", p.Slug, "2026-07-27-sample-plan")
	}
	if len(p.Tasks) != 2 {
		t.Fatalf("len(Tasks) = %d, want 2", len(p.Tasks))
	}
	if p.Tasks[0].N != 1 || p.Tasks[0].Title != "First thing" {
		t.Errorf("Tasks[0] = %d/%q, want 1/%q", p.Tasks[0].N, p.Tasks[0].Title, "First thing")
	}
	if !strings.Contains(p.Tasks[0].Body, "Nested heading inside task 1") {
		t.Errorf("task 1 body dropped its nested ### heading:\n%s", p.Tasks[0].Body)
	}
	if strings.Contains(p.Tasks[0].Body, "Body of two") {
		t.Errorf("task 1 body leaked into task 2:\n%s", p.Tasks[0].Body)
	}
	if !strings.Contains(p.GlobalConstraints, "Constraint one.") ||
		!strings.Contains(p.GlobalConstraints, "Constraint two.") {
		t.Errorf("GlobalConstraints = %q", p.GlobalConstraints)
	}
	if strings.Contains(p.GlobalConstraints, "Task 1") {
		t.Errorf("GlobalConstraints ran past its section:\n%s", p.GlobalConstraints)
	}
}

func TestParsePlanNoTasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte("# Nothing here\n"), 0644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if _, err := ParsePlan(path); err == nil {
		t.Fatal("ParsePlan on a plan with no tasks: want error, got nil")
	}
}

func TestWriteBrief(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task-3-brief.md")
	spec := TaskSpec{N: 3, Title: "Third thing", Body: "### Task 3: Third thing\n\nDo it.\n"}
	if err := WriteBrief(path, spec); err != nil {
		t.Fatalf("WriteBrief: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	if string(data) != spec.Body {
		t.Errorf("brief = %q, want the task body verbatim %q", data, spec.Body)
	}
}
