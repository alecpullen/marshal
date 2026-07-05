package swarm

import (
	"strings"
	"sync"
	"testing"
)

func TestTaskStateRenderIncludesPopulatedSectionsOnly(t *testing.T) {
	ts := NewTaskState("fix the parser")

	rendered := ts.Render()
	if !strings.Contains(rendered, "Goal: fix the parser") {
		t.Fatalf("Render missing goal, got:\n%s", rendered)
	}
	for _, absent := range []string{"Plan:", "Findings:", "Changes made:", "Review:"} {
		if strings.Contains(rendered, absent) {
			t.Fatalf("Render should omit empty section %q, got:\n%s", absent, rendered)
		}
	}

	ts.SetPlan([]string{"1. read parser.go", "2. patch it"})
	ts.AddFinding(Finding{Agent: "repo_scout", Area: "tests", Content: "parser_test.go covers Parse"})
	ts.AddPatchNote("fixed off-by-one in Parse")
	ts.SetFinalSummary("APPROVE")

	rendered = ts.Render()
	for _, want := range []string{
		"Plan:", "1. read parser.go",
		"Findings:", "[repo_scout/tests] parser_test.go covers Parse",
		"Changes made:", "fixed off-by-one in Parse",
		"Review:", "APPROVE",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Render missing %q, got:\n%s", want, rendered)
		}
	}
}

func TestTaskStateAccessorsReturnCopies(t *testing.T) {
	ts := NewTaskState("goal")
	ts.SetPlan([]string{"step"})
	plan := ts.Plan()
	plan[0] = "mutated"
	if got := ts.Plan()[0]; got != "step" {
		t.Fatalf("Plan() must return a copy; internal state became %q", got)
	}
}

func TestTaskStateIsSafeForConcurrentWrites(t *testing.T) {
	ts := NewTaskState("goal")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts.AddFinding(Finding{Agent: "repo_scout", Area: "code", Content: "x"})
			_ = ts.Render()
		}()
	}
	wg.Wait()
	if got := len(ts.Findings()); got != 8 {
		t.Fatalf("Findings length = %d, want 8", got)
	}
}
