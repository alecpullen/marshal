package tui

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestRenderSwarmPanelShowsRolesAndStatus(t *testing.T) {
	p := session.SwarmProgress{
		Goal:   "add a regression test",
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRoleDone},
			{Name: "scouts", Status: session.SwarmRoleDone, Detail: "3/3"},
			{Name: "implementer", Status: session.SwarmRoleActive, Detail: "round 2/3"},
			{Name: "tester", Status: session.SwarmRolePending},
			{Name: "reviewer", Status: session.SwarmRolePending},
		},
	}
	out := renderSwarmPanel(p, "*", 60)

	for _, want := range []string{"add a regression test", "planner", "implementer", "round 2/3", "tester", "reviewer"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q\n%s", want, out)
		}
	}
}

func TestRenderSwarmPanelInactiveIsEmpty(t *testing.T) {
	if out := renderSwarmPanel(session.SwarmProgress{Active: false}, "*", 60); out != "" {
		t.Errorf("inactive panel should render empty, got %q", out)
	}
}

func TestSwarmPanelRowsReservedOnlyWhenActive(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	if got := m.swarmPanelRows(); got != 0 {
		t.Fatalf("inactive swarmPanelRows = %d, want 0", got)
	}
	m.state.SetSwarmProgress(session.SwarmProgress{Active: true})
	if got := m.swarmPanelRows(); got != swarmPanelRows {
		t.Fatalf("active swarmPanelRows = %d, want %d", got, swarmPanelRows)
	}
}

func TestAgentFinishedReleasesSwarmPanelReservation(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	fullHeight := m.viewport.Height()
	m.state.SetSwarmProgress(session.SwarmProgress{Active: true})
	m.updateViewportHeight()
	if m.viewport.Height() != fullHeight-swarmPanelRows {
		t.Fatalf("active viewport height = %d, want %d", m.viewport.Height(), fullHeight-swarmPanelRows)
	}

	m.state.ClearSwarmProgress()
	updated, _ := m.Update(agentFinishedMsg{})
	m = updated.(Model)
	if m.viewport.Height() != fullHeight {
		t.Fatalf("finished viewport height = %d, want restored %d", m.viewport.Height(), fullHeight)
	}
}

func TestRenderSwarmPanelShowsTokenBudget(t *testing.T) {
	p := session.SwarmProgress{
		Goal:   "refactor auth",
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRoleDone},
			{Name: "scouts", Status: session.SwarmRolePending},
			{Name: "implementer", Status: session.SwarmRolePending},
			{Name: "tester", Status: session.SwarmRolePending},
			{Name: "reviewer", Status: session.SwarmRolePending},
		},
		TokensUsed: 4200,
		TokensMax:  120000,
	}
	out := renderSwarmPanel(p, "*", 60)

	if !strings.Contains(out, "Tokens:") {
		t.Fatalf("panel missing token budget line:\n%s", out)
	}
	if !strings.Contains(out, "4200") {
		t.Fatalf("panel missing used token count:\n%s", out)
	}
	if !strings.Contains(out, "120000") {
		t.Fatalf("panel missing max token count:\n%s", out)
	}
}

func TestRenderSwarmPanelHidesTokensWhenZero(t *testing.T) {
	p := session.SwarmProgress{
		Goal:   "refactor auth",
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRolePending},
		},
	}
	out := renderSwarmPanel(p, "*", 60)

	if strings.Contains(out, "Tokens:") {
		t.Fatalf("panel should not show token line when both are zero:\n%s", out)
	}
}

func TestSwarmPanelEmitsOneLinePerRole(t *testing.T) {
	p := session.SwarmProgress{
		Goal:   "test",
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "implementer", Status: session.SwarmRoleActive},
			{Name: "tester", Status: session.SwarmRolePending},
		},
	}
	out := renderSwarmPanel(p, "*", 60)
	if got, want := strings.Count(out, "\n"), 2; got != want {
		t.Fatalf("expected %d newlines, got %d (%q)", want, got, out)
	}
}

func TestSwarmPanelIsBorderless(t *testing.T) {
	p := session.SwarmProgress{Active: true, Goal: "build", Roles: []session.SwarmRole{{Name: "coder", Status: session.SwarmRoleActive}}}
	out := renderSwarmPanel(p, "⠋", 60)
	if strings.Contains(out, "╭") || strings.Contains(out, "╰") {
		t.Fatalf("swarm panel should have no border:\n%s", out)
	}
}
