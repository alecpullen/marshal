package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestScaffoldReportsAnExistingFile(t *testing.T) {
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
	var told bool
	for _, msg := range m.state.Messages() {
		if strings.Contains(msg.Content, "already exists") {
			told = true
		}
	}
	if !told {
		t.Error("the user must be told the plan already exists")
	}
}
