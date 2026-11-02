package livebox

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
)

// downloadItem is a running command with a measured total, so its card has
// every row.
func downloadItem(key, id string, started time.Time) acpwire.WorkItem {
	total := 3.7 * gib
	return acpwire.WorkItem{
		TaskKey: key, Kind: "command", DisplayID: id,
		Label: "Download qwen3-coder weights", State: "running",
		ProgressConfig: &acpwire.WorkProgressConfig{Profile: "download", Source: "events-v1"},
		Progress: &acpwire.WorkProgress{
			Sequence: 2, PrimaryMetricID: "bytes",
			Metrics: []acpwire.WorkMetric{
				{ID: "bytes", Label: "Downloaded", Unit: "bytes", Current: 2.1 * gib, Total: &total},
			},
			Detail:     "shard 7 of 12",
			ObservedAt: acpwire.Millis(testNow),
		},
		StartedAt: acpwire.Millis(started),
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
		Work: []acpwire.WorkItem{
			downloadItem("k1", "job-1", testNow.Add(-2*time.Minute)),
			{
				TaskKey: "k2", Kind: "agent", SourceID: "1", DisplayID: "#1",
				Label: "review the config change", State: "running",
				StartedAt: acpwire.Millis(testNow.Add(-time.Minute)),
			},
			{
				TaskKey: "k3", Kind: "command", DisplayID: "job-2",
				Label: "Harness tests", State: "running",
				OutputTail: "--- FAIL: TestX\nlast useful line\n",
				StartedAt:  acpwire.Millis(testNow.Add(-30 * time.Second)),
			},
			{
				TaskKey: "k4", Kind: "watch", DisplayID: "w-1",
				Label: "src/", State: "watching",
				StartedAt: acpwire.Millis(testNow.Add(-20 * time.Second)),
			},
			{
				TaskKey: "k5", Kind: "agent", SourceID: "2", DisplayID: "#2",
				Label: "review the diff", State: "blocked",
				Prompt:    &acpwire.WorkPrompt{Kind: "approval"},
				StartedAt: acpwire.Millis(testNow.Add(-10 * time.Second)),
			},
		},
		Held: []HeldItem{{
			Item: acpwire.WorkItem{
				TaskKey: "k6", Kind: "command", DisplayID: "job-0",
				Label: "go build ./...", State: "completed",
				StartedAt: acpwire.Millis(testNow.Add(-3 * time.Minute)),
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

// innerRows is a layout's rows inside the outline, without its border lines.
func innerRows(l Layout) []Row {
	out := make([]Row, 0, len(l.Rows))
	for _, r := range l.Rows {
		if r.Frame == FrameInner {
			out = append(out, r)
		}
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
// is its own framed section, the header in the top border over two plan rows,
// and the tray ends on its last plan row — it draws no closing row.
func TestIdlePlanDrawsItsRows(t *testing.T) {
	in := Input{Width: 80, MaxRows: 20, Now: testNow, Plan: planOf(4, 2, 3)}
	l := Plan(in)
	texts := rowTexts(l)
	if len(l.Rows) != 3 {
		t.Fatalf("rows = %d, want the top border and 2 plan rows: %q", len(l.Rows), texts)
	}
	if !strings.HasPrefix(texts[0], "╭─ plan") || !strings.Contains(texts[0], "2/4") {
		t.Errorf("top border = %q, want the plan header", texts[0])
	}
	if n := len(innerRows(l)); n != 2 {
		t.Errorf("inner rows = %d, want the plan's two rows", n)
	}
	out := strings.Split(ansi.Strip(Render(l, 80)), "\n")
	for _, want := range []string{"│ ✓ todo a", "│ ✓ todo b"} {
		if !strings.Contains(strings.Join(out, "\n"), want) {
			t.Errorf("Render = %q, want it to contain %q", out, want)
		}
	}
	if got := out[len(out)-1]; !strings.HasPrefix(got, "│ ✓ todo b") {
		t.Errorf("last line = %q, want the plan's last row", got)
	}
	if l.Open {
		t.Error("the tray ends on a plan row, so it is not open")
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
	if fullHeight != 16 {
		t.Fatalf("full height = %d, want 16 (top border, held, card+4 running, the two subagent cards on two rows each, plan header, 2 plan rows — no closing row)", fullHeight)
	}
	if full.Open {
		t.Error("the full tray ends on a plan row, so it is not open")
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
	if got := ansi.Strip(innerRows(byShed[0])[0].Text); !strings.Contains(got, "job-0") {
		t.Errorf("unshed first row = %q, want the held row first", got)
	}

	// Step 1: the plan's section folds into the tray's last row, drawn with
	// tees so the console continues under it.
	shed1 := rowTexts(byShed[1])
	if got := shed1[len(shed1)-1]; !strings.HasPrefix(got, "├─ plan") || !strings.Contains(got, "3/4") || !strings.Contains(got, "todo c") {
		t.Errorf("shed 1 last row = %q, want the one-line plan", got)
	}
	if !byShed[1].Open {
		t.Error("shed 1 ends on the folded plan's border row, so it must be open")
	}
	if n := strings.Count(joined(byShed[1]), "├─ plan"); n != 1 {
		t.Errorf("shed 1 tray = %q, want the plan section gone and only the folded line", joined(byShed[1]))
	}
	if n := len(byShed[1].Rows); n != 14 {
		t.Errorf("shed 1 rows = %d, want 14", n)
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

// TestOneExpandedCard pins that exactly one item opens and the rest keep their
// collapsed shape: one row each, except a subagent, whose card is two.
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
	for key, want := range map[string]int{"k1": 1, "k3": 1, "k4": 1, "k5": 2, "k6": 1} {
		if rows[key] != want {
			t.Errorf("item %q drew %d rows, want %d", key, rows[key], want)
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
		Work: []acpwire.WorkItem{
			{TaskKey: "a", Kind: "command", DisplayID: "job-a", Label: "first", State: "running", StartedAt: acpwire.Millis(testNow.Add(-3 * time.Minute))},
			{TaskKey: "c", Kind: "command", DisplayID: "job-c", Label: "third", State: "running", StartedAt: acpwire.Millis(testNow.Add(-time.Minute))},
		},
		Held: []HeldItem{{
			Item: acpwire.WorkItem{
				TaskKey: "b", Kind: "command", DisplayID: "job-b", Label: "second", State: "completed",
				StartedAt: acpwire.Millis(testNow.Add(-2 * time.Minute)),
			},
			Outcome:  "done",
			Summary:  "ok",
			Duration: 12 * time.Second,
		}},
	}
	l := Plan(in)
	rows := innerRows(l)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %q", len(rows), rowTexts(l))
	}
	if got := rows[1].Target.Key; got != "b" {
		t.Fatalf("row 1 = %q, want the held item between the two running ones", got)
	}
	if rows[1].Tint {
		t.Error("a held row must not be tinted")
	}
	if got := rowTexts(l)[0]; !strings.Contains(got, "running 2 · 1 finished") {
		t.Errorf("top border = %q, want the running and finished counts", got)
	}
	text := ansi.Strip(rows[1].Text)
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
	items := []acpwire.WorkItem{
		{TaskKey: "a", Kind: "command", DisplayID: "job-a", Label: "first", State: "running", StartedAt: acpwire.Millis(testNow.Add(-3 * time.Minute))},
		{TaskKey: "b", Kind: "command", DisplayID: "job-b", Label: "second", State: "running", StartedAt: acpwire.Millis(testNow.Add(-2 * time.Minute))},
		{TaskKey: "c", Kind: "command", DisplayID: "job-c", Label: "third", State: "running", StartedAt: acpwire.Millis(testNow.Add(-time.Minute))},
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
	blocked := append([]acpwire.WorkItem(nil), items...)
	blocked[1].State = "blocked"
	blocked[1].Prompt = &acpwire.WorkPrompt{Kind: "approval"}
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

// TestRunRowComesFirstAndIsNoStop: with a run active, the first row inside the
// frame is the plan-run row, and no stop names it.
func TestRunRowComesFirstAndIsNoStop(t *testing.T) {
	in := busyInput()
	in.Run = &RunRow{Title: "p-e-tray", Summary: "task 2/5 implement", StartedAt: testNow.Add(-4 * time.Minute)}
	l := Plan(in)
	rows := innerRows(l)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	first := ansi.Strip(rows[0].Text)
	for _, want := range []string{"run", "p-e-tray", "task 2/5 implement"} {
		if !strings.Contains(first, want) {
			t.Errorf("first row = %q, want it to contain %q", first, want)
		}
	}
	if !rows[0].Tint {
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

// TestTrayOpensAtTheBottom: the running work and the plan sit inside one
// outline, the running section's header in the top border and the plan's
// between them, and the tray draws no closing row: every row after the top
// border is inside the outline or a section header.
func TestTrayOpensAtTheBottom(t *testing.T) {
	l := Plan(busyInput())
	lines := strings.Split(ansi.Strip(Render(l, 100)), "\n")
	if got := lines[0]; !strings.HasPrefix(got, "╭─ running 5 · 1 finished ") || !strings.HasSuffix(got, "╮") {
		t.Errorf("top border = %q, want the running section's header", got)
	}
	plan := -1
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != 100 {
			t.Errorf("line %d is %d cells, want 100: %q", i, w, line)
		}
		if strings.HasPrefix(line, "╰") {
			t.Errorf("line %d = %q, want no closing row", i, line)
		}
		switch {
		case strings.HasPrefix(line, "├─ plan"):
			plan = i
		case i > 0:
			if !strings.HasPrefix(line, "│ ") || !strings.HasSuffix(line, " │") {
				t.Errorf("line %d = %q, want it inside the outline", i, line)
			}
		}
	}
	if plan < 0 {
		t.Fatalf("no plan section header:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[plan+1], "todo a") {
		t.Errorf("line after the plan header = %q, want the plan's first row", lines[plan+1])
	}
	if l.Open {
		t.Error("the tray ends on a plan row, so it is not open")
	}
}

// TestCompactRowHasNoOutline: a budget too small for the outline sheds to the
// one plain row, which is not a framed tray.
func TestCompactRowHasNoOutline(t *testing.T) {
	l := Plan(withMax(busyInput(), 2))
	if len(l.Rows) != 1 || l.Rows[0].Frame != FramePlain {
		t.Fatalf("rows = %q, want the one plain compact row", rowTexts(l))
	}
	if l.Framed() {
		t.Error("the compact row is not framed")
	}
	if l.Open {
		t.Error("the compact row has no border row, so it is not open")
	}
	if out := ansi.Strip(Render(l, 100)); strings.ContainsAny(out, "╭│╰") {
		t.Errorf("compact row = %q, want no outline", out)
	}
}

// TestFinishedPlanBesideWorkLeavesTheTrayOpen: a finished plan beside running
// work is its header alone, drawn as the tray's last row, so the tray stays
// open for the console.
func TestFinishedPlanBesideWorkLeavesTheTrayOpen(t *testing.T) {
	in := Input{
		Width: 100, MaxRows: 40, Now: testNow, Spinner: "⠋",
		Work: []acpwire.WorkItem{subagentItem()},
		Plan: planOf(4, 4, 0),
	}
	l := Plan(in)
	texts := rowTexts(l)
	last := texts[len(texts)-1]
	if !strings.HasPrefix(last, "├─ plan") || !strings.Contains(last, "4/4 done") {
		t.Errorf("last row = %q, want the finished plan's header", last)
	}
	if len(innerRows(l)) == 0 {
		t.Errorf("tray = %q, want the running work above the header", texts)
	}
	if !l.Open {
		t.Error("the tray ends on the plan's header border row, so it must be open")
	}
}

// TestRenderTintsTheWholeRow: a running card's tint runs edge to edge. Every
// styled segment ends in a reset, and the tint must come back after each one,
// or only the id column is tinted and the rest of the row is bare.
func TestRenderTintsTheWholeRow(t *testing.T) {
	in := Input{Now: testNow, Spinner: "⠋"}
	out := Render(Layout{Rows: itemRows(subagentItem(), in, InnerWidth(100))}, 100)
	b := borderStyle()
	for i, line := range strings.Split(out, "\n") {
		band, ok := strings.CutPrefix(line, b.Render("│ "))
		band, closed := strings.CutSuffix(band, b.Render(" │"))
		if !ok || !closed {
			t.Fatalf("row %d is not inside the outline: %q", i, line)
		}
		if !strings.HasPrefix(band, "\x1b[48") {
			t.Fatalf("row %d is not tinted: %q", i, line)
		}
		rest := strings.TrimSuffix(band, "\x1b[m")
		for {
			j := strings.Index(rest, "\x1b[m")
			if j < 0 {
				break
			}
			rest = rest[j+len("\x1b[m"):]
			if !strings.HasPrefix(rest, "\x1b[48") {
				t.Fatalf("row %d loses its tint after a styled segment: %q", i, line)
			}
		}
	}
}
