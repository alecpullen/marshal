package livebox

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// Plan assembles the box from its sections and sheds it until it fits
// in.MaxRows.
//
// The box exists while the turn is busy, while work is running, or while the
// live part has lines to draw: the live part's own busy flag stays set for an
// active call or streaming reasoning after the turn clock has stopped, and a
// live part with no box would be on screen nowhere. With none of those, a plan
// draws the idle row alone; with no plan either, the box has no rows.
func Plan(in Input) Layout {
	w := max(in.Width, 2)
	if !in.Busy && len(in.Work) == 0 && len(in.Live.Full) == 0 {
		if len(in.Plan) == 0 {
			return Layout{Mode: drawnMode(in.Mode, w)}
		}
		l := Layout{Mode: drawnMode(in.Mode, w), Rows: []Row{idleRow(in, w)}}
		finish(&l, in)
		return l
	}

	st := buildState{mode: in.Mode}
	l := build(in, st)
	if in.MaxRows > 0 {
		// Each step is applied in order and the box rebuilt; Shed is how many
		// steps it took to fit. Step 7 leaves one plain row, so the loop always
		// terminates.
		for step := 1; step <= shedSteps && len(l.Rows) > in.MaxRows; step++ {
			applyShed(&st, step)
			l = build(in, st)
			l.Shed = step
		}
	}
	finish(&l, in)
	return l
}

// Render paints a layout: every row on its own line, inner rows inside the
// outline, and a tinted or cursored row's content area band-painted.
func Render(l Layout, width int) string {
	w := max(width, 1)
	split := l.Mode == Split && w >= SplitMinWidth
	cell := InnerWidth(w)
	if split {
		cell = LeftWidth(w, l.Mode)
	}
	b := borderStyle()
	lines := make([]string, 0, len(l.Rows))
	for _, r := range l.Rows {
		if r.Frame != FrameInner {
			lines = append(lines, ansi.Truncate(r.Text, w, "…"))
			continue
		}
		text := padCells(ansi.Truncate(r.Text, cell, "…"), cell)
		switch {
		case r.Cursor:
			text = chrome.PaintBand(text, cell, theme.Current().BGSelection)
		case r.Tint:
			text = chrome.PaintBand(text, cell, theme.Current().BGSurface)
		}
		line := b.Render("│ ") + text
		if split {
			right := padCells(ansi.Truncate(r.Right, RightWidth, "…"), RightWidth)
			line += b.Render(" │ ") + right + b.Render(" │")
		} else {
			line += b.Render(" │")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// shedSteps is the length of the shed order.
const shedSteps = 7

// buildState is how much of the box the shed order has taken away.
type buildState struct {
	mode      Mode
	liveShort bool // step 2: the live part is Live.Short
	cap       int  // step 3 (RunningCap) and step 6 (1): running rows cap
	collapse  bool // step 4: the expanded card is a row
	liveOff   bool // step 5: the live part is gone
	compact   bool // step 7: one plain summary row
}

// applyShed applies shed step n (1-based).
func applyShed(st *buildState, step int) {
	switch step {
	case 1:
		st.mode = Folded
	case 2:
		st.liveShort = true
	case 3:
		st.cap = RunningCap
	case 4:
		st.collapse = true
	case 5:
		st.liveOff = true
	case 6:
		st.cap = 1
	case 7:
		st.compact = true
	}
}

// drawnMode is the mode as drawn: split falls back to stacked below its
// minimum width, and anything unknown is stacked.
func drawnMode(m Mode, w int) Mode {
	switch m {
	case Split:
		if w < SplitMinWidth {
			return Stacked
		}
		return Split
	case Folded:
		return Folded
	default:
		return Stacked
	}
}

// build assembles the box in one of its shapes.
func build(in Input, st buildState) Layout {
	w := max(in.Width, 2)
	mode := drawnMode(st.mode, w)
	l := Layout{Mode: mode}
	if st.compact {
		l.Rows = []Row{compactRow(in, w)}
		return l
	}
	inner := InnerWidth(w)
	left := LeftWidth(w, mode)

	live, liveTargets := liveLines(in.Live, st)
	work := runningOrder(in.Work)
	shown := work
	if st.cap > 0 && len(work) > st.cap {
		shown = work[:st.cap]
	}

	var body []Row
	for i, line := range live {
		var node viewmodel.NodeID
		if i < len(liveTargets) {
			node = liveTargets[i]
		}
		body = append(body, Row{Text: line, Target: Target{Kind: TargetLive, Node: node}})
	}
	rowW := inner
	if mode == Split {
		rowW = left
	}
	if len(work) > 0 {
		hdrW := w
		if mode == Split {
			hdrW = left + 4
		}
		body = append(body, Row{Text: sectionHeader(fmt.Sprintf("running %d", len(work)), hdrW), Frame: FrameBorder})
		for _, it := range shown {
			if in.Expanded != "" && in.Expanded == it.TaskKey && !st.collapse {
				body = append(body, card(it, in, rowW)...)
			} else {
				body = append(body, runningRow(it, in, rowW, mode == Split))
			}
		}
		if n := len(work) - len(shown); n > 0 {
			body = append(body, Row{
				Text:   muted(fmt.Sprintf("+%d more · /work", n)),
				Target: Target{Kind: TargetMore},
			})
		}
	}

	if mode == Split {
		budget := in.MaxRows - 2
		if budget < 1 {
			budget = planWindowItems + 2
		}
		lines := planSplit(in, budget)
		l.Rows = append(l.Rows, Row{Text: splitTop(in, w, left), Frame: FrameBorder})
		// The box is as tall as the longer side: the left content or the plan's
		// rows, whichever asks for more.
		for i := 0; i < max(len(body), len(lines)); i++ {
			var r Row
			if i < len(body) {
				r = body[i]
			}
			var right string
			if i < len(lines) {
				right = lines[i]
			}
			if r.Frame == FrameBorder {
				l.Rows = append(l.Rows, Row{Text: splitBorder(r.Text, right, left), Frame: FrameBorder})
				continue
			}
			r.Right = right
			l.Rows = append(l.Rows, r)
		}
		l.Rows = append(l.Rows, Row{Text: splitBottom(w, left), Frame: FrameBorder})
		return l
	}

	l.Rows = append(l.Rows, Row{Text: topLine(in.State, in.Hints, w, "─╮"), Frame: FrameBorder})
	l.Rows = append(l.Rows, body...)
	if mode == Stacked && len(in.Plan) > 0 {
		header, planRows := planStacked(in, inner)
		l.Rows = append(l.Rows, Row{Text: sectionHeader(header, w), Frame: FrameBorder})
		l.Rows = append(l.Rows, planRows...)
	}
	l.Rows = append(l.Rows, Row{Text: bottomLine(in, mode, w), Frame: FrameBorder})
	return l
}

// liveLines is the live part as the shed order leaves it.
func liveLines(l Live, st buildState) ([]string, []viewmodel.NodeID) {
	switch {
	case st.liveOff:
		return nil, nil
	case st.liveShort:
		return l.Short, l.ShortTargets
	default:
		return l.Full, l.Targets
	}
}

// runningOrder sorts a copy of the work for the running section: blocked items
// first, then the earliest started, then by task key.
func runningOrder(items []viewmodel.WorkItem) []viewmodel.WorkItem {
	out := append([]viewmodel.WorkItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		bi, bj := out[i].State == "blocked", out[j].State == "blocked"
		if bi != bj {
			return bi
		}
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].TaskKey < out[j].TaskKey
	})
	return out
}

// finish marks the cursor's row and collects the browse stops in row order: one
// per running item, the more row, then one per plan item drawn.
func finish(l *Layout, in Input) {
	seen := make(map[Target]bool, len(l.Rows))
	for i := range l.Rows {
		r := &l.Rows[i]
		if in.Cursor.Kind != TargetNone && sameTarget(r.Target, in.Cursor) {
			r.Cursor = true
		}
		switch r.Target.Kind {
		case TargetWork, TargetMore, TargetPlan:
			if seen[r.Target] {
				continue
			}
			seen[r.Target] = true
			l.Stops = append(l.Stops, r.Target)
		}
	}
}

// sameTarget compares a row's target with the cursor's: the same kind and key,
// and for a live row the same node.
func sameTarget(a, b Target) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == TargetLive {
		return a.Node == b.Node
	}
	return a.Key == b.Key
}

// borderStyle is the colour every outline glyph is drawn in.
func borderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().BorderMuted)
}

// topLine draws a top border segment of exactly w cells:
// ╭─ [state] ─…─ [hints] <close>, close being ─╮ or ─┬. Hints drop from the
// end before the state is cut.
func topLine(state string, hints []string, w int, close string) string {
	const open = "╭─"
	b := borderStyle()
	statePart := ""
	if state != "" {
		statePart = " " + state + " "
	}
	for len(hints) > 0 {
		hintPart := " " + strings.Join(hints, " · ") + " "
		fill := w - ansi.StringWidth(open) - ansi.StringWidth(statePart) -
			ansi.StringWidth(hintPart) - ansi.StringWidth(close)
		if fill >= 1 {
			return b.Render(open) + statePart + b.Render(strings.Repeat("─", fill)) +
				muted(hintPart) + b.Render(close)
		}
		hints = hints[:len(hints)-1]
	}
	if state != "" {
		// The state keeps its two surrounding spaces and the border's corners,
		// with the rest left to the fill.
		if room := w - ansi.StringWidth(open) - ansi.StringWidth(close) - 2; room >= 1 {
			statePart = " " + ansi.Truncate(state, room, "…") + " "
		} else {
			statePart = ""
		}
	}
	fill := max(w-ansi.StringWidth(open)-ansi.StringWidth(statePart)-ansi.StringWidth(close), 0)
	line := b.Render(open) + statePart + b.Render(strings.Repeat("─", fill)+close)
	if ansi.StringWidth(line) > w {
		return ansi.Truncate(line, w, "")
	}
	return line
}

// sectionHeader draws a left-aligned section header of exactly w cells:
// ├─ <label> ─…─┤.
func sectionHeader(label string, w int) string {
	b := borderStyle()
	label = ansi.Truncate(label, max(w-6, 1), "…")
	fill := max(w-5-ansi.StringWidth(label), 1)
	return b.Render("├─ ") + label + b.Render(" "+strings.Repeat("─", fill)+"┤")
}

// splitTop is the split layout's top border: the left segment closes with ┬ at
// the split column, and the plan header sits in the right pane.
func splitTop(in Input, w, left int) string {
	leftW := left + 4
	header := ""
	if len(in.Plan) > 0 {
		header = planHeader(in)
	}
	return topLine(in.State, in.Hints, leftW, "─┬") + splitTopRight(header, w-leftW)
}

// splitTopRight draws the top border over the right pane, which holds the plan
// header when there is a plan.
func splitTopRight(header string, w int) string {
	b := borderStyle()
	if header == "" {
		return b.Render(strings.Repeat("─", max(w-1, 0)) + "╮")
	}
	header = ansi.Truncate(header, max(w-6, 1), "…")
	fill := max(w-5-ansi.StringWidth(header), 1)
	return b.Render("─ ") + header + b.Render(" "+strings.Repeat("─", fill)+"─╮")
}

// splitBorder draws a section header across the split layout: the left segment
// padded to the split column, then the right pane's line for that row.
func splitBorder(leftSeg, right string, left int) string {
	b := borderStyle()
	leftSeg = padCells(ansi.Truncate(leftSeg, left+4, "…"), left+4)
	right = padCells(ansi.Truncate(right, RightWidth, "…"), RightWidth)
	return leftSeg + b.Render(" ") + right + b.Render(" │")
}

// splitBottom is the split layout's bottom border, ┴ at the split column.
func splitBottom(w, left int) string {
	b := borderStyle()
	return b.Render("╰" + strings.Repeat("─", left+2) + "┴" +
		strings.Repeat("─", max(w-left-5, 0)) + "╯")
}

// bottomLine is the bottom border: the folded plan when there is one, else the
// plain outline.
func bottomLine(in Input, mode Mode, w int) string {
	if mode == Folded && len(in.Plan) > 0 {
		return planFolded(in, w)
	}
	return borderStyle().Render("╰" + strings.Repeat("─", max(w-2, 0)) + "╯")
}

// compactRow is the box as one plain row: the state, the counts of running
// kinds, and the plan's progress.
func compactRow(in Input, w int) Row {
	parts := make([]string, 0, 5)
	if strings.TrimSpace(ansi.Strip(in.State)) != "" {
		parts = append(parts, in.State)
	}
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
	// Any other kind still counts: the box must not hide work.
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
	return Row{Text: ansi.Truncate(strings.Join(parts, " "), max(w, 1), "…"), Frame: FramePlain}
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
