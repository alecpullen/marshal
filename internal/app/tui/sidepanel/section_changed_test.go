package sidepanel

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func changedData() Data {
	return Data{Changed: []ChangedFile{
		{Path: "internal/llm/routing/resolve.go", Status: 'M', Added: 42, Removed: 8},
		{Path: "internal/llm/routing/route.go", Status: 'M', Added: 11},
		{Path: "internal/llm/routing/route_test.go", Status: 'A', Added: 96},
	}}
}

func TestChangedSectionIdentity(t *testing.T) {
	s := ChangedSection{}
	if s.ID() != "changed" {
		t.Errorf("ID = %q, want changed", s.ID())
	}
	if s.Priority() != 2 {
		t.Errorf("Priority = %d, want 2", s.Priority())
	}
	if !s.Clippable() {
		t.Error("Clippable = false, want true (it is a list)")
	}
}

func TestChangedSectionRelevance(t *testing.T) {
	if (ChangedSection{}).Relevant(Data{}) {
		t.Error("Relevant(no changes) = true, want false")
	}
	if !(ChangedSection{}).Relevant(changedData()) {
		t.Error("Relevant(changes) = false, want true")
	}
}

func TestChangedSectionRender(t *testing.T) {
	got := StripANSI(strings.Join((ChangedSection{}).Render(changedData(), 34, 10), "\n"))
	if !strings.Contains(got, "resolve.go") {
		t.Errorf("missing basename:\n%s", got)
	}
	if !strings.Contains(got, "+42") || !strings.Contains(got, "-8") {
		t.Errorf("missing line counts:\n%s", got)
	}
}

func TestChangedSectionKeepsBasenameWhenNarrow(t *testing.T) {
	got := StripANSI(strings.Join((ChangedSection{}).Render(changedData(), 22, 10), "\n"))
	if !strings.Contains(got, "resolve.go") {
		t.Errorf("basename lost at 22 cols:\n%s", got)
	}
}

func TestChangedSectionRespectsMaxRows(t *testing.T) {
	if got := (ChangedSection{}).Render(changedData(), 34, 2); len(got) > 2 {
		t.Errorf("got %d rows, want at most 2", len(got))
	}
}

func TestChangedSectionColorsLineCounts(t *testing.T) {
	d := Data{Changed: []ChangedFile{
		{Path: "model.go", Status: 'M', Added: 42, Removed: 8},
	}}
	rows := ChangedSection{}.Render(d, 30, 0)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}

	// Visible text is unchanged.
	visible := StripANSI(rows[0])
	if !strings.Contains(visible, "+42") || !strings.Contains(visible, "-8") {
		t.Fatalf("visible text lost counts: %q", visible)
	}

	// The counts carry distinct styling from each other.
	if !strings.Contains(rows[0], StripANSI(styleSuccess("+42"))) {
		t.Errorf("row missing +42: %q", visible)
	}
	if rows[0] == visible {
		t.Errorf("row is unstyled, want status color on counts: %q", visible)
	}
	if styleSuccess("+42") == styleError("+42") {
		// Guard: success and error must be distinguishable styles.
		if strings.Count(rows[0], "\x1b[") < 2 {
			t.Errorf("want separate styling for additions and deletions: %q", rows[0])
		}
	}
}

func TestChangedSectionRowWidthUnaffectedByStyling(t *testing.T) {
	d := Data{Changed: []ChangedFile{
		{Path: "internal/app/tui/sidepanel/section_changed.go", Status: 'M', Added: 42, Removed: 8},
	}}
	rows := ChangedSection{}.Render(d, 24, 0)
	for i, r := range rows {
		if w := ansi.StringWidth(r); w > 24 {
			t.Errorf("row %d width = %d, want <= 24: %q", i, w, StripANSI(r))
		}
	}
}

func TestChangedSectionOneLine(t *testing.T) {
	got := StripANSI((ChangedSection{}).OneLine(changedData(), 40))
	if !strings.Contains(got, "3") {
		t.Errorf("OneLine = %q, want the file count", got)
	}
	if !strings.Contains(got, "149") {
		t.Errorf("OneLine = %q, want total added (42+11+96=149)", got)
	}
}
