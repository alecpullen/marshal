package sidepanel

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The rail's whole visual argument is that value columns share one right
// edge. Any row carrying a right column must therefore be exactly width
// cells — one cell short is what put CONTEXT's fill percentage out of line
// with the composition percentages beneath it.
func TestRailRowIsFlushToWidth(t *testing.T) {
	for _, tc := range []struct{ marker, label, right string }{
		{"", "transcript", "24k  48%"},
		{"M", "internal/app/tui/model.go", "+3 -1"},
		{"✎", "rail.go", "1 edit"},
		{"", "a", "×2"},
		{"", strings.Repeat("long", 40), "×2"},
		{"", "", "×2"},
	} {
		for _, w := range []int{20, 28, 32, 40, 60} {
			got := railRow(tc.marker, tc.label, tc.right, w)
			if n := ansi.StringWidth(got); n != w {
				t.Errorf("railRow(%q,%q,%q,%d) width = %d, want %d: %q",
					tc.marker, tc.label, tc.right, w, n, w, got)
			}
		}
	}
}

// The right column is reserved before the label is sized, so an overlong
// label is what gives way — never the value it describes.
func TestRailRowKeepsRightColumnUnderPressure(t *testing.T) {
	got := railRow("M", strings.Repeat("x", 200), "+3 -1", 24)
	if !strings.HasSuffix(got, "+3 -1") {
		t.Errorf("right column lost to a long label: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("label should have been truncated: %q", got)
	}
}

// Styling on the right column must cost zero cells, or every styled row
// drifts out of the column its plain neighbours hold.
func TestRailRowMeasuresStyledRightColumnInCells(t *testing.T) {
	plain := railRow("", "grep", "+9", 30)
	styled := railRow("", "grep", styleSuccess("+9"), 30)
	if StripANSI(styled) != plain {
		t.Errorf("styling changed the layout:\n plain  = %q\n styled = %q",
			plain, StripANSI(styled))
	}
}

// A row with no right column has no edge to align to, so it must not be
// padded — the frame supplies trailing space already.
func TestRailRowWithoutRightColumnIsNotPadded(t *testing.T) {
	if got := railRow("", "allow go test ./...", "", 40); got != " allow go test ./..." {
		t.Errorf("railRow padded a row with no right column: %q", got)
	}
}

// railBudget must predict exactly what railRow will allow, so callers can
// pre-shorten a path to the cell and never trigger a second truncation.
func TestRailBudgetMatchesWhatRailRowAllows(t *testing.T) {
	for _, marker := range []string{"", "M", "✎"} {
		for _, right := range []string{"", "×2", "24k  48%"} {
			for _, w := range []int{16, 24, 33, 50} {
				b := railBudget(marker, right, w)
				got := railRow(marker, strings.Repeat("x", b), right, w)
				if strings.Contains(got, "…") {
					t.Errorf("railBudget(%q,%q,%d)=%d was too generous: %q",
						marker, right, w, b, got)
				}
			}
		}
	}
}

func TestShortenPathCutsAtSeparators(t *testing.T) {
	const p = "internal/app/tui/sidepanel/section_context.go"
	for _, tc := range []struct {
		budget int
		want   string
	}{
		{60, p},
		{45, p},
		{33, "…/sidepanel/section_context.go"},
		// "…/sidepanel/section_context.go" is exactly 30 cells, so 29 must
		// fall through to the next separator rather than cut mid-segment.
		{30, "…/sidepanel/section_context.go"},
		{29, "…/section_context.go"},
		{20, "…/section_context.go"},
	} {
		if got := shortenPath(p, tc.budget); got != tc.want {
			t.Errorf("shortenPath(budget=%d) = %q, want %q", tc.budget, got, tc.want)
		}
	}
}

// Whatever the budget, the result must fit it — the caller has already
// reserved the remaining cells for a value column.
func TestShortenPathAlwaysFitsBudget(t *testing.T) {
	for _, p := range []string{
		"internal/app/tui/sidepanel/section_context.go",
		"a/b/c.go",
		"no-separators-at-all-in-this-very-long-file-name.go",
		"x.go",
	} {
		for b := 1; b <= 50; b++ {
			if n := ansi.StringWidth(shortenPath(p, b)); n > b {
				t.Errorf("shortenPath(%q, %d) = %d cells, over budget", p, b, n)
			}
		}
	}
}
