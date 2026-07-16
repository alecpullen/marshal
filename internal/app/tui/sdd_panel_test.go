package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestRenderSDDPanelShowsTasksAndStatus(t *testing.T) {
	p := session.SDDProgress{
		Active:     true,
		PlanName:   "feature-plan",
		TotalTasks: 3,
		Tasks: []session.SDDTaskStatus{
			{Name: "Hook", Phase: session.SDDPhaseDone, Implementer: session.SDDPhaseDone, Reviewer: session.SDDPhaseDone},
			{Name: "Recovery", Phase: session.SDDPhaseActive, Implementer: session.SDDPhaseDone, Reviewer: session.SDDPhaseActive, FixRound: 1, MaxFixes: 3, Detail: "fix 1/3"},
			{Name: "Cleanup", Phase: session.SDDPhasePending, Implementer: session.SDDPhasePending, Reviewer: session.SDDPhasePending, MaxFixes: 3},
		},
		BranchReview: session.SDDPhasePending,
	}
	out, rows := renderSDDPanel(p, "*", 60)

	for _, want := range []string{"feature-plan", "Hook", "Recovery", "Cleanup", "fix 1/3", "Branch review"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q\n%s", want, out)
		}
	}
	if rows < 1 {
		t.Errorf("active panel should report positive rows, got %d", rows)
	}
}

func TestRenderSDDPanelInactiveIsEmpty(t *testing.T) {
	out, rows := renderSDDPanel(session.SDDProgress{Active: false}, "*", 60)
	if out != "" {
		t.Errorf("inactive panel should render empty, got %q", out)
	}
	if rows != 0 {
		t.Errorf("inactive panel should report 0 rows, got %d", rows)
	}
}

func TestRenderSDDPanelShowsTokenBudget(t *testing.T) {
	p := session.SDDProgress{
		Active:     true,
		PlanName:   "test",
		TotalTasks: 1,
		Tasks:      []session.SDDTaskStatus{{Name: "T1", Phase: session.SDDPhaseActive, MaxFixes: 3}},
		TokensUsed: 4200,
		TokensMax:  90000,
	}
	out, _ := renderSDDPanel(p, "*", 60)
	if !strings.Contains(out, "Tokens:") {
		t.Fatalf("panel missing token budget line:\n%s", out)
	}
}

func TestRenderSDDPanelHidesTokensWhenZero(t *testing.T) {
	p := session.SDDProgress{
		Active:     true,
		PlanName:   "test",
		TotalTasks: 1,
		Tasks:      []session.SDDTaskStatus{{Name: "T1", Phase: session.SDDPhasePending, MaxFixes: 3}},
	}
	out, _ := renderSDDPanel(p, "*", 60)
	if strings.Contains(out, "Tokens:") {
		t.Fatalf("panel should not show token line when both are zero:\n%s", out)
	}
}

func TestSDDPanelHeightMatchesContent(t *testing.T) {
	_, rows := renderSDDPanel(session.SDDProgress{Active: true, Tasks: nil}, "*", 80)
	if rows > 3 {
		t.Fatalf("empty SDD panel reports too many rows: %d", rows)
	}
}
