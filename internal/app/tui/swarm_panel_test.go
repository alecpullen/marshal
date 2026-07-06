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
	fullHeight := m.viewport.Height
	m.state.SetSwarmProgress(session.SwarmProgress{Active: true})
	m.updateViewportHeight()
	if m.viewport.Height != fullHeight-swarmPanelRows {
		t.Fatalf("active viewport height = %d, want %d", m.viewport.Height, fullHeight-swarmPanelRows)
	}

	m.state.ClearSwarmProgress()
	updated, _ := m.Update(agentFinishedMsg{})
	m = updated.(Model)
	if m.viewport.Height != fullHeight {
		t.Fatalf("finished viewport height = %d, want restored %d", m.viewport.Height, fullHeight)
	}
}
