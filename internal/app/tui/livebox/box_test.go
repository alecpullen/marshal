package livebox

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/progress"
	"marshal/internal/viewmodel"
)

// node is a live row's identity.
func node(key string) viewmodel.NodeID {
	return viewmodel.NodeID{Kind: viewmodel.KindMessage, Key: key}
}

// downloadItem is a running command with a measured total, so its card has
// every row.
func downloadItem(key, id string, started time.Time) viewmodel.WorkItem {
	total := 3.7 * gib
	return viewmodel.WorkItem{
		TaskKey: key, Kind: "command", DisplayID: id,
		Label: "Download qwen3-coder weights", State: "running",
		ProgressConfig: &progress.Config{Profile: "download", Source: "events-v1"},
		Progress: &progress.Snapshot{
			Frame: progress.Frame{Sequence: 2, PrimaryMetricID: "bytes",
				Metrics: []progress.Metric{
					{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
				},
				Detail: "shard 7 of 12"},
			ObservedAt: testNow,
		},
		StartedAt: started,
	}
}

// busyInput is the fixture the box tests start from: a two-line live part, five
// running items (the download expanded), and a four-item plan.
func busyInput() Input {
	return Input{
		Width:   100,
		MaxRows: 40,
		Now:     testNow,
		Busy:    true,
		State:   "⠋ working 4m12s",
		Hints:   []string{"Ctrl+T plan", "Ctrl+C ×2 stop"},
		Spinner: "⠋",
		Live: Live{
			Full: []string{
				"⠋ Checking the eval thresholds still make sense for a coder model.",
				"  ├ read evals/thresholds.toml",
			},
			Targets:      []viewmodel.NodeID{node("live-1"), node("live-2")},
			Short:        []string{"⠋ Checking the eval thresholds still make sense…"},
			ShortTargets: []viewmodel.NodeID{node("live-1")},
		},
		Work: []viewmodel.WorkItem{
			downloadItem("k1", "job-1", testNow.Add(-2*time.Minute)),
			{
				TaskKey: "k2", Kind: "agent", SourceID: "1", DisplayID: "#1",
				Label: "review the config change", State: "running",
				StartedAt: testNow.Add(-time.Minute),
			},
			{
				TaskKey: "k3", Kind: "command", DisplayID: "job-2",
				Label: "Harness tests", State: "running",
				OutputTail: "--- FAIL: TestX\nlast useful line\n",
				StartedAt:  testNow.Add(-30 * time.Second),
			},
			{
				TaskKey: "k4", Kind: "watch", DisplayID: "w-1",
				Label: "src/", State: "watching",
				StartedAt: testNow.Add(-20 * time.Second),
			},
			{
				TaskKey: "k5", Kind: "agent", SourceID: "2", DisplayID: "#2",
				Label: "review the diff", State: "blocked",
				Prompt:    &viewmodel.WorkPrompt{Kind: "approval"},
				StartedAt: testNow.Add(-10 * time.Second),
			},
		},
		Headlines: map[string]string{"k2": "Checking context_window…"},
		Expanded:  "k1",
		Plan:      planOf(4, 2, 3),
		Mode:      Stacked,
	}
}

// withMax returns the fixture with a row budget.
func withMax(in Input, rows int) Input {
	in.MaxRows = rows
	return in
}

// rowTexts strips a layout's rows to plain text.
func rowTexts(l Layout) []string {
	out := make([]string, 0, len(l.Rows))
	for _, r := range l.Rows {
		out = append(out, ansi.Strip(r.Text))
	}
	return out
}

// joined is a layout's plain rows as one string.
func joined(l Layout) string {
	return strings.Join(rowTexts(l), "\n")
}

// TestBoxEmptyWhenNothingLiveAndNoPlan pins the box's absence: no turn clock,
// no work, no live part and no plan means no rows at all.
func TestBoxEmptyWhenNothingLiveAndNoPlan(t *testing.T) {
	l := Plan(Input{Width: 80, MaxRows: 20, Now: testNow})
	if len(l.Rows) != 0 {
		t.Fatalf("rows = %d, want none: %q", len(l.Rows), rowTexts(l))
	}
	if got := Render(l, 80); got != "" {
		t.Errorf("Render = %q, want the empty string", got)
	}
}

// TestIdlePlanIsOnePlainRow pins the idle box: with nothing live, a plan draws
// one plain row and no outline.
func TestIdlePlanIsOnePlainRow(t *testing.T) {
	in := Input{Width: 80, MaxRows: 20, Now: testNow, Plan: planOf(4, 2, 3)}
	l := Plan(in)
	if len(l.Rows) != 1 {
		t.Fatalf("rows = %d, want 1: %q", len(l.Rows), rowTexts(l))
	}
	row := l.Rows[0]
	if row.Frame != FramePlain {
		t.Errorf("frame = %v, want FramePlain", row.Frame)
	}
	if got, want := ansi.Strip(row.Text), "▸ Plan 2/4 · todo c"; got != want {
		t.Errorf("idle row = %q, want %q", got, want)
	}
	if out := Render(l, 80); out != row.Text {
		t.Errorf("Render = %q, want the plain row alone", out)
	}
}

// TestLivePartDrawsTheBoxWithoutTheTurnClock pins that the live part keeps the
// box on screen after the turn clock stops, with no state in the border.
func TestLivePartDrawsTheBoxWithoutTheTurnClock(t *testing.T) {
	in := Input{
		Width: 80, MaxRows: 10, Now: testNow,
		Live: Live{
			Full:    []string{"⠋ Checking the eval thresholds…"},
			Targets: []viewmodel.NodeID{node("live-1")},
		},
		Mode: Stacked,
	}
	l := Plan(in)
	if len(l.Rows) < 3 {
		t.Fatalf("rows = %d, want a bordered box: %q", len(l.Rows), rowTexts(l))
	}
	texts := rowTexts(l)
	if !strings.HasPrefix(texts[0], "╭──") {
		t.Errorf("top border = %q, want it to start ╭─ with no state gap", texts[0])
	}
	if got := texts[len(texts)-1]; got != "╰"+strings.Repeat("─", 78)+"╯" {
		t.Errorf("bottom border = %q, want the plain outline", got)
	}
	if !strings.Contains(strings.Join(texts, "\n"), "Checking the eval thresholds") {
		t.Errorf("box = %q, want the live line", texts)
	}
	if strings.Contains(texts[0], "working") {
		t.Errorf("top border = %q, want no state while the turn clock is off", texts[0])
	}
	// The live row is a target, so the TUI can click it.
	if got, want := l.Rows[1].Target, (Target{Kind: TargetLive, Node: node("live-1")}); got != want {
		t.Errorf("live target = %+v, want %+v", got, want)
	}
}

// TestShedOrder walks the row budget down from the full height and pins the
// shape at every shed step.
func TestShedOrder(t *testing.T) {
	base := busyInput()
	full := Plan(withMax(base, 40))
	if full.Shed != 0 {
		t.Fatalf("full box shed %d steps, want none", full.Shed)
	}
	fullHeight := len(full.Rows)
	if fullHeight != 17 {
		t.Fatalf("full height = %d, want 17 (top, 2 live, header, card+4 rows, plan header+2, bottom)", fullHeight)
	}

	byShed := make(map[int]Layout, shedSteps+1)
	for m := 1; m <= fullHeight; m++ {
		l := Plan(withMax(base, m))
		if len(l.Rows) > m {
			t.Errorf("MaxRows %d: %d rows, want at most %d", m, len(l.Rows), m)
		}
		if _, ok := byShed[l.Shed]; !ok {
			byShed[l.Shed] = l
		}
	}
	for step := 0; step <= shedSteps; step++ {
		if _, ok := byShed[step]; !ok {
			t.Fatalf("no budget produced shed step %d", step)
		}
	}

	if got := byShed[0].Mode; got != Stacked {
		t.Errorf("unshed mode = %q, want stacked", got)
	}
	if got := joined(byShed[0]); !strings.Contains(got, "├─ plan") {
		t.Errorf("unshed box = %q, want the stacked plan section", got)
	}

	// Step 1: the plan folds into the bottom border and any split ends.
	if got := byShed[1].Mode; got != Folded {
		t.Errorf("shed 1 mode = %q, want folded", got)
	}
	if texts := rowTexts(byShed[1]); strings.Contains(strings.Join(texts, "\n"), "├─ plan") {
		t.Errorf("shed 1 box = %q, want no plan section", texts)
	}
	if got := rowTexts(byShed[1])[len(byShed[1].Rows)-1]; !strings.HasPrefix(got, "╰─ plan ") {
		t.Errorf("shed 1 bottom = %q, want the folded plan", got)
	}

	// Step 2: the live part drops to Short.
	short := joined(byShed[2])
	if !strings.Contains(short, "Checking the eval thresholds still make sense…") {
		t.Errorf("shed 2 box = %q, want the live part's short lines", short)
	}
	if strings.Contains(short, "├ read evals/thresholds.toml") {
		t.Errorf("shed 2 box = %q, want the live part's second line gone", short)
	}

	// Step 3: running rows cap at three, with the more row.
	capped := joined(byShed[3])
	if !strings.Contains(capped, "+2 more · /work") {
		t.Errorf("shed 3 box = %q, want the more row", capped)
	}
	if n := countWorkRows(byShed[3]); n != 3 {
		t.Errorf("shed 3 work items = %d, want 3", n)
	}

	// Step 4: the expanded card collapses.
	collapsed := joined(byShed[4])
	if strings.Contains(collapsed, "↵ collapse") {
		t.Errorf("shed 4 box = %q, want the card collapsed", collapsed)
	}
	if n := countWorkRows(byShed[4]); n != 3 {
		t.Errorf("shed 4 work items = %d, want 3 one-row items", n)
	}

	// Steps 5 and 6: the live part goes, then running rows cap at one.
	if got := joined(byShed[5]); strings.Contains(got, "Checking the eval") {
		t.Errorf("shed 5 box = %q, want the live part dropped", got)
	}
	if n := countWorkRows(byShed[6]); n != 1 {
		t.Errorf("shed 6 work items = %d, want 1", n)
	}
	if got := joined(byShed[6]); !strings.Contains(got, "+4 more · /work") {
		t.Errorf("shed 6 box = %q, want the more row", got)
	}

	// Step 7: one plain summary row.
	if len(byShed[7].Rows) != 1 {
		t.Fatalf("shed 7 rows = %d, want 1", len(byShed[7].Rows))
	}
	if got := byShed[7].Rows[0].Frame; got != FramePlain {
		t.Errorf("shed 7 frame = %v, want FramePlain", got)
	}
	compact := ansi.Strip(byShed[7].Rows[0].Text)
	for _, want := range []string{"working", "⧉2", "┆2", "○1", "plan 2/4"} {
		if !strings.Contains(compact, want) {
			t.Errorf("compact row = %q, want it to contain %q", compact, want)
		}
	}
}

// countWorkRows counts the rows drawn per work item.
func countWorkRows(l Layout) int {
	keys := make(map[string]bool)
	for _, r := range l.Rows {
		if r.Target.Kind == TargetWork {
			keys[r.Target.Key] = true
		}
	}
	return len(keys)
}

// TestBoxNeverExceedsMaxRows is the budget invariant over every small budget.
func TestBoxNeverExceedsMaxRows(t *testing.T) {
	base := busyInput()
	for _, mode := range []Mode{Stacked, Split, Folded} {
		for m := 1; m <= 30; m++ {
			in := withMax(base, m)
			in.Mode = mode
			l := Plan(in)
			if len(l.Rows) > m {
				t.Fatalf("%s MaxRows %d: %d rows, want at most %d", mode, m, len(l.Rows), m)
			}
			if l.Shed > shedSteps {
				t.Fatalf("%s MaxRows %d: shed %d steps, want at most %d", mode, m, l.Shed, shedSteps)
			}
		}
	}
}

// TestSplitSkippedBelowNinety pins that the split layout only exists from 90
// columns.
func TestSplitSkippedBelowNinety(t *testing.T) {
	in := busyInput()
	in.Width = 89
	in.Mode = Split
	l := Plan(in)
	if got := l.Mode; got != Stacked {
		t.Errorf("mode at 89 columns = %q, want stacked", got)
	}
	texts := joined(l)
	for _, notWant := range []string{"┬", "┴"} {
		if strings.Contains(texts, notWant) {
			t.Errorf("box at 89 columns = %q, want no split glyph %q", texts, notWant)
		}
	}

	in.Width = 90
	if got := Plan(in).Mode; got != Split {
		t.Errorf("mode at 90 columns = %q, want split", got)
	}
}

// TestOneExpandedCard pins that exactly one item opens and the rest stay one
// row each.
func TestOneExpandedCard(t *testing.T) {
	in := busyInput()
	in.Expanded = "k2"
	l := Plan(in)
	rows := make(map[string]int)
	for _, r := range l.Rows {
		if r.Target.Kind == TargetWork {
			rows[r.Target.Key]++
		}
	}
	if rows["k2"] < 2 {
		t.Errorf("expanded item drew %d rows, want its card", rows["k2"])
	}
	for _, key := range []string{"k1", "k3", "k4", "k5"} {
		if rows[key] != 1 {
			t.Errorf("item %q drew %d rows, want 1", key, rows[key])
		}
	}
}

// TestTargetsMatchRows pins the row targets and the browse stops: every work
// row names an item drawn, every running item's first row is targeted, the
// stops follow row order, and the cursor marks its row.
func TestTargetsMatchRows(t *testing.T) {
	in := busyInput()
	in.Expanded = ""
	in.Work = in.Work[:3]
	in.Cursor = Target{Kind: TargetWork, Key: "k2"}
	l := Plan(in)

	// The running order is k1 (2m), k2 (1m), k3 (30s).
	drawn := make(map[string]bool)
	var firstRow []string
	lastKey := ""
	for _, r := range l.Rows {
		if r.Target.Kind != TargetWork {
			continue
		}
		drawn[r.Target.Key] = true
		if r.Target.Key != lastKey {
			firstRow = append(firstRow, r.Target.Key)
		}
		lastKey = r.Target.Key
		if r.Cursor != (r.Target.Key == "k2") {
			t.Errorf("row %q cursor = %v", r.Target.Key, r.Cursor)
		}
	}
	for _, it := range in.Work {
		if !drawn[it.TaskKey] {
			t.Errorf("item %q has no targeted row", it.TaskKey)
		}
	}
	if got, want := strings.Join(firstRow, ","), "k1,k2,k3"; got != want {
		t.Errorf("first rows = %q, want %q", got, want)
	}

	// Stops: the running items in row order, then one per plan item drawn, in
	// the order the rows draw them — the left entry of a two-column row first,
	// then its right entry.
	want := []Target{
		{Kind: TargetWork, Key: "k1"},
		{Kind: TargetWork, Key: "k2"},
		{Kind: TargetWork, Key: "k3"},
		{Kind: TargetPlan, Key: "#1"},
		{Kind: TargetPlan, Key: "#3"},
		{Kind: TargetPlan, Key: "#2"},
		{Kind: TargetPlan, Key: "#4"},
	}
	if len(l.Stops) != len(want) {
		t.Fatalf("stops = %+v, want %+v", l.Stops, want)
	}
	for i := range want {
		if l.Stops[i] != want[i] {
			t.Errorf("stop %d = %+v, want %+v", i, l.Stops[i], want[i])
		}
	}
}

// TestEveryRowIsExactlyTheWidth pins the geometry: in every mode, every rendered
// line is exactly the frame's width.
func TestEveryRowIsExactlyTheWidth(t *testing.T) {
	base := busyInput()
	for _, width := range []int{80, 89, 96, 120} {
		for _, mode := range []Mode{Stacked, Split, Folded} {
			if mode == Split && width < SplitMinWidth {
				continue
			}
			in := withMax(base, 40)
			in.Width = width
			in.Mode = mode
			out := Render(Plan(in), width)
			for i, line := range strings.Split(out, "\n") {
				if got := ansi.StringWidth(line); got != width {
					t.Errorf("%s %d cols: line %d is %d cells, want %d: %q",
						mode, width, i, got, width, ansi.Strip(line))
				}
			}
		}
	}
}

// TestHintsDropBeforeTheState pins the top border's priority: hints drop from
// the end first, and only then is the state cut.
func TestHintsDropBeforeTheState(t *testing.T) {
	base := Input{
		Width: 40, MaxRows: 40, Now: testNow, Busy: true,
		State:   "⠋ working 4m12s",
		Hints:   []string{"Ctrl+T plan", "Ctrl+C ×2 stop"},
		Spinner: "⠋",
		Live:    Live{Full: []string{"⠋ checking"}, Targets: []viewmodel.NodeID{node("l")}},
		Mode:    Stacked,
	}

	// Both hints fit at 80 columns.
	in := withMax(base, 40)
	in.Width = 80
	wide := rowTexts(Plan(in))[0]
	if !strings.Contains(wide, "Ctrl+T plan") || !strings.Contains(wide, "Ctrl+C ×2 stop") {
		t.Errorf("80-col top border = %q, want both hints", wide)
	}

	// At 40 columns the last hint goes; the state stays whole.
	in.Width = 40
	top := rowTexts(Plan(in))[0]
	if !strings.Contains(top, "⠋ working 4m12s") {
		t.Errorf("40-col top border = %q, want the state intact", top)
	}
	if strings.Contains(top, "Ctrl+C ×2 stop") {
		t.Errorf("40-col top border = %q, want the last hint dropped", top)
	}
	if !strings.Contains(top, "Ctrl+T plan") {
		t.Errorf("40-col top border = %q, want the first hint kept", top)
	}
	if w := ansi.StringWidth(top); w != 40 {
		t.Errorf("40-col top border is %d cells, want 40", w)
	}

	// At 30 columns no hint fits.
	in.Width = 30
	top = rowTexts(Plan(in))[0]
	if strings.Contains(top, "Ctrl") {
		t.Errorf("30-col top border = %q, want every hint dropped", top)
	}
	if !strings.Contains(top, "⠋ working 4m12s") {
		t.Errorf("30-col top border = %q, want the state intact", top)
	}

	// At 15 columns the state itself is cut.
	in.Width = 15
	top = rowTexts(Plan(in))[0]
	if strings.Contains(top, "4m12s") {
		t.Errorf("15-col top border = %q, want the state truncated", top)
	}
	if !strings.Contains(top, "…") {
		t.Errorf("15-col top border = %q, want the truncation mark", top)
	}
	if w := ansi.StringWidth(top); w != 15 {
		t.Errorf("15-col top border is %d cells, want 15: %q", w, top)
	}
}

// TestPlanBudgetBelowOneStillSheds pins the MaxRows contract: a budget below
// one sheds the box down to the single plain row instead of growing without
// limit.
func TestPlanBudgetBelowOneStillSheds(t *testing.T) {
	in := busyInput()
	in.MaxRows = 0
	l := Plan(in)
	if len(l.Rows) != 1 {
		t.Fatalf("MaxRows 0 drew %d rows, want 1", len(l.Rows))
	}
	if l.Rows[0].Frame != FramePlain {
		t.Errorf("MaxRows 0 row frame = %v, want the plain summary row", l.Rows[0].Frame)
	}
}

// TestRenderNarrowerThanTheOutlineStaysConsistent pins that a request narrower
// than the outline is still drawn at a consistent width, so every line closes.
func TestRenderNarrowerThanTheOutlineStaysConsistent(t *testing.T) {
	for _, width := range []int{1, 2, 4, 5} {
		in := busyInput()
		in.Width = width
		out := Render(Plan(in), width)
		want := max(width, 5)
		for i, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(line); got != want {
				t.Errorf("%d-col request: line %d is %d cells, want %d: %q",
					width, i, got, want, ansi.Strip(line))
			}
		}
	}
}

// TestCursorRowPaintsTheSelectionBand pins that the browse cursor's row is
// band-painted rather than merely flagged: the text is unchanged and the
// escapes are not.
func TestCursorRowPaintsTheSelectionBand(t *testing.T) {
	base := busyInput()
	l := Plan(base)
	cursored := Render(l, base.Width)

	base.Cursor = Target{Kind: TargetWork, Key: "k1"}
	withCursor := Plan(base)
	if !hasCursorRow(withCursor) {
		t.Fatal("the cursor target matched no row")
	}
	painted := Render(withCursor, base.Width)

	if ansi.Strip(painted) != ansi.Strip(cursored) {
		t.Fatal("the cursor changed the box's text; it must only paint it")
	}
	if painted == cursored {
		t.Fatal("the cursor row was not painted")
	}
}

// hasCursorRow reports whether any row is marked as the cursor's.
func hasCursorRow(l Layout) bool {
	for _, r := range l.Rows {
		if r.Cursor {
			return true
		}
	}
	return false
}
