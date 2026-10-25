package livebox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// planOf builds a plan of n items labelled "todo a", "todo b", …: the first
// done items are completed, the item at active (1-based; 0 for none) is in
// progress, and the rest are pending.
func planOf(n, done, active int) []PlanItem {
	items := make([]PlanItem, 0, n)
	for i := 0; i < n; i++ {
		status := "pending"
		switch {
		case i < done:
			status = "completed"
		case i == active-1:
			status = "in_progress"
		}
		items = append(items, PlanItem{Content: fmt.Sprintf("todo %c", 'a'+i), Status: status})
	}
	return items
}

// planRowTexts strips every row to its plain text.
func planRowTexts(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, ansi.Strip(r.Text))
	}
	return out
}

// TestPlanWindowAroundTheActiveItem pins the long list's window: it starts at
// the in-progress item, shows four of them, and summarises both ends.
func TestPlanWindowAroundTheActiveItem(t *testing.T) {
	items := planOf(10, 5, 6)
	shown, _, doneBefore, moreAfter := planWindowAt(items)
	if len(shown) != 4 {
		t.Fatalf("planWindowAt showed %d items, want 4", len(shown))
	}
	if got := shown[0].Content; got != "todo f" {
		t.Errorf("window starts at %q, want the in-progress item %q", got, "todo f")
	}
	if got := shown[3].Content; got != "todo i" {
		t.Errorf("window ends at %q, want %q", got, "todo i")
	}
	if doneBefore != 5 {
		t.Errorf("doneBefore = %d, want 5", doneBefore)
	}
	if moreAfter != 1 {
		t.Errorf("moreAfter = %d, want 1", moreAfter)
	}

	rows := planRows(Input{Plan: items}, 80)
	got := strings.Join(planRowTexts(rows), "\n")
	for _, want := range []string{"✓ 5 done", "todo f", "todo g", "todo h", "todo i", "+1 more"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan rows = %q, want them to contain %q", got, want)
		}
	}
	for _, notWant := range []string{"todo a", "todo b", "todo c", "todo d", "todo e", "todo j"} {
		if strings.Contains(got, notWant) {
			t.Errorf("plan rows = %q, want them not to contain the out-of-window %q", got, notWant)
		}
	}
	// The rows are the six entries in three column-major rows; a row's target
	// is the first real item it draws.
	if len(rows) != 3 {
		t.Fatalf("plan rows = %d, want 3", len(rows))
	}
	if want := (Target{Kind: TargetPlan, Key: "#6"}); rows[1].Target != want {
		t.Errorf("second row target = %+v, want %+v", rows[1].Target, want)
	}
}

// TestShortPlanShowsEveryItem pins that a plan no longer than the window draws
// every item and neither summary entry.
func TestShortPlanShowsEveryItem(t *testing.T) {
	items := planOf(4, 1, 2)
	shown, _, doneBefore, moreAfter := planWindowAt(items)
	if len(shown) != len(items) || doneBefore != 0 || moreAfter != 0 {
		t.Fatalf("planWindowAt = %d shown, %d done before, %d after; want every item and no summaries",
			len(shown), doneBefore, moreAfter)
	}

	rows := planRows(Input{Plan: items}, 80)
	got := strings.Join(planRowTexts(rows), "\n")
	for _, want := range []string{"todo a", "todo b", "todo c", "todo d"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan rows = %q, want them to contain %q", got, want)
		}
	}
	for _, notWant := range []string{"done", "+", "more"} {
		if strings.Contains(got, notWant) {
			t.Errorf("plan rows = %q, want them not to contain %q", got, notWant)
		}
	}
}

// TestFinishedPlanIsOneRow pins that a finished plan is its one finished row,
// and that Plan draws only that row for it.
func TestFinishedPlanIsOneRow(t *testing.T) {
	in := Input{Width: 80, MaxRows: 20, Now: testNow, Plan: planOf(4, 4, 0)}
	row := finishedRow(in, 80)
	if got, want := ansi.Strip(row.Text), " ✓ Plan 4/4 done"; got != want {
		t.Errorf("finished row = %q, want %q", got, want)
	}
	if row.Target.Kind != TargetNone {
		t.Errorf("finished row target = %+v, want none", row.Target)
	}

	l := Plan(in)
	if len(l.Rows) != 1 {
		t.Fatalf("finished plan drew %d rows, want 1: %q", len(l.Rows), rowTexts(l))
	}
	if got := ansi.Strip(l.Rows[0].Text); !strings.Contains(got, "Plan 4/4 done") {
		t.Errorf("finished plan row = %q, want the finished notice", got)
	}
}

// TestStackedPlanUsesTwoColumns pins the column-major fill: for six items the
// first item sits on the first row, left of the fourth.
func TestStackedPlanUsesTwoColumns(t *testing.T) {
	rows := planRows(Input{Plan: planOf(6, 0, 1)}, 80)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (two columns of three)", len(rows))
	}
	for i, want := range [][2]string{
		{"todo a", "todo d"},
		{"todo b", "todo e"},
		{"todo c", "todo f"},
	} {
		got := ansi.Strip(rows[i].Text)
		left, right := strings.Index(got, want[0]), strings.Index(got, want[1])
		if left < 0 || right < 0 {
			t.Errorf("row %d = %q, want both %q and %q", i, got, want[0], want[1])
			continue
		}
		if left > right {
			t.Errorf("row %d = %q, want %q left of %q", i, got, want[0], want[1])
		}
	}
}

// TestPlanLineUsesBlocksPastTwelve pins the one-line form's cap: one mark per
// item up to twelve, then the blocks and no item marks.
func TestPlanLineUsesBlocksPastTwelve(t *testing.T) {
	long := ansi.Strip(planLine(Input{Plan: planOf(13, 3, 4)}, 100).Text)
	if !strings.Contains(long, glyph.ProgressFull) && !strings.Contains(long, glyph.ProgressEmpty) {
		t.Errorf("plan line = %q, want the plan blocks", long)
	}
	for _, notWant := range []string{glyph.OK, glyph.Ambient, glyph.Running} {
		if strings.Contains(long, notWant) {
			t.Errorf("plan line = %q, want no per-item mark %q", long, notWant)
		}
	}
	if !strings.Contains(long, "4/13") {
		t.Errorf("plan line = %q, want the active position 4/13", long)
	}
	if !strings.Contains(long, "todo d") {
		t.Errorf("plan line = %q, want the active item's text", long)
	}
	if !strings.HasPrefix(long, " ") {
		t.Errorf("plan line = %q, want it indented one cell", long)
	}

	short := ansi.Strip(planLine(Input{Plan: planOf(6, 2, 3)}, 100).Text)
	if n := strings.Count(short, glyph.OK); n != 2 {
		t.Errorf("plan line = %q: %d done marks, want 2", short, n)
	}
	for _, want := range []string{glyph.Running, glyph.Ambient, "3/6", "todo c"} {
		if !strings.Contains(short, want) {
			t.Errorf("plan line = %q, want it to contain %q", short, want)
		}
	}
}

// TestIdleRowSaysDoneOrActive pins the finished row a plan draws once every
// item is completed.
func TestIdleRowSaysDoneOrActive(t *testing.T) {
	done := finishedRow(Input{Plan: planOf(4, 4, 0)}, 80)
	if got, want := ansi.Strip(done.Text), " ✓ Plan 4/4 done"; got != want {
		t.Errorf("finished row = %q, want %q", got, want)
	}
	if done.Target.Kind != TargetNone {
		t.Errorf("finished row target = %+v, want none", done.Target)
	}
}

// TestStalePlanAddsAnEntry pins decision 8: a stale plan gains a final
// "stale <age>" entry in the warning colour, with no target.
func TestStalePlanAddsAnEntry(t *testing.T) {
	in := Input{Plan: planOf(4, 2, 3), Stale: 4 * 60 * 1e9}
	entries := planEntries(in)
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	last := entries[len(entries)-1]
	if got, want := last.text, "stale 4m 0s"; got != want {
		t.Errorf("stale entry = %q, want %q", got, want)
	}
	if !last.warning {
		t.Error("the stale entry must be painted in the warning colour")
	}
	if last.target.Kind != TargetNone {
		t.Errorf("stale entry target = %+v, want none", last.target)
	}

	// The one-line form carries it too.
	line := ansi.Strip(planLine(in, 100).Text)
	if !strings.Contains(line, "stale 4m 0s") {
		t.Errorf("plan line = %q, want the stale notice", line)
	}

	// A fresh plan has no stale entry.
	fresh := planEntries(Input{Plan: planOf(4, 2, 3)})
	if got := fresh[len(fresh)-1].text; strings.HasPrefix(got, "stale") {
		t.Errorf("fresh plan's last entry = %q, want no stale notice", got)
	}
}

// TestStackedRowsKeepBothColumnTargets pins that a row drawing two plan items
// keeps both of their targets: the left one in Target, the right one in Also at
// the cell it starts at, and both as browse stops in row order.
func TestStackedRowsKeepBothColumnTargets(t *testing.T) {
	items := planOf(10, 5, 6)
	rows := planRows(Input{Plan: items}, 80)
	if len(rows) < 2 {
		t.Fatalf("plan rows = %d, want at least 2", len(rows))
	}
	// Row 1 draws "todo f" (6th) on the left and "todo i" (9th) on the right.
	if want := (Target{Kind: TargetPlan, Key: "#6"}); rows[1].Target != want {
		t.Fatalf("row 1 target = %+v, want %+v", rows[1].Target, want)
	}
	if len(rows[1].Also) != 1 {
		t.Fatalf("row 1 also = %+v, want the right column's target", rows[1].Also)
	}
	if want := (Target{Kind: TargetPlan, Key: "#9"}); rows[1].Also[0].Target != want {
		t.Errorf("row 1 right target = %+v, want %+v", rows[1].Also[0].Target, want)
	}
	if rows[1].Also[0].Col <= 0 {
		t.Errorf("row 1 right entry starts at cell %d, want a positive offset", rows[1].Also[0].Col)
	}

	in := Input{Width: 96, MaxRows: 40, Now: testNow, Plan: items}
	stops := map[string]bool{}
	for _, s := range Plan(in).Stops {
		if s.Kind == TargetPlan {
			stops[s.Key] = true
		}
	}
	for _, key := range []string{"#6", "#9"} {
		if !stops[key] {
			t.Errorf("stops = %v, want the drawn plan item %q", stops, key)
		}
	}
}

// TestPlanLineFitsNarrowWidths pins that the one-line form is never wider than
// the width it is asked for.
func TestPlanLineFitsNarrowWidths(t *testing.T) {
	for width := 10; width <= 40; width++ {
		for _, items := range [][]PlanItem{planOf(4, 1, 2), planOf(12, 3, 5)} {
			line := planLine(Input{Plan: items, Now: testNow, Spinner: "⠋"}, width)
			if got := ansi.StringWidth(line.Text); got > width {
				t.Errorf("%d-col plan line with %d items is %d cells, want at most %d: %q",
					width, len(items), got, width, ansi.Strip(line.Text))
			}
		}
	}
}
