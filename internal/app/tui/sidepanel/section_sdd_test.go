package sidepanel

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
)

func sddData() Data {
	return Data{
		Spinner: "⠋",
		SDD: session.SDDProgress{
			Active:      true,
			DoneTasks:   2,
			TotalTasks:  4,
			CurrentTask: 3,
			Phase:       "implementing",
			Detail:      "src/auth.go",
			PlanName:    "my-plan",
			Branch:      "pipeline/my-plan",
			Tasks:       []string{"Scaffold config", "Add theme slots", "Consolidate run panel", "Remove live strip"},
		},
	}
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
	finished := Data{SDD: session.SDDProgress{Finished: true}}
	if !(SDDSection{}).Relevant(finished) {
		t.Error("Relevant(finished run) = false, want true — the collapsed summary still shows")
	}
}

func TestSDDSectionRendersSummaryAndChecklist(t *testing.T) {
	got := StripANSI(strings.Join((SDDSection{}).Render(sddData(), 34, 12), "\n"))
	for _, want := range []string{"⠋", "task 3/4", "implementing", "src/auth.go",
		"✓ 1 Scaffold config", "✓ 2 Add theme slots", "▸ 3 Consolidate run panel", "· 4 Remove live strip"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}
}

func TestSDDSectionStaticGlyphWhenSpinnerGated(t *testing.T) {
	d := sddData()
	d.Spinner = ""
	got := StripANSI(strings.Join((SDDSection{}).Render(d, 34, 12), "\n"))
	if !strings.Contains(got, "▸ task 3/4") {
		t.Errorf("summary must fall back to the static ▸ glyph:\n%s", got)
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

func TestSDDSectionFinishedSuccess(t *testing.T) {
	started := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 30, 10, 23, 41, 0, time.UTC)
	d := Data{
		SDD: session.SDDProgress{
			Finished:   true,
			Succeeded:  true,
			DoneTasks:  4,
			TotalTasks: 4,
			Tasks:      []string{"A", "B", "C", "D"},
			StartedAt:  started,
			EndedAt:    ended,
		},
	}
	got := StripANSI(strings.Join((SDDSection{}).Render(d, 50, 12), "\n"))
	want := " ✓ sdd done — 4/4 tasks · 23m 41s"
	if got != want {
		t.Errorf("Render(finished success) =\n  %q\nwant\n  %q", got, want)
	}
}

func TestSDDSectionFinishedFailure(t *testing.T) {
	started := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 30, 10, 0, 12, 0, time.UTC)
	d := Data{
		SDD: session.SDDProgress{
			Finished:   true,
			Succeeded:  false,
			DoneTasks:  2,
			TotalTasks: 4,
			Tasks:      []string{"A", "B", "C", "D"},
			StartedAt:  started,
			EndedAt:    ended,
		},
	}
	got := StripANSI(strings.Join((SDDSection{}).Render(d, 50, 12), "\n"))
	want := " ✗ sdd stopped — 2/4 tasks · 12s — /sdd to resume"
	if got != want {
		t.Errorf("Render(finished failure) =\n  %q\nwant\n  %q", got, want)
	}
}

func TestSDDSectionFinishedFailureWithReason(t *testing.T) {
	started := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 30, 10, 0, 12, 0, time.UTC)
	d := Data{
		SDD: session.SDDProgress{
			Finished:   true,
			Succeeded:  false,
			DoneTasks:  2,
			TotalTasks: 4,
			Tasks:      []string{"A", "B", "C", "D"},
			StartedAt:  started,
			EndedAt:    ended,
			Error:      "transient timeout",
		},
	}
	got := StripANSI(strings.Join((SDDSection{}).Render(d, 80, 12), "\n"))
	want := " ✗ sdd stopped — 2/4 tasks · 12s · transient timeout — /sdd to resume"
	if got != want {
		t.Errorf("Render(finished failure with reason) =\n  %q\nwant\n  %q", got, want)
	}
}

func TestSDDSectionFinishedFailureTruncatesHintFirst(t *testing.T) {
	started := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 30, 10, 0, 12, 0, time.UTC)
	d := Data{
		SDD: session.SDDProgress{
			Finished:   true,
			Succeeded:  false,
			DoneTasks:  2,
			TotalTasks: 4,
			StartedAt:  started,
			EndedAt:    ended,
			Error:      "transient timeout",
		},
	}
	got := StripANSI(strings.Join((SDDSection{}).Render(d, 55, 12), "\n"))
	want := " ✗ sdd stopped — 2/4 tasks · 12s · transient timeout"
	if got != want {
		t.Errorf("Render(narrow finished failure) =\n  %q\nwant\n  %q", got, want)
	}
}
