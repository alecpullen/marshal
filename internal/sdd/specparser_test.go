package sdd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSpecExtractsTasks(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.md")

	var body strings.Builder
	body.WriteString("---\n")
	body.WriteString("status: approved\n")
	body.WriteString("source_plan: docs/plans/feature.md\n")
	body.WriteString("target_branch: main\n")
	body.WriteString("---\n")
	body.WriteString("\n")
	body.WriteString("# Feature Spec\n")
	body.WriteString("\n")
	body.WriteString("```yaml\n")
	body.WriteString("tasks:\n")
	body.WriteString("  - id: T1\n")
	body.WriteString("    title: Foundation\n")
	body.WriteString("    deps: []\n")
	body.WriteString("    files: [internal/sdd/types.go]\n")
	body.WriteString("    acceptance: [go build ./internal/sdd/]\n")
	body.WriteString("  - id: T2\n")
	body.WriteString("    title: Git ops\n")
	body.WriteString("    deps: [T1]\n")
	body.WriteString("    files: [internal/sdd/git.go]\n")
	body.WriteString("    acceptance: [go test ./internal/sdd/]\n")
	body.WriteString("```\n")

	if err := os.WriteFile(specPath, []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}
	dag, err := ParseSpec(specPath)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if len(dag.Tasks) != 2 {
		t.Fatalf("Tasks len = %d, want 2", len(dag.Tasks))
	}
	if dag.Tasks[0].ID != "T1" || dag.Tasks[0].Title != "Foundation" {
		t.Errorf("T1 = %+v", dag.Tasks[0])
	}
	if len(dag.Tasks[0].Files) != 1 || dag.Tasks[0].Files[0] != "internal/sdd/types.go" {
		t.Errorf("T1 Files = %v", dag.Tasks[0].Files)
	}
	if len(dag.Tasks[1].Deps) != 1 || dag.Tasks[1].Deps[0] != "T1" {
		t.Errorf("T2 Deps = %v", dag.Tasks[1].Deps)
	}
	if dag.Tasks[1].Status != TaskPending {
		t.Errorf("T2 Status = %q, want pending", dag.Tasks[1].Status)
	}
}

func TestParseSpecMissingFile(t *testing.T) {
	_, err := ParseSpec("/nonexistent/spec.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseSpecNoTasksBlock(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.md")

	var body strings.Builder
	body.WriteString("---\n")
	body.WriteString("status: approved\n")
	body.WriteString("source_plan: docs/plans/feature.md\n")
	body.WriteString("target_branch: main\n")
	body.WriteString("---\n")
	body.WriteString("\n")
	body.WriteString("# Spec with no tasks\n")

	if err := os.WriteFile(specPath, []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}
	dag, err := ParseSpec(specPath)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if len(dag.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0", len(dag.Tasks))
	}
}
