package sidepanel

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func sddData() Data {
	return Data{SDD: session.SDDProgress{
		Active:      true,
		DoneTasks:   2,
		TotalTasks:  4,
		CurrentTask: 3,
		Phase:       "implementing",
		PlanName:    "my-plan",
		Branch:      "pipeline/my-plan",
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

func TestSDDSectionRendersPlanAndBranch(t *testing.T) {
	got := StripANSI(strings.Join((SDDSection{}).Render(sddData(), 34, 12), "\n"))
	for _, want := range []string{"my-plan", "pipeline/my-plan", "3/4", "implementing"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}
}

func TestSDDSectionRespectsMaxRows(t *testing.T) {
	if got := (SDDSection{}).Render(sddData(), 34, 1); len(got) > 1 {
		t.Errorf("got %d rows, want at most 1", len(got))
	}
}

func TestSDDSectionOneLine(t *testing.T) {
	got := StripANSI((SDDSection{}).OneLine(sddData(), 40))
	if !strings.Contains(got, "2/4") {
		t.Errorf("OneLine = %q, want 2/4", got)
	}
	if !strings.Contains(got, "implementing") {
		t.Errorf("OneLine = %q, want the phase", got)
	}
}
