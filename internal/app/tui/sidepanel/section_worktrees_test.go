package sidepanel

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/worktree"
)

func fleetData() Data {
	return Data{Fleet: []worktree.FleetRow{
		{Path: "/repo/.marshal/worktrees/feat-x", Branch: "feat/x", Ahead: 2, Behind: 1, Dirty: true, Age: 2 * time.Hour},
		{Path: "/repo/.marshal/worktrees/feat-y", Branch: "feat/y", Ahead: 0, Behind: 0, Age: 30 * time.Minute},
	}}
}

func TestWorktreesSectionIdentity(t *testing.T) {
	s := WorktreesSection{}
	if s.ID() != "worktrees" {
		t.Errorf("ID = %q, want worktrees", s.ID())
	}
	if s.Title() != "WORKTREES" {
		t.Errorf("Title = %q, want WORKTREES", s.Title())
	}
	if !s.Clippable() {
		t.Error("Clippable = false, want true (it is a list)")
	}
}

func TestWorktreesSectionRelevance(t *testing.T) {
	if (WorktreesSection{}).Relevant(Data{}) {
		t.Error("Relevant(no fleet) = true, want false")
	}
	if !(WorktreesSection{}).Relevant(fleetData()) {
		t.Error("Relevant(fleet) = false, want true")
	}
}

func TestWorktreesSectionRender(t *testing.T) {
	got := StripANSI(strings.Join((WorktreesSection{}).Render(fleetData(), 34, 10), "\n"))
	if !strings.Contains(got, "feat/x") {
		t.Errorf("missing branch:\n%s", got)
	}
	if !strings.Contains(got, "↑2") || !strings.Contains(got, "↓1") {
		t.Errorf("missing ahead/behind:\n%s", got)
	}
	if !strings.Contains(got, "2h") {
		t.Errorf("missing human age:\n%s", got)
	}
}

func TestWorktreesSectionMarksDirty(t *testing.T) {
	rows := (WorktreesSection{}).Render(fleetData(), 34, 0)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// The dirty row carries a visible marker the clean row lacks.
	if !strings.Contains(StripANSI(rows[0]), "●") {
		t.Errorf("dirty row missing marker: %q", StripANSI(rows[0]))
	}
	if strings.Contains(StripANSI(rows[1]), "●") {
		t.Errorf("clean row has a marker: %q", StripANSI(rows[1]))
	}
}

func TestWorktreesSectionRespectsMaxRows(t *testing.T) {
	if got := (WorktreesSection{}).Render(fleetData(), 34, 1); len(got) > 1 {
		t.Errorf("got %d rows, want at most 1", len(got))
	}
}

func TestWorktreesSectionRowWidthUnaffectedByStyling(t *testing.T) {
	rows := (WorktreesSection{}).Render(fleetData(), 24, 0)
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > 24 {
			t.Errorf("row %d width = %d, want <= 24: %q", i, w, StripANSI(r))
		}
	}
}

func TestWorktreesSectionOneLine(t *testing.T) {
	got := StripANSI((WorktreesSection{}).OneLine(fleetData(), 40))
	if !strings.Contains(got, "2 worktrees") {
		t.Errorf("OneLine = %q, want the worktree count", got)
	}
	if !strings.Contains(got, "1 dirty") {
		t.Errorf("OneLine = %q, want the dirty count", got)
	}
}
