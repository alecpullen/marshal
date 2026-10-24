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
	shown, doneBefore, moreAfter := planWindow(items)
	if len(shown) != 4 {
		t.Fatalf("planWindow showed %d items, want 4", len(shown))
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

	_, rows := planStacked(Input{Plan: items}, 80)
	got := strings.Join(planRowTexts(rows), "\n")
	for _, want := range []string{"✓ 5 done", "todo f", "todo g", "todo h", "todo i", "+1 more"} {
		if !strings.Contains(got, want) {
			t.Errorf("stacked rows = %q, want them to contain %q", got, want)
		}
	}
	for _, notWant := range []string{"todo a", "todo b", "todo c", "todo d", "todo e", "todo j"} {
		if strings.Contains(got, notWant) {
			t.Errorf("stacked rows = %q, want them not to contain the out-of-window %q", got, notWant)
		}
	}
	// The rows are the six entries in three column-major rows; a row's target
	// is the first real item it draws.
	if len(rows) != 3 {
		t.Fatalf("stacked rows = %d, want 3", len(rows))
	}
	if want := (Target{Kind: TargetPlan, Key: "#6"}); rows[1].Target != want {
		t.Errorf("second row target = %+v, want %+v", rows[1].Target, want)
	}
}

// TestShortPlanShowsEveryItem pins that a plan no longer than the window draws
// every item and neither summary entry.
func TestShortPlanShowsEveryItem(t *testing.T) {
	items := planOf(4, 1, 2)
	shown, doneBefore, moreAfter := planWindow(items)
	if len(shown) != len(items) || doneBefore != 0 || moreAfter != 0 {
		t.Fatalf("planWindow = %d shown, %d done before, %d after; want every item and no summaries",
			len(shown), doneBefore, moreAfter)
	}

	_, rows := planStacked(Input{Plan: items}, 80)
	got := strings.Join(planRowTexts(rows), "\n")
	for _, want := range []string{"todo a", "todo b", "todo c", "todo d"} {
		if !strings.Contains(got, want) {
			t.Errorf("stacked rows = %q, want them to contain %q", got, want)
		}
	}
	for _, notWant := range []string{"done", "+", "more"} {
		if strings.Contains(got, notWant) {
			t.Errorf("stacked rows = %q, want them not to contain %q", got, notWant)
		}
	}
}

// TestFinishedPlanHasNoItemRows pins decision 8 for the stacked layout: a
// finished plan is its header alone.
func TestFinishedPlanHasNoItemRows(t *testing.T) {
	in := Input{Plan: planOf(4, 4, 0)}
	header, rows := planStacked(in, 80)
	if len(rows) != 0 {
		t.Errorf("finished plan drew %d rows, want none", len(rows))
	}
	if !strings.Contains(ansi.Strip(header), "4/4") {
		t.Errorf("header = %q, want the 4/4 count", ansi.Strip(header))
	}
	if !strings.HasSuffix(ansi.Strip(header), " done") {
		t.Errorf("header = %q, want it to end with the finished notice", ansi.Strip(header))
	}
}

// TestStackedPlanUsesTwoColumns pins the column-major fill: for six items the
// first item sits on the first row, left of the fourth.
func TestStackedPlanUsesTwoColumns(t *testing.T) {
	_, rows := planStacked(Input{Plan: planOf(6, 0, 1)}, 80)
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

// TestFoldedBorderUsesBlocksPastTwelve pins the folded border's cap: one mark
// per item up to twelve, then the blocks and no item marks.
func TestFoldedBorderUsesBlocksPastTwelve(t *testing.T) {
	long := ansi.Strip(planFolded(Input{Plan: planOf(13, 3, 4)}, 100))
	if !strings.HasPrefix(long, "╰─ plan ") {
		t.Errorf("folded border = %q, want it to start with the label", long)
	}
	if !strings.HasSuffix(long, "╯") {
		t.Errorf("folded border = %q, want it to end with the corner", long)
	}
	if !strings.Contains(long, glyph.ProgressFull) && !strings.Contains(long, glyph.ProgressEmpty) {
		t.Errorf("folded border = %q, want the plan blocks", long)
	}
	for _, notWant := range []string{glyph.OK, glyph.Ambient, glyph.Running} {
		if strings.Contains(long, notWant) {
			t.Errorf("folded border = %q, want no per-item mark %q", long, notWant)
		}
	}
	if !strings.Contains(long, "4/13") {
		t.Errorf("folded border = %q, want the active position 4/13", long)
	}
	if !strings.Contains(long, "todo d") {
		t.Errorf("folded border = %q, want the active item's text", long)
	}

	short := ansi.Strip(planFolded(Input{Plan: planOf(6, 2, 3)}, 100))
	if n := strings.Count(short, glyph.OK); n != 2 {
		t.Errorf("folded border = %q: %d done marks, want 2", short, n)
	}
	for _, want := range []string{glyph.Running, glyph.Ambient, "3/6", "todo c"} {
		if !strings.Contains(short, want) {
			t.Errorf("folded border = %q, want it to contain %q", short, want)
		}
	}
}

// TestIdleRowSaysDoneOrActive pins the one plain row a plan draws when the box
// is not there.
func TestIdleRowSaysDoneOrActive(t *testing.T) {
	done := idleRow(Input{Plan: planOf(4, 4, 0)}, 80)
	if got, want := ansi.Strip(done.Text), "✓ Plan 4/4 done"; got != want {
		t.Errorf("finished idle row = %q, want %q", got, want)
	}
	if done.Frame != FramePlain {
		t.Errorf("idle row frame = %v, want FramePlain", done.Frame)
	}

	active := idleRow(Input{Plan: planOf(4, 2, 3)}, 80)
	if got, want := ansi.Strip(active.Text), "▸ Plan 2/4 · todo c"; got != want {
		t.Errorf("active idle row = %q, want %q", got, want)
	}

	// With nothing in progress the row names the first pending item.
	pending := idleRow(Input{Plan: planOf(4, 2, 0)}, 80)
	if got, want := ansi.Strip(pending.Text), "▸ Plan 2/4 · todo c"; got != want {
		t.Errorf("pending idle row = %q, want %q", got, want)
	}
}

// TestSplitPaneWrapsALongTodo pins the split pane's one continuation line: a
// long entry wraps onto a second line indented by two cells and then
// truncates, and a pane one line tall truncates instead.
func TestSplitPaneWrapsALongTodo(t *testing.T) {
	items := []PlanItem{{Content: strings.Repeat("word ", 12), Status: "pending"}}
	lines := planSplit(Input{Plan: items}, 4)
	if len(lines) != 2 {
		t.Fatalf("planSplit returned %d lines, want the entry and its continuation", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > RightWidth {
			t.Errorf("line %d = %q is %d cells, want at most %d", i, ansi.Strip(line), w, RightWidth)
		}
	}
	first, second := ansi.Strip(lines[0]), ansi.Strip(lines[1])
	if !strings.HasPrefix(first, glyph.Ambient+" ") {
		t.Errorf("first line = %q, want the pending mark and a space", first)
	}
	if !strings.HasPrefix(second, "  ") || len(strings.TrimSpace(second)) == 0 {
		t.Errorf("continuation line = %q, want a two-cell indent and content", second)
	}

	one := planSplit(Input{Plan: items}, 1)
	if len(one) != 1 {
		t.Fatalf("planSplit in a one-line pane returned %d lines, want 1", len(one))
	}
	if !strings.Contains(ansi.Strip(one[0]), "…") {
		t.Errorf("one-line pane line = %q, want it truncated", ansi.Strip(one[0]))
	}
}
