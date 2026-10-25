package livebox

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// Plan assembles the tray from its sections and sheds it until it fits
// in.MaxRows.
//
// The tray has rows while a run is active, while work is running or held, or
// while there is a plan to show. With none of those it has no rows at all.
func Plan(in Input) Layout {
	if in.MaxRows < 1 {
		// The contract is "the most rows the tray may take", so a budget below
		// one still sheds down to the single compact row rather than growing
		// without limit.
		in.MaxRows = 1
	}
	if in.Run == nil && len(in.Work) == 0 && len(in.Held) == 0 && len(in.Plan) == 0 {
		return Layout{}
	}

	st := buildState{}
	l := build(in, st)
	// Each step is applied in order and the tray rebuilt; Shed is how many
	// steps it took to fit. Step 6 leaves one compact row, so the loop always
	// terminates.
	for step := 1; step <= shedSteps && len(l.Rows) > in.MaxRows; step++ {
		applyShed(&st, step)
		l = build(in, st)
		l.Shed = step
	}
	finish(&l, in)
	return l
}

// Render paints a layout: every row on its own line, truncated and padded to
// exactly width cells, with a tinted or cursored row band-painted. There is no
// outline. A request narrower than the floor is drawn at the floor, so every
// line is still the same width.
func Render(l Layout, width int) string {
	w := max(width, 5)
	lines := make([]string, 0, len(l.Rows))
	for _, r := range l.Rows {
		text := padCells(ansi.Truncate(r.Text, w, "…"), w)
		switch {
		case r.Cursor:
			text = chrome.PaintBand(text, w, theme.Current().BGSelection)
		case r.Tint:
			text = chrome.PaintBand(text, w, theme.Current().BGSurface)
		}
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

// shedSteps is the length of the shed order.
const shedSteps = 6

// buildState is how much of the tray the shed order has taken away.
type buildState struct {
	planOneLine bool // step 1: the plan is its one-row form
	heldFolded  bool // step 2: the held rows are one "✓ N finished" row
	cap         int  // step 3 (RunningCap) and step 5 (1): running rows cap
	collapse    bool // step 4: the expanded card is a row
	compact     bool // step 6: one compact row
}

// applyShed applies shed step n (1-based).
func applyShed(st *buildState, step int) {
	switch step {
	case 1:
		st.planOneLine = true
	case 2:
		st.heldFolded = true
	case 3:
		st.cap = RunningCap
	case 4:
		st.collapse = true
	case 5:
		st.cap = 1
	case 6:
		st.compact = true
	}
}

// build assembles the tray in one of its shapes.
func build(in Input, st buildState) Layout {
	w := max(in.Width, 5)
	if st.compact {
		return Layout{Rows: []Row{compactRow(in, w)}}
	}

	var rows []Row
	if in.Run != nil {
		rows = append(rows, runRow(*in.Run, in, w))
	}

	entries := mergedOrder(in, st.heldFolded)
	shown := entries
	if st.cap > 0 {
		shown = capEntries(entries, st.cap)
	}
	for _, e := range shown {
		if e.held != nil {
			rows = append(rows, heldRow(*e.held, in, w))
			continue
		}
		if in.Expanded != "" && in.Expanded == e.item.TaskKey && !st.collapse {
			rows = append(rows, card(e.item, in, w)...)
		} else {
			rows = append(rows, itemRows(e.item, in, w)...)
		}
	}
	if n := len(entries) - len(shown); n > 0 {
		rows = append(rows, Row{
			Text:   muted(fmt.Sprintf("+%d more · /work", n)),
			Target: Target{Kind: TargetMore},
		})
	}
	if st.heldFolded {
		if n := len(in.Held); n > 0 {
			rows = append(rows, finishedCountRow(n))
		}
	}

	if len(in.Plan) > 0 {
		switch {
		case planFinished(in.Plan):
			rows = append(rows, finishedRow(in, w))
		case st.planOneLine:
			rows = append(rows, planLine(in, w))
		default:
			rows = append(rows, planRows(in, w)...)
		}
	}
	return Layout{Rows: rows}
}

// mergedEntry is one row of the running-and-held stack: a running item, or a
// held one, with the start time and key that fix its slot.
type mergedEntry struct {
	item    viewmodel.WorkItem
	held    *HeldItem
	start   time.Time
	key     string
	blocked bool
}

// mergedOrder sorts the running and held rows by start time, then task key, so
// a row never moves: becoming blocked does not reorder it. Held rows are left
// out when the shed order has folded them into one row.
func mergedOrder(in Input, heldFolded bool) []mergedEntry {
	entries := make([]mergedEntry, 0, len(in.Work)+len(in.Held))
	for _, it := range in.Work {
		entries = append(entries, mergedEntry{
			item:    it,
			start:   it.StartedAt,
			key:     it.TaskKey,
			blocked: it.State == "blocked",
		})
	}
	if !heldFolded {
		for i := range in.Held {
			h := &in.Held[i]
			entries = append(entries, mergedEntry{
				held:  h,
				start: h.Item.StartedAt,
				key:   h.Item.TaskKey,
			})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].start.Equal(entries[j].start) {
			return entries[i].start.Before(entries[j].start)
		}
		return entries[i].key < entries[j].key
	})
	// A snapshot should never carry a duplicate TaskKey, but one would share a
	// keep slot in capEntries and let the tray exceed its row budget, so the
	// first entry for a key wins.
	seen := make(map[string]bool, len(entries))
	out := entries[:0]
	for _, e := range entries {
		if seen[e.key] {
			continue
		}
		seen[e.key] = true
		out = append(out, e)
	}
	return out
}

// capEntries keeps at most n entries, blocked ones first, and returns the kept
// entries in start order.
func capEntries(entries []mergedEntry, n int) []mergedEntry {
	if n <= 0 || len(entries) <= n {
		return entries
	}
	keep := make(map[string]bool, n)
	for _, e := range entries {
		if len(keep) >= n {
			break
		}
		if e.blocked {
			keep[e.key] = true
		}
	}
	for _, e := range entries {
		if len(keep) >= n {
			break
		}
		keep[e.key] = true
	}
	out := make([]mergedEntry, 0, n)
	for _, e := range entries {
		if keep[e.key] {
			out = append(out, e)
		}
	}
	return out
}

// finish marks the cursor's row and collects the browse stops in row order: one
// per running or held row, the more row, then one per plan item drawn. The
// plan-run row and the "✓ N finished" row carry no target, so they are not
// stops.
func finish(l *Layout, in Input) {
	seen := make(map[Target]bool, len(l.Rows))
	for i := range l.Rows {
		r := &l.Rows[i]
		if in.Cursor.Kind != TargetNone && (sameTarget(r.Target, in.Cursor) ||
			anySameTarget(r.Also, in.Cursor)) {
			r.Cursor = true
		}
		for _, t := range append([]Target{r.Target}, alsoTargets(r.Also)...) {
			switch t.Kind {
			case TargetWork, TargetMore, TargetPlan:
				if seen[t] {
					continue
				}
				seen[t] = true
				l.Stops = append(l.Stops, t)
			}
		}
	}
}

// alsoTargets is a row's secondary targets, in draw order.
func alsoTargets(stops []RowStop) []Target {
	out := make([]Target, 0, len(stops))
	for _, s := range stops {
		out = append(out, s.Target)
	}
	return out
}

// anySameTarget reports whether any of a row's secondary targets is the
// cursor's.
func anySameTarget(stops []RowStop, cursor Target) bool {
	for _, s := range stops {
		if sameTarget(s.Target, cursor) {
			return true
		}
	}
	return false
}

// sameTarget compares a row's target with the cursor's: the same kind and key.
func sameTarget(a, b Target) bool {
	return a.Kind == b.Kind && a.Key == b.Key
}

// finishedCountRow is the folded held rows: one row saying how many finished
// items the tray is holding. It has no target.
func finishedCountRow(n int) Row {
	return Row{Text: lipgloss.NewStyle().Foreground(theme.Current().StatusSuccess).Render(glyph.OK) +
		muted(fmt.Sprintf(" %d finished", n))}
}

// compactRow is the tray as one plain row: the counts of running kinds and the
// plan's progress, joined by single spaces.
func compactRow(in Input, w int) Row {
	parts := make([]string, 0, 5)
	counts := make(map[string]int, len(in.Work))
	for _, it := range in.Work {
		counts[it.Kind]++
	}
	for _, kind := range []string{"agent", "command", "watch"} {
		if n := counts[kind]; n > 0 {
			parts = append(parts, countCell(kind, n))
			delete(counts, kind)
		}
	}
	// Any other kind still counts: the tray must not hide work.
	rest := make([]string, 0, len(counts))
	for kind := range counts {
		rest = append(rest, kind)
	}
	sort.Strings(rest)
	for _, kind := range rest {
		parts = append(parts, countCell(kind, counts[kind]))
	}
	if len(in.Plan) > 0 {
		done, total := planCounts(in.Plan)
		parts = append(parts, muted(fmt.Sprintf("plan %d/%d", done, total)))
	}
	return Row{Text: ansi.Truncate(strings.Join(parts, " "), max(w, 1), "…")}
}

// countCell is one kind's glyph in its colour and its count muted.
func countCell(kind string, n int) string {
	g, c := kindStyle(kind)
	return lipgloss.NewStyle().Foreground(c).Render(g) + muted(strconv.Itoa(n))
}

// padCells pads s with spaces to exactly w cells. Callers truncate first.
func padCells(s string, w int) string {
	if gap := w - ansi.StringWidth(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}
