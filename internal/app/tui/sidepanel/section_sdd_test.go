package sidepanel

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func sddData() Data {
	return Data{SDD: session.SDDProgress{
		Active:          true,
		DoneTasks:       2,
		TotalTasks:      4,
		ControllerState: "implement",
		Tasks: []session.SDDTaskStatus{
			{Name: "scaffold resolver", Phase: session.SDDPhaseDone},
			{Name: "add role profiles", Phase: session.SDDPhaseDone},
			{Name: "wire route lookup", Phase: session.SDDPhaseActive},
			{Name: "tests", Phase: session.SDDPhasePending},
		},
	}}
}

func TestSDDSectionIdentity(t *testing.T) {
	s := SDDSection{}
	if s.ID() != "sdd" {
		t.Errorf("ID = %q, want sdd", s.ID())
	}
	if s.Priority() != 0 {
		t.Errorf("Priority = %d, want 0", s.Priority())
	}
	if !s.Clippable() {
		t.Error("Clippable = false, want true")
	}
}

func TestSDDSectionRelevance(t *testing.T) {
	if (SDDSection{}).Relevant(Data{}) {
		t.Error("Relevant(no run) = true, want false")
	}
	if !(SDDSection{}).Relevant(sddData()) {
		t.Error("Relevant(active run) = false, want true")
	}
}

func TestSDDSectionRendersTasks(t *testing.T) {
	got := StripANSI(strings.Join((SDDSection{}).Render(sddData(), 34, 12), "\n"))
	for _, want := range []string{"scaffold resolver", "wire route lookup", "tests", "implement"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}
}

func TestSDDSectionRespectsMaxRows(t *testing.T) {
	if got := (SDDSection{}).Render(sddData(), 34, 3); len(got) > 3 {
		t.Errorf("got %d rows, want at most 3", len(got))
	}
}

func TestSDDSectionOneLine(t *testing.T) {
	got := StripANSI((SDDSection{}).OneLine(sddData(), 40))
	if !strings.Contains(got, "2/4") {
		t.Errorf("OneLine = %q, want 2/4", got)
	}
	if !strings.Contains(got, "implement") {
		t.Errorf("OneLine = %q, want the controller state", got)
	}
}
