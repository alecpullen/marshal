package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/pipeline"
)

func TestScaffoldWritesAParseablePlan(t *testing.T) {
	m := newTestModelInRepo(t)

	m.scaffoldSDDPlan()

	path := filepath.Join(m.state.WorkingDir, m.state.Config.SDD.PlansDir, "starter-plan.md")
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

func TestScaffoldTellsTheUserWhereItWrote(t *testing.T) {
	m := newTestModelInRepo(t)
	m.scaffoldSDDPlan()

	var found bool
	for _, msg := range m.state.Messages() {
		if strings.Contains(msg.Content, "starter-plan.md") {
			found = true
		}
	}
	if !found {
		t.Error("the user must be told the path that was written")
	}
}

func TestScaffoldDoesNotClobberAnExistingFile(t *testing.T) {
	m := newTestModelInRepo(t)
	dir := filepath.Join(m.state.WorkingDir, m.state.Config.SDD.PlansDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "starter-plan.md")
	if err := os.WriteFile(path, []byte("PRIOR CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.scaffoldSDDPlan()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PRIOR CONTENT" {
		t.Error("scaffold must never overwrite an existing plan")
	}
}
