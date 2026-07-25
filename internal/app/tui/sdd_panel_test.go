package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestSDDPanelRendersTaskRows(t *testing.T) {
	p := session.SDDProgress{
		Active:          true,
		PlanName:        "my-plan",
		ControllerState: "DRAIN_ITERATION",
		TotalTasks:      3,
		DoneTasks:       1,
		Tasks: []session.SDDTaskStatus{
			{Name: "T1", Phase: session.SDDPhaseDone},
			{Name: "T2", Phase: session.SDDPhaseActive, Implementer: session.SDDPhaseActive, FixRound: 1, MaxFixes: 3},
			{Name: "T3", Phase: session.SDDPhasePending},
		},
		BranchReview: session.SDDPhasePending,
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "T1") || !strings.Contains(out, "T2") || !strings.Contains(out, "T3") {
		t.Errorf("panel missing task rows: %q", out)
	}
	if !strings.Contains(out, "drain_iteration") {
		t.Errorf("panel missing controller state: %q", out)
	}
}

func TestSDDPanelShowsRetryCount(t *testing.T) {
	p := session.SDDProgress{
		Active:          true,
		PlanName:        "my-plan",
		ControllerState: "DRAIN_ITERATION",
		TotalTasks:      2,
		DoneTasks:       0,
		Tasks: []session.SDDTaskStatus{
			{Name: "T1", Phase: session.SDDPhaseActive, Implementer: session.SDDPhaseActive, FixRound: 1, MaxFixes: 3},
			{Name: "T2", Phase: session.SDDPhaseDone},
		},
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "retry 1/3") {
		t.Errorf("panel missing retry count for T1: %q", out)
	}
}

func TestSDDPanelEmptyWhenInactive(t *testing.T) {
	p := session.SDDProgress{Active: false}
	out := sddPanel(p, 60)
	if out != "" {
		t.Errorf("panel should be empty when inactive, got %q", out)
	}
}

func TestSDDPanelShowsActiveSubPhase(t *testing.T) {
	p := session.SDDProgress{
		Active:          true,
		PlanName:        "p",
		ControllerState: "RUNNING",
		TotalTasks:      1,
		DoneTasks:       0,
		Tasks: []session.SDDTaskStatus{
			{Name: "T1", Phase: session.SDDPhaseActive, Implementer: session.SDDPhaseActive},
		},
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "implementer") {
		t.Errorf("panel missing implementer sub-phase: %q", out)
	}
}

func TestActiveSubPhase(t *testing.T) {
	cases := []struct {
		name string
		task session.SDDTaskStatus
		want string
	}{
		{"implementer", session.SDDTaskStatus{Phase: session.SDDPhaseActive, Implementer: session.SDDPhaseActive}, "implementer"},
		{"audit", session.SDDTaskStatus{Phase: session.SDDPhaseActive, Audit: session.SDDPhaseActive}, "audit"},
		{"review", session.SDDTaskStatus{Phase: session.SDDPhaseActive, Review: session.SDDPhaseActive}, "review"},
		{"default active", session.SDDTaskStatus{Phase: session.SDDPhaseActive}, "active"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := activeSubPhase(c.task)
			if got != c.want {
				t.Errorf("activeSubPhase(%+v) = %q, want %q", c.task, got, c.want)
			}
		})
	}
}

func TestSDDPanelShowsBranchReview(t *testing.T) {
	p := session.SDDProgress{
		Active:          true,
		PlanName:        "p",
		ControllerState: "RUNNING",
		TotalTasks:      1,
		DoneTasks:       1,
		Tasks: []session.SDDTaskStatus{
			{Name: "T1", Phase: session.SDDPhaseDone},
		},
		BranchReview: session.SDDPhaseActive,
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "branch review: active") {
		t.Errorf("panel missing branch review: %q", out)
	}
}

func TestSDDPanelOmitsPendingBranchReview(t *testing.T) {
	p := session.SDDProgress{
		Active:          true,
		PlanName:        "p",
		ControllerState: "RUNNING",
		TotalTasks:      1,
		DoneTasks:       0,
		Tasks: []session.SDDTaskStatus{
			{Name: "T1", Phase: session.SDDPhaseActive, Implementer: session.SDDPhaseActive},
		},
		BranchReview: session.SDDPhasePending,
	}
	out := sddPanel(p, 60)
	if strings.Contains(out, "branch review") {
		t.Errorf("panel should omit pending branch review: %q", out)
	}
}

func TestSDDPanelShowsTokenUsage(t *testing.T) {
	p := session.SDDProgress{
		Active:          true,
		PlanName:        "p",
		ControllerState: "RUNNING",
		TotalTasks:      2,
		DoneTasks:       1,
		TokensUsed:      150,
		TokensMax:       4000,
		Tasks: []session.SDDTaskStatus{
			{Name: "T1", Phase: session.SDDPhaseDone},
			{Name: "T2", Phase: session.SDDPhasePending},
		},
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "tokens 150/4000") {
		t.Errorf("panel missing token usage: %q", out)
	}
}
