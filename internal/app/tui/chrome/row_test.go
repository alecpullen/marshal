package chrome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// widest returns the width of the widest line in a rendered row.
func widest(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if n := ansi.StringWidth(line); n > w {
			w = n
		}
	}
	return w
}

// TestRowNeverExceedsInner pins the hard invariant: a row is a layout, not a
// hope. Overflow here corrupts every panel that composes rows into a frame.
func TestRowNeverExceedsInner(t *testing.T) {
	for _, inner := range []int{10, 20, 30, 44, 60, 80, 120} {
		got := Row("▸ ", "anthropic/claude-opus-5-20260115-preview",
			"200k ctx · $15/$75", "●now", inner)
		if w := widest(got); w > inner {
			t.Errorf("inner=%d: widest line is %d cells:\n%q", inner, w, got)
		}
	}
}

// TestRowKeepsBadgeWhenTight pins C12: the badge is a state marker, not
// metadata. Losing it changes what the user believes about the system, so it
// outranks the detail column and the full label.
func TestRowKeepsBadgeWhenTight(t *testing.T) {
	got := Row("▸ ", "anthropic/claude-opus-5-20260115-preview",
		"200k ctx · $15/$75", "●now", 44)
	if !strings.Contains(got, "●now") {
		t.Errorf("badge dropped at inner=44:\n%q", got)
	}
}

// TestRowDropsDetailWholeNotStubbed pins C12: a fragment like "128k ct…" is
// strictly worse than nothing — it costs columns and conveys nothing.
func TestRowDropsDetailWholeNotStubbed(t *testing.T) {
	got := Row("  ", "openai/gpt-5-turbo-2026-04-01", "128k ctx · $10/$30", "", 40)
	if strings.Contains(got, "128k ct…") {
		t.Errorf("detail was stubbed rather than dropped:\n%q", got)
	}
	if strings.Contains(got, "128k ctx · $10/$30") {
		return // it fit whole, which is also correct
	}
	if strings.Contains(got, "128k") {
		t.Errorf("detail appears partially:\n%q", got)
	}
}

// TestRowStacksWhenLabelWouldStarve pins C14: below the stack threshold a
// single line cannot carry a meaningful label and its metadata, and
// truncating the label loses more than the extra line costs.
func TestRowStacksWhenLabelWouldStarve(t *testing.T) {
	got := Row("▸ ", "anthropic/claude-opus-5-20260115-preview",
		"200k ctx · $15/$75", "●now", 28)
	if !strings.Contains(got, "\n") {
		t.Errorf("expected a stacked row at inner=28, got one line:\n%q", got)
	}
	if !strings.Contains(got, "200k ctx") {
		t.Errorf("stacked row dropped the detail it exists to preserve:\n%q", got)
	}
	if w := widest(got); w > 28 {
		t.Errorf("stacked row is %d cells wide, want <= 28:\n%q", w, got)
	}
}

// TestRowRightAlignsWhenRoomy pins the wide case: the right column hugs the
// right edge so a column of rows scans as a column.
func TestRowRightAlignsWhenRoomy(t *testing.T) {
	got := Row("  ", "short", "detail", "", 40)
	if ansi.StringWidth(got) != 40 {
		t.Errorf("want the row padded to exactly 40 cells, got %d:\n%q",
			ansi.StringWidth(got), got)
	}
	if !strings.HasSuffix(got, "detail") {
		t.Errorf("right column is not flush right:\n%q", got)
	}
}

// TestRowWithoutRightColumnUsesFullWidth pins that a label-only row is not
// penalised by reservation logic it does not need.
func TestRowWithoutRightColumnUsesFullWidth(t *testing.T) {
	label := strings.Repeat("x", 100)
	got := Row("  ", label, "", "", 40)
	if strings.Contains(got, "\n") {
		t.Errorf("label-only row must not stack:\n%q", got)
	}
	if w := ansi.StringWidth(got); w > 40 {
		t.Errorf("label-only row is %d cells, want <= 40", w)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("an over-long label must be truncated with an ellipsis:\n%q", got)
	}
}
