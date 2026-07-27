package sidepanel

import (
	"strings"
	"testing"
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

func TestChangedSectionOneLine(t *testing.T) {
	got := StripANSI((ChangedSection{}).OneLine(changedData(), 40))
	if !strings.Contains(got, "3") {
		t.Errorf("OneLine = %q, want the file count", got)
	}
	if !strings.Contains(got, "149") {
		t.Errorf("OneLine = %q, want total added (42+11+96=149)", got)
	}
}
