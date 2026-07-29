package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestSDDPanelRendersPlanAndBranch(t *testing.T) {
	p := session.SDDProgress{
		Active:      true,
		PlanName:    "my-plan",
		Branch:      "pipeline/my-plan",
		TotalTasks:  3,
		CurrentTask: 1,
		Phase:       "implementing",
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "my-plan") || !strings.Contains(out, "pipeline/my-plan") {
		t.Errorf("panel missing plan/branch: %q", out)
	}
	if !strings.Contains(out, "task 1/3") || !strings.Contains(out, "implementing") {
		t.Errorf("panel missing task/phase: %q", out)
	}
}

func TestSDDPanelShowsFixRound(t *testing.T) {
	p := session.SDDProgress{
		Active:       true,
		PlanName:     "p",
		TotalTasks:   2,
		CurrentTask:  1,
		Phase:        "fixing",
		FixRound:     1,
		MaxFixRounds: 3,
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "fix 1/3") {
		t.Errorf("panel missing fix round: %q", out)
	}
}

func TestSDDPanelEmptyWhenInactive(t *testing.T) {
	p := session.SDDProgress{Active: false}
	out := sddPanel(p, 60)
	if out != "" {
		t.Errorf("panel should be empty when inactive, got %q", out)
	}
}

func TestSDDPanelShowsDetailAndLedger(t *testing.T) {
	p := session.SDDProgress{
		Active:      true,
		PlanName:    "p",
		TotalTasks:  1,
		CurrentTask: 1,
		Phase:       "verifying",
		Detail:      "go build ./...",
		LastLedger:  "Task 1: gate passed",
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "go build") {
		t.Errorf("panel missing detail: %q", out)
	}
	if !strings.Contains(out, "gate passed") {
		t.Errorf("panel missing ledger line: %q", out)
	}
}

func TestSDDPanelShowsTokenUsage(t *testing.T) {
	p := session.SDDProgress{
		Active:      true,
		PlanName:    "p",
		TotalTasks:  2,
		CurrentTask: 1,
		TokensUsed:  150,
		TokensMax:   4000,
	}
	out := sddPanel(p, 60)
	if !strings.Contains(out, "tokens 150/4000") {
		t.Errorf("panel missing token usage: %q", out)
	}
}
