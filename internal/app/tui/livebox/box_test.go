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

// busyInput is the fixture the tray tests start from: one held row, five
// running items (the download expanded), and a four-item plan.
func busyInput() Input {
	return Input{
		Width:   100,
		MaxRows: 40,
		Now:     testNow,
		Spinner: "⠋",
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
		Held: []HeldItem{{
			Item: viewmodel.WorkItem{
				TaskKey: "k6", Kind: "command", DisplayID: "job-0",
				Label: "go build ./...", State: "completed",
				StartedAt: testNow.Add(-3 * time.Minute),
			},
			Outcome:  "done",
			Summary:  "ok",
			Duration: 12 * time.Second,
		}},
		Headlines: map[string]string{"k2": "Checking context_window…"},
		Expanded:  "k1",
		Plan:      planOf(4, 2, 3),
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

// TestTrayEmptyWithNothingToPin pins the tray's absence: no run, no work, no
// held rows and no plan means no rows at all.
func TestTrayEmptyWithNothingToPin(t *testing.T) {
	l := Plan(Input{Width: 80, MaxRows: 20, Now: testNow})
	if len(l.Rows) != 0 {
		t.Fatalf("rows = %d, want none: %q", len(l.Rows), rowTexts(l))
	}
	if got := Render(l, 80); got != "" {
		t.Errorf("Render = %q, want the empty string", got)
	}
}

// TestIdlePlanDrawsItsRows pins the idle tray: with no work, a four-item plan
// is two plan rows, each indented one cell.
func TestIdlePlanDrawsItsRows(t *testing.T) {
	in := Input{Width: 80, MaxRows: 20, Now: testNow, Plan: planOf(4, 2, 3)}
	l := Plan(in)
	if len(l.Rows) != 2 {
		t.Fatalf("rows = %d, want 2: %q", len(l.Rows), rowTexts(l))
	}
	for i, r := range l.Rows {
		if !strings.HasPrefix(ansi.Strip(r.Text), " ") {
			t.Errorf("plan row %d = %q, want it indented one cell", i, ansi.Strip(r.Text))
		}
	}
	out := ansi.Strip(Render(l, 80))
	for _, want := range []string{"todo a", "todo b"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render = %q, want it to contain %q", out, want)
		}
	}
	if n := len(strings.Split(out, "\n")); n != 2 {
		t.Errorf("Render = %d lines, want the plan's two rows", n)
	}
}

// TestShedOrder walks the row budget down from the full height and pins the
// shape at every shed step.
func TestShedOrder(t *testing.T) {
	base := busyInput()
	full := Plan(withMax(base, 40))
	if full.Shed != 0 {
		t.Fatalf("full tray shed %d steps, want none", full.Shed)
	}
	fullHeight := len(full.Rows)
	if fullHeight != 12 {
		t.Fatalf("full height = %d, want 12 (held, card+4 running, 2 plan rows)", fullHeight)
	}

	byShed := make([]Layout, shedSteps+1)
	byShed[0] = full
	// Each step is applied cumulatively, exactly as the shed loop does. A
	// budget cannot reach every step — folding one held row into one row does
	// not change the height — so the shapes are built from the state directly.
	st := buildState{}
	for step := 1; step <= shedSteps; step++ {
		applyShed(&st, step)
		byShed[step] = build(base, st)
	}
	for step := 0; step <= shedSteps; step++ {
		if len(byShed[step].Rows) == 0 {
			t.Fatalf("shed step %d drew no rows", step)
		}
	}

	// Unshed: the held row comes first, in start order.
	unshed := rowTexts(byShed[0])
	if !strings.Contains(unshed[0], "job-0") {
		t.Errorf("unshed row 0 = %q, want the held row first", unshed[0])
	}

	// Step 1: the plan becomes its one-line form.
	if got := joined(byShed[1]); !strings.Contains(got, "3/4") || !strings.Contains(got, "todo c") {
		t.Errorf("shed 1 tray = %q, want the one-line plan", got)
	}
	if n := len(byShed[1].Rows); n != 11 {
		t.Errorf("shed 1 rows = %d, want 11", n)
	}

	// Step 2: the held rows fold into one row.
	if got := joined(byShed[2]); !strings.Contains(got, "✓ 1 finished") {
		t.Errorf("shed 2 tray = %q, want the folded held row", got)
	}

	// Step 3: running rows cap at three, blocked first, with the more row.
	capped := joined(byShed[3])
	if !strings.Contains(capped, "+2 more · /work") {
		t.Errorf("shed 3 tray = %q, want the more row", capped)
	}
	if n := countWorkRows(byShed[3]); n != 3 {
		t.Errorf("shed 3 work items = %d, want 3", n)
	}
	if !strings.Contains(capped, "#2") {
		t.Errorf("shed 3 tray = %q, want the blocked k5 kept", capped)
	}

	// Step 4: the expanded card collapses.
	if got := joined(byShed[4]); strings.Contains(got, "↵ collapse") {
		t.Errorf("shed 4 tray = %q, want the card collapsed", got)
	}

	// Step 5: running rows cap at one, the blocked one.
	if n := countWorkRows(byShed[5]); n != 1 {
		t.Errorf("shed 5 work items = %d, want 1", n)
	}
	if got := joined(byShed[5]); !strings.Contains(got, "+4 more · /work") {
		t.Errorf("shed 5 tray = %q, want the more row", got)
	}

	// Step 6: one compact row.
	if len(byShed[6].Rows) != 1 {
		t.Fatalf("shed 6 rows = %d, want 1", len(byShed[6].Rows))
	}
	if got, want := ansi.Strip(byShed[6].Rows[0].Text), "⧉2 ┆2 ○1 plan 2/4"; got != want {
		t.Errorf("compact row = %q, want %q", got, want)
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
	for m := 1; m <= 30; m++ {
		l := Plan(withMax(base, m))
		if len(l.Rows) > m {
			t.Fatalf("MaxRows %d: %d rows, want at most %d", m, len(l.Rows), m)
		}
		if l.Shed > shedSteps {
			t.Fatalf("MaxRows %d: shed %d steps, want at most %d", m, l.Shed, shedSteps)
		}
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
	for _, key := range []string{"k1", "k3", "k4", "k5", "k6"} {
		if rows[key] != 1 {
			t.Errorf("item %q drew %d rows, want 1", key, rows[key])
		}
	}
}

// TestTargetsMatchRows pins the row targets and the browse stops: every work
// row names an item drawn, the stops follow row order, and the cursor marks
// its row.
func TestTargetsMatchRows(t *testing.T) {
	in := busyInput()
	in.Expanded = ""
	in.Work = in.Work[:3]
	in.Cursor = Target{Kind: TargetWork, Key: "k2"}
	l := Plan(in)

	// The order is k6 (3m, held), k1 (2m), k2 (1m), k3 (30s).
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
	if !drawn["k6"] {
		t.Error("the held item has no targeted row")
	}
	if got, want := strings.Join(firstRow, ","), "k6,k1,k2,k3"; got != want {
		t.Errorf("first rows = %q, want %q", got, want)
	}

	// Stops: the held row first, then the running rows in row order, then one
	// per plan item drawn — the left entry of a two-column row first, then its
	// right entry.
	want := []Target{
		{Kind: TargetWork, Key: "k6"},
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

// TestFoldedHeldRowIsNoStop: the "✓ N finished" row is information, not a
// browse stop, so folding the held rows must not add one.
func TestFoldedHeldRowIsNoStop(t *testing.T) {
	base := busyInput()
	st := buildState{}
	applyShed(&st, 2) // fold the held rows into one row
	l := build(base, st)
	finish(&l, base)

	folded := false
	onRows := map[Target]bool{}
	for _, r := range l.Rows {
		if strings.Contains(ansi.Strip(r.Text), "finished") {
			folded = true
			if r.Target.Kind != TargetNone || len(r.Also) > 0 {
				t.Errorf("the folded held row carries a target: %+v %+v", r.Target, r.Also)
			}
		}
		if r.Target.Kind != TargetNone {
			onRows[r.Target] = true
		}
		for _, a := range r.Also {
			if a.Target.Kind != TargetNone {
				onRows[a.Target] = true
			}
		}
	}
	if !folded {
		t.Fatal("the fixture did not fold the held rows")
	}
	for _, s := range l.Stops {
		if !onRows[s] {
			t.Errorf("stop %+v is not a row target", s)
		}
	}
}

// TestEveryRowIsExactlyTheWidth pins the geometry: every rendered line is
// exactly the frame's width.
func TestEveryRowIsExactlyTheWidth(t *testing.T) {
	base := busyInput()
	for _, width := range []int{80, 89, 96, 120} {
		in := withMax(base, 40)
		in.Width = width
		out := Render(Plan(in), width)
		for i, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(line); got != width {
				t.Errorf("%d cols: line %d is %d cells, want %d: %q",
					width, i, got, width, ansi.Strip(line))
			}
		}
	}
}

// TestPlanBudgetBelowOneStillSheds pins the MaxRows contract: a budget below
// one sheds the tray down to the single compact row instead of growing without
// limit.
func TestPlanBudgetBelowOneStillSheds(t *testing.T) {
	in := busyInput()
	in.MaxRows = 0
	l := Plan(in)
	if len(l.Rows) != 1 {
		t.Fatalf("MaxRows 0 drew %d rows, want 1", len(l.Rows))
	}
}

// TestRenderStaysOneWidthWhenNarrow pins that a request narrower than the
// floor is still drawn at a consistent width.
func TestRenderStaysOneWidthWhenNarrow(t *testing.T) {
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
		t.Fatal("the cursor changed the tray's text; it must only paint it")
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

// TestHeldRowKeepsItsSlot: a held item between two running items by start time
// draws between them, untinted, with its outcome, summary, duration and mark.
func TestHeldRowKeepsItsSlot(t *testing.T) {
	in := Input{
		Width: 100, MaxRows: 40, Now: testNow, Spinner: "⠋",
		Work: []viewmodel.WorkItem{
			{TaskKey: "a", Kind: "command", DisplayID: "job-a", Label: "first", State: "running", StartedAt: testNow.Add(-3 * time.Minute)},
			{TaskKey: "c", Kind: "command", DisplayID: "job-c", Label: "third", State: "running", StartedAt: testNow.Add(-time.Minute)},
		},
		Held: []HeldItem{{
			Item: viewmodel.WorkItem{
				TaskKey: "b", Kind: "command", DisplayID: "job-b", Label: "second", State: "completed",
				StartedAt: testNow.Add(-2 * time.Minute),
			},
			Outcome:  "done",
			Summary:  "ok",
			Duration: 12 * time.Second,
		}},
	}
	l := Plan(in)
	if len(l.Rows) != 3 {
		t.Fatalf("rows = %d, want 3: %q", len(l.Rows), rowTexts(l))
	}
	if got := l.Rows[1].Target.Key; got != "b" {
		t.Fatalf("row 1 = %q, want the held item between the two running ones", got)
	}
	if l.Rows[1].Tint {
		t.Error("a held row must not be tinted")
	}
	text := ansi.Strip(l.Rows[1].Text)
	for _, want := range []string{"done · ok", "12s", "✓"} {
		if !strings.Contains(text, want) {
			t.Errorf("held row = %q, want it to contain %q", text, want)
		}
	}
}

// TestRowsDoNotMoveWhenOneBlocks: blocking the middle of three running items
// leaves the order unchanged when uncapped; capped at one, the blocked item is
// the one kept.
func TestRowsDoNotMoveWhenOneBlocks(t *testing.T) {
	items := []viewmodel.WorkItem{
		{TaskKey: "a", Kind: "command", DisplayID: "job-a", Label: "first", State: "running", StartedAt: testNow.Add(-3 * time.Minute)},
		{TaskKey: "b", Kind: "command", DisplayID: "job-b", Label: "second", State: "running", StartedAt: testNow.Add(-2 * time.Minute)},
		{TaskKey: "c", Kind: "command", DisplayID: "job-c", Label: "third", State: "running", StartedAt: testNow.Add(-time.Minute)},
	}
	order := func(l Layout) string {
		var keys []string
		for _, r := range l.Rows {
			if r.Target.Kind == TargetWork {
				keys = append(keys, r.Target.Key)
			}
		}
		return strings.Join(keys, ",")
	}

	before := order(Plan(Input{Width: 100, MaxRows: 40, Now: testNow, Work: items}))
	blocked := append([]viewmodel.WorkItem(nil), items...)
	blocked[1].State = "blocked"
	blocked[1].Prompt = &viewmodel.WorkPrompt{Kind: "approval"}
	after := order(Plan(Input{Width: 100, MaxRows: 40, Now: testNow, Work: blocked}))
	if before != after {
		t.Errorf("order changed when one item blocked: %q → %q", before, after)
	}

	// The cap is driven directly: a MaxRows of one sheds all the way to the
	// compact row, which has no work targets at all.
	one := build(Input{Width: 100, MaxRows: 40, Now: testNow, Work: blocked}, buildState{cap: 1})
	if got := order(one); got != "b" {
		t.Errorf("capped at one kept %q, want the blocked item b", got)
	}
}

// TestRunRowComesFirstAndIsNoStop: with a run active, row 0 is the plan-run
// row, and no stop names it.
func TestRunRowComesFirstAndIsNoStop(t *testing.T) {
	in := busyInput()
	in.Run = &RunRow{Title: "p-e-tray", Summary: "task 2/5 implement", StartedAt: testNow.Add(-4 * time.Minute)}
	l := Plan(in)
	if len(l.Rows) == 0 {
		t.Fatal("no rows")
	}
	first := ansi.Strip(l.Rows[0].Text)
	for _, want := range []string{"run", "p-e-tray", "task 2/5 implement"} {
		if !strings.Contains(first, want) {
			t.Errorf("row 0 = %q, want it to contain %q", first, want)
		}
	}
	if !l.Rows[0].Tint {
		t.Error("the plan-run row must be tinted")
	}
	for _, s := range l.Stops {
		if s.Kind == TargetWork && s.Key == "" {
			t.Errorf("the plan-run row must not be a stop: %+v", s)
		}
	}
	if n := len(l.Stops); n != 6+4 {
		t.Errorf("stops = %d, want the held row, five running rows and four plan items", n)
	}
}

// TestNoFrameRows: the tray draws no outline at all.
func TestNoFrameRows(t *testing.T) {
	out := Render(Plan(busyInput()), 100)
	for _, glyph := range []string{"╭", "├─", "╰", "│"} {
		if strings.Contains(out, glyph) {
			t.Errorf("tray contains the frame glyph %q:\n%s", glyph, ansi.Strip(out))
		}
	}
}
