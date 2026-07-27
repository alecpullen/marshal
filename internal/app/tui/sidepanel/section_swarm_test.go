package sidepanel

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
)

func swarmData() Data {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	return Data{
		Now: now,
		Swarm: session.SwarmProgress{
			Active: true,
			Roles: []session.SwarmRole{
				{Name: "planner", Status: session.SwarmRoleDone, Tokens: 1200,
					StartedAt: now.Add(-74 * time.Second)},
				{Name: "implementer", Status: session.SwarmRoleActive, Detail: "patch resolve.go",
					Tokens: 8400, StartedAt: now.Add(-62 * time.Second)},
				{Name: "reviewer", Status: session.SwarmRolePending},
			},
		},
	}
}

func TestSwarmSectionIdentity(t *testing.T) {
	s := SwarmSection{}
	if s.ID() != "swarm" {
		t.Errorf("ID = %q, want swarm", s.ID())
	}
	if s.Priority() != 0 {
		t.Errorf("Priority = %d, want 0", s.Priority())
	}
	if !s.Clippable() {
		t.Error("Clippable = false, want true")
	}
}

func TestSwarmSectionRelevance(t *testing.T) {
	if (SwarmSection{}).Relevant(Data{}) {
		t.Error("Relevant(no swarm) = true, want false")
	}
	if !(SwarmSection{}).Relevant(swarmData()) {
		t.Error("Relevant(active swarm) = false, want true")
	}
	inactive := swarmData()
	inactive.Swarm.Active = false
	if (SwarmSection{}).Relevant(inactive) {
		t.Error("Relevant(inactive swarm) = true, want false")
	}
}

func TestSwarmSectionRendersRoles(t *testing.T) {
	got := StripANSI(strings.Join((SwarmSection{}).Render(swarmData(), 34, 12), "\n"))
	for _, want := range []string{"planner", "implementer", "reviewer", "8k", "1:02"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}
}

func TestSwarmSectionPendingRoleHasNoElapsed(t *testing.T) {
	got := StripANSI(strings.Join((SwarmSection{}).Render(swarmData(), 34, 12), "\n"))
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "reviewer") && strings.Contains(line, ":") {
			t.Errorf("pending role shows an elapsed time: %q", line)
		}
	}
}

func TestSwarmSectionOneLine(t *testing.T) {
	got := StripANSI((SwarmSection{}).OneLine(swarmData(), 44))
	if !strings.Contains(got, "1/3") {
		t.Errorf("OneLine = %q, want done/total = 1/3", got)
	}
	if !strings.Contains(got, "implementer") {
		t.Errorf("OneLine = %q, want the active role", got)
	}
}
