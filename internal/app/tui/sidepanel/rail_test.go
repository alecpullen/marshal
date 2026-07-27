package sidepanel

import (
	"reflect"
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

func TestDistributeGapsCapsAtMax(t *testing.T) {
	got := distributeGaps(4, 100)
	if len(got) != 3 {
		t.Fatalf("got %d gaps, want 3", len(got))
	}
	for i, g := range got {
		if g != maxGapRows {
			t.Errorf("gap %d = %d, want %d", i, g, maxGapRows)
		}
	}
}

func TestDistributeGapsFillsFrontToBack(t *testing.T) {
	// 3 sections, 2 gaps, 3 leftover rows, cap 2 → [2, 1].
	got := distributeGaps(3, 3)
	want := []int{2, 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDistributeGapsZeroLeftover(t *testing.T) {
	got := distributeGaps(3, 0)
	want := []int{0, 0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDistributeGapsSingleSection(t *testing.T) {
	if got := distributeGaps(1, 10); len(got) != 0 {
		t.Errorf("got %v, want no gaps for a single section", got)
	}
}

func TestRailSpreadsShortSectionsWithinCap(t *testing.T) {
	// Two 2-row sections in a 30-row rail: lots of leftover.
	r := New(mk("alpha", 0, 2), mk("beta", 1, 2))
	out := r.View(Data{}, 30, 30)
	lines := strings.Split(out, "\n")

	if len(lines) != 30 {
		t.Fatalf("got %d rows, want 30", len(lines))
	}

	// Find the two title rows and confirm the gap between the sections
	// never exceeds the cap plus the one baseline separator row.
	firstEnd, secondStart := -1, -1
	for i, l := range lines {
		v := strings.TrimSpace(StripANSI(l))
		if strings.Contains(v, "ALPHA") {
			firstEnd = i + 2 // title + 2 body rows
		}
		if strings.Contains(v, "BETA") {
			secondStart = i
		}
	}
	if firstEnd < 0 || secondStart < 0 {
		t.Fatalf("could not locate both sections in:\n%s", StripANSI(out))
	}
	gap := secondStart - firstEnd - 1
	if gap > maxGapRows+1 {
		t.Errorf("gap between sections = %d rows, want <= %d", gap, maxGapRows+1)
	}
}

func TestRailDistributionDoesNotChangeStates(t *testing.T) {
	// A rail too small for both expanded must still collapse identically
	// whether or not distribution runs.
	r := New(mk("alpha", 0, 5), mk("beta", 1, 5))
	out := StripANSI(r.View(Data{}, 30, 8))
	if !strings.Contains(out, "beta summary") {
		t.Errorf("expected beta collapsed to its one-line summary, got:\n%s", out)
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
	if !strings.HasSuffix(got, " 3") {
		t.Errorf("got %q, want a ' 3' suffix", got)
	}
}

func TestHeaderDropsRightWhenNoRoom(t *testing.T) {
	out := StripANSI(Header("CONTEXT", "78%", 9))
	if ansi.StringWidth(out) != 9 {
		t.Fatalf("width = %d, want 9: %q", ansi.StringWidth(out), out)
	}
	if strings.Contains(out, "78%") {
		t.Errorf("want right dropped at narrow width, got %q", out)
	}
}

func TestHeaderRendersRule(t *testing.T) {
	out := StripANSI(Header("CONTEXT", "", 20))
	if ansi.StringWidth(out) != 20 {
		t.Fatalf("width = %d, want 20: %q", ansi.StringWidth(out), out)
	}
	if !strings.HasPrefix(out, "CONTEXT ") {
		t.Errorf("want title then space, got %q", out)
	}
	if !strings.Contains(out, "─") {
		t.Errorf("want a rule, got %q", out)
	}
	if strings.TrimRight(out, "─") != "CONTEXT " {
		t.Errorf("want rule to run to the edge, got %q", out)
	}
}

func TestHeaderRuleSeparatesTitleFromRight(t *testing.T) {
	out := StripANSI(Header("CONTEXT", "78%", 20))
	if ansi.StringWidth(out) != 20 {
		t.Fatalf("width = %d, want 20: %q", ansi.StringWidth(out), out)
	}
	if !strings.HasPrefix(out, "CONTEXT ") {
		t.Errorf("want title first, got %q", out)
	}
	if !strings.HasSuffix(out, " 78%") {
		t.Errorf("want right value last, got %q", out)
	}
	if !strings.Contains(out, "─") {
		t.Errorf("want a rule between title and value, got %q", out)
	}
}

func TestHeaderTruncatesLongTitle(t *testing.T) {
	out := StripANSI(Header("VERYLONGSECTIONTITLE", "", 8))
	if ansi.StringWidth(out) != 8 {
		t.Fatalf("width = %d, want 8: %q", ansi.StringWidth(out), out)
	}
}

func TestHeaderZeroWidth(t *testing.T) {
	if got := Header("CONTEXT", "", 0); got != "" {
		t.Errorf("want empty string at width 0, got %q", got)
	}
}

// footerSection is a fake with the pinned footer's identity.
func footerSection(rows int) fakeSection {
	f := mk("session", 9, rows)
	f.title = ""
	f.oneLine = "session summary"
	return f
}

func TestFooterRendersFlushToBottom(t *testing.T) {
	r := New(mk("alpha", 0, 2), footerSection(2))
	lines := strings.Split(r.View(Data{}, 30, 20), "\n")
	if len(lines) != 20 {
		t.Fatalf("got %d rows, want 20", len(lines))
	}
	last := strings.TrimSpace(StripANSI(lines[19]))
	if !strings.Contains(last, "session-row") {
		t.Errorf("bottom row = %q, want the footer's last body row", last)
	}
}

func TestFooterFlushAtVariousHeights(t *testing.T) {
	for _, h := range []int{12, 20, 40} {
		r := New(mk("alpha", 0, 2), footerSection(2))
		lines := strings.Split(r.View(Data{}, 30, h), "\n")
		last := strings.TrimSpace(StripANSI(lines[h-1]))
		if !strings.Contains(last, "session-row") {
			t.Errorf("height %d: bottom row = %q, want footer body", h, last)
		}
	}
}

func TestFooterCollapsesToOneLineWhenTight(t *testing.T) {
	// alpha needs 6 rows expanded; footer needs 3. At height 8 something
	// must give, and the footer has the worst priority.
	r := New(mk("alpha", 0, 5), footerSection(2))
	out := StripANSI(r.View(Data{}, 30, 8))
	if !strings.Contains(out, "session summary") {
		t.Errorf("want footer collapsed to one-line, got:\n%s", out)
	}
}

func TestFooterDropsWhenNoRoom(t *testing.T) {
	// Two 2-row footers survive at height 4; one fit-collapsed footer
	// at height 2 only has room for one of them, so alpha's OneLine.
	// A footer at height 2 must give — and the footer is the worst
	// priority. Plan originally said height 6, but with the rail's
	// inter-section baseline + section natural heights that doesn't
	// force drop.
	r := New(mk("alpha", 0, 5), footerSection(2))
	out := StripANSI(r.View(Data{}, 30, 2))
	if strings.Contains(out, "session") {
		t.Errorf("want footer dropped at height 2, got:\n%s", out)
	}
	if !strings.Contains(out, "alpha summary") {
		t.Errorf("want alpha to survive, got:\n%s", out)
	}
}

func TestRailStructureSurvivesColorStripping(t *testing.T) {
	r := New(mk("alpha", 0, 2), mk("beta", 1, 2), footerSection(2))
	plain := StripANSI(r.View(Data{}, 30, 20))
	for _, want := range []string{"ALPHA", "BETA", "─", "│"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("structure marker %q must survive color stripping:\n%s", want, plain)
		}
	}
}

func TestFooterDoesNotPanicAtSmallHeights(t *testing.T) {
	// Regression test for C1: a footer-only rail must not panic at any
	// height from 1 to 8. The footer's natural height is 3 (2 body rows
	// + 1 title), but View prepends 2 intro rows (blank + rule), so the
	// footer needs 5 rows total. At heights < 5 it must be dropped.
	r := New(footerSection(2))
	for h := 1; h <= 8; h++ {
		out := r.View(Data{}, 30, h)
		lines := strings.Split(out, "\n")
		if len(lines) != h {
			t.Fatalf("height %d: got %d rows, want %d", h, len(lines), h)
		}
		// At heights where the footer fits, it should be present.
		if h >= 5 {
			if !strings.Contains(StripANSI(out), "session-row") {
				t.Errorf("height %d: footer should be present", h)
			}
		}
	}
}

func TestRailWithoutFooterUnchanged(t *testing.T) {
	r := New(mk("alpha", 0, 2), mk("beta", 1, 2))
	lines := strings.Split(r.View(Data{}, 30, 20), "\n")
	if len(lines) != 20 {
		t.Fatalf("got %d rows, want 20", len(lines))
	}
	// With no footer, the bottom is padding (just the gutter divider).
	plain := StripANSI(lines[19])
	if strings.TrimSpace(plain) != "│" {
		t.Errorf("bottom row = %q, want just the divider when no footer is present", plain)
	}
}
