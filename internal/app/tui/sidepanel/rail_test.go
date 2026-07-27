package sidepanel

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// fakeSection is a Section with fully controllable geometry.
type fakeSection struct {
	id        string
	title     string
	priority  int
	body      []string
	oneLine   string
	clippable bool
	relevant  bool
}

func (f fakeSection) ID() string               { return f.id }
func (f fakeSection) Title() string            { return f.title }
func (f fakeSection) Priority() int            { return f.priority }
func (f fakeSection) Relevant(Data) bool       { return f.relevant }
func (f fakeSection) Clippable() bool          { return f.clippable }
func (f fakeSection) OneLine(Data, int) string { return f.oneLine }
func (f fakeSection) Render(_ Data, _, maxRows int) []string {
	if maxRows > 0 && len(f.body) > maxRows {
		return f.body[:maxRows]
	}
	return f.body
}

func mk(id string, prio int, rows int) fakeSection {
	body := make([]string, rows)
	for i := range body {
		body[i] = id + "-row"
	}
	return fakeSection{
		id: id, title: strings.ToUpper(id), priority: prio,
		body: body, oneLine: id + " summary", relevant: true,
	}
}

func TestRailViewDimensions(t *testing.T) {
	r := New(mk("alpha", 0, 3), mk("beta", 1, 2))
	out := r.View(Data{}, 30, 20)

	lines := strings.Split(out, "\n")
	if len(lines) != 20 {
		t.Fatalf("got %d rows, want 20", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 30 {
			t.Errorf("row %d width = %d, want 30: %q", i, w, StripANSI(l))
		}
	}
}

func TestRailViewDividerOnEveryRow(t *testing.T) {
	r := New(mk("alpha", 0, 3))
	for i, l := range strings.Split(r.View(Data{}, 30, 12), "\n") {
		if plain := StripANSI(l); !strings.HasPrefix(plain, "│") {
			t.Errorf("row %d = %q, want a │ prefix", i, plain)
		}
	}
}

func TestRailViewSkipsIrrelevantSections(t *testing.T) {
	hidden := mk("beta", 1, 2)
	hidden.relevant = false
	out := StripANSI(New(mk("alpha", 0, 3), hidden).View(Data{}, 30, 20))
	if strings.Contains(out, "BETA") {
		t.Errorf("irrelevant section rendered:\n%s", out)
	}
	if !strings.Contains(out, "ALPHA") {
		t.Errorf("relevant section missing:\n%s", out)
	}
}

func TestRailViewCollapsesUnderPressure(t *testing.T) {
	// alpha 4 rows (+1 title), beta 4 rows (+1 title), 1 blank = 11.
	// A budget of 8 forces beta (priority 1) to its one-line form.
	out := StripANSI(New(mk("alpha", 0, 4), mk("beta", 1, 4)).View(Data{}, 30, 8))
	if !strings.Contains(out, "beta summary") {
		t.Errorf("beta not collapsed to one-line:\n%s", out)
	}
	if strings.Contains(out, "beta-row") {
		t.Errorf("beta still expanded:\n%s", out)
	}
	if !strings.Contains(out, "alpha-row") {
		t.Errorf("alpha should stay expanded:\n%s", out)
	}
}

func TestRailViewEmptyWhenNoRelevantSections(t *testing.T) {
	s := mk("alpha", 0, 3)
	s.relevant = false
	if out := New(s).View(Data{}, 30, 20); out != "" {
		t.Errorf("View = %q, want empty", out)
	}
}

func TestRailViewDegenerateSizes(t *testing.T) {
	r := New(mk("alpha", 0, 3))
	for _, tc := range []struct{ w, h int }{{0, 20}, {2, 20}, {30, 0}, {-5, -5}} {
		if out := r.View(Data{}, tc.w, tc.h); out != "" {
			t.Errorf("View(w=%d,h=%d) = %q, want empty", tc.w, tc.h, out)
		}
	}
}

func TestHeaderRightAligns(t *testing.T) {
	got := StripANSI(Header("CHANGED", "3", 20))
	if ansi.StringWidth(got) != 20 {
		t.Errorf("width = %d, want 20: %q", ansi.StringWidth(got), got)
	}
	if !strings.HasPrefix(got, "CHANGED") {
		t.Errorf("got %q, want a CHANGED prefix", got)
	}
	if !strings.HasSuffix(got, "3") {
		t.Errorf("got %q, want a 3 suffix", got)
	}
}

func TestHeaderDropsRightWhenNoRoom(t *testing.T) {
	got := StripANSI(Header("VERYLONGSECTIONTITLE", "12345", 10))
	if ansi.StringWidth(got) != 10 {
		t.Errorf("width = %d, want 10: %q", ansi.StringWidth(got), got)
	}
}
