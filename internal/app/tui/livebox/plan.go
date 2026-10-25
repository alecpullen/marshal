package livebox

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// planWindowItems is how many items the plan draws one by one; a longer list
// is summarised by the "✓ N done" and "+N more" entries.
const planWindowItems = 4

// planMarksMax is the longest plan the one-line form draws one mark per item.
const planMarksMax = 12

// planWindowAt is the plan's visible slice, plus the window's offset in the
// list so an entry can still name its item by its position: every item when
// the list is short enough, else up to PlanWindow from the anchor. The anchor
// is the first in-progress item, else the first item that is not completed.
// doneBefore counts the completed items before the anchor, and moreAfter
// counts the items the window leaves out.
func planWindowAt(items []PlanItem) (shown []PlanItem, start, doneBefore, moreAfter int) {
	if len(items) <= PlanWindow {
		return items, 0, 0, 0
	}
	start = -1
	for i, it := range items {
		if it.Status == "in_progress" {
			start = i
			break
		}
	}
	if start < 0 {
		for i, it := range items {
			if it.Status != "completed" {
				start = i
				break
			}
		}
	}
	if start < 0 {
		// Every item is completed, so there is no anchor to start from: the
		// window is empty and every item counts as done before it. A finished
		// plan draws its one finished row instead of item rows.
		start = len(items)
	}
	for _, it := range items[:start] {
		if it.Status == "completed" {
			doneBefore++
		}
	}
	end := min(start+planWindowItems, len(items))
	return items[start:end], start, doneBefore, len(items) - end
}

// planCounts counts the plan's completed items and its length.
func planCounts(items []PlanItem) (done, total int) {
	for _, it := range items {
		if it.Status == "completed" {
			done++
		}
	}
	return done, len(items)
}

// planFinished reports whether every item of a non-empty list is completed.
func planFinished(items []PlanItem) bool {
	done, total := planCounts(items)
	return total > 0 && done == total
}

// planOpen reports whether the plan still has work to draw: a finished plan is
// its one finished row.
func planOpen(items []PlanItem) bool {
	done, total := planCounts(items)
	return total > 0 && done < total
}

// planFocus is the item the plan is on: the in-progress one, else the first
// one that is not completed. "" when every item is completed.
func planFocus(items []PlanItem) string {
	for _, it := range items {
		if it.Status == "in_progress" {
			return it.Content
		}
	}
	for _, it := range items {
		if it.Status != "completed" {
			return it.Content
		}
	}
	return ""
}

// planMark is one item's status mark: ✓ in the success colour, the turn
// spinner in the primary accent, and the ambient dot muted.
func planMark(it PlanItem, spinner string) string {
	th := theme.Current()
	switch it.Status {
	case "completed":
		return lipgloss.NewStyle().Foreground(th.StatusSuccess).Render(glyph.OK)
	case "in_progress":
		if spinner == "" {
			spinner = glyph.Running
		}
		return lipgloss.NewStyle().Foreground(th.AccentPrimary).Render(spinner)
	default:
		return lipgloss.NewStyle().Foreground(th.FGMuted).Render(glyph.Ambient)
	}
}

// planKey names the item in a Target: its ID, else its 1-based position.
func planKey(it PlanItem, pos int) string {
	if it.ID != "" {
		return it.ID
	}
	return fmt.Sprintf("#%d", pos)
}

// planEntry is one cell of the plan: a window item, or the summary entry
// standing in for the items the window leaves out, or the stale notice.
type planEntry struct {
	mark    string // the styled status mark; "" for a summary entry
	text    string // the cleaned content, or the summary's label
	tail    string // the in-progress elapsed clock, styled; "" when none
	dim     bool   // completed text is muted
	warning bool   // the stale entry is painted in the warning colour
	target  Target
}

// planEntries is the window as the plan draws it: the window's items between a
// "✓ N done" entry and a "+N more" entry when the window trimmed the list, and
// the stale notice last when the plan is stale.
func planEntries(in Input) (entries []planEntry) {
	shown, start, doneBefore, moreAfter := planWindowAt(in.Plan)
	if doneBefore > 0 {
		entries = append(entries, planEntry{
			text: fmt.Sprintf("%s %d done", glyph.OK, doneBefore),
			dim:  true,
		})
	}
	for i, it := range shown {
		e := planEntry{
			mark:   planMark(it, in.Spinner),
			text:   clean(it.Content, 200),
			dim:    it.Status == "completed",
			target: Target{Kind: TargetPlan, Key: planKey(it, start+i+1)},
		}
		if it.Status == "in_progress" && !it.StartedAt.IsZero() {
			e.tail = muted(formatElapsed(max(in.Now.Sub(it.StartedAt), 0)))
		}
		entries = append(entries, e)
	}
	if moreAfter > 0 {
		entries = append(entries, planEntry{
			text: fmt.Sprintf("+%d more", moreAfter),
			dim:  true,
		})
	}
	if in.Stale > 0 {
		entries = append(entries, planEntry{
			text:    "stale " + formatElapsed(in.Stale),
			warning: true,
		})
	}
	return entries
}

// planCell renders one entry in room cells: its mark, a space, the content and
// the in-progress clock, truncated.
func planCell(e planEntry, room int) string {
	if room <= 0 {
		return ""
	}
	head, tail := planHeadTail(e)
	text := cut(e.text, max(room-ansi.StringWidth(head)-ansi.StringWidth(tail), 1))
	return ansi.Truncate(head+styledText(text, e.dim, e.warning)+tail, room, "…")
}

// planHeadTail is an entry's styled mark with its trailing space, and its
// styled elapsed clock with its leading space.
func planHeadTail(e planEntry) (head, tail string) {
	if e.mark != "" {
		head = e.mark + " "
	}
	if e.tail != "" {
		tail = " " + e.tail
	}
	return head, tail
}

// styledText mutes a completed entry's content, or paints the stale notice in
// the warning colour.
func styledText(s string, dim, warning bool) string {
	switch {
	case warning:
		return warn(s)
	case dim:
		return muted(s)
	}
	return s
}

// planRows is the plan as two columns of entries, filled column-major. The
// left column takes ceil(n/2) entries. Each row is indented one cell so its
// marks sit in the same column as history's glyphs. A finished plan is not
// drawn by it.
func planRows(in Input, w int) []Row {
	if !planOpen(in.Plan) {
		return nil
	}
	entries := planEntries(in)
	n := len(entries)
	inner := max(w-1, 3) // one cell of indent
	// The indent and the two-cell gap are charged before the columns are
	// split, so a full left column cannot push the right one past inner.
	cell := max((inner-3)/2, 1)
	left := (n + 1) / 2
	rows := make([]Row, 0, left)
	for i := 0; i < min(left, n); i++ {
		target := entries[i].target
		text := planCell(entries[i], cell)
		row := Row{Target: target}
		if j := i + left; j < n {
			gap := max(cell-ansi.StringWidth(text), 0) + 2
			col := 1 + ansi.StringWidth(text) + gap
			text += strings.Repeat(" ", gap) + planCell(entries[j], cell)
			switch {
			case target.Kind == TargetNone:
				row.Target = entries[j].target
			case entries[j].target.Kind != TargetNone:
				// The row carries two live entries: the second keeps its own
				// target so its click and its browse stop are not lost.
				row.Also = []RowStop{{Target: entries[j].target, Col: col}}
			}
		}
		row.Text = " " + ansi.Truncate(text, inner, "…")
		rows = append(rows, row)
	}
	return rows
}

// planLine is the plan as one row, with no border characters: the marks (or
// the blocks once the list is long), the position, the active item's text, and
// the stale notice last. It is indented one cell and has no target.
func planLine(in Input, w int) Row {
	if len(in.Plan) == 0 {
		return Row{}
	}
	done, total := planCounts(in.Plan)
	pos, active := fmt.Sprintf("%d/%d", done, total), ""
	for i, it := range in.Plan {
		if it.Status == "in_progress" {
			pos, active = fmt.Sprintf("%d/%d", i+1, total), clean(it.Content, 120)
			break
		}
	}
	text := " " + planMarks(in, planMarksMax, 8) + "  " + pos
	if active != "" {
		text += "  " + active
	}
	if in.Stale > 0 {
		text += "  " + warn("stale "+formatElapsed(in.Stale))
	}
	return Row{Text: ansi.Truncate(text, max(w, 1), "…")}
}

// planMarks draws one mark per item, separated by border-coloured spaces, or
// the plan's blocks once the list is longer than maxMarks items.
func planMarks(in Input, maxMarks, blocksCells int) string {
	if len(in.Plan) > maxMarks {
		done, total := planCounts(in.Plan)
		return Blocks(done, total, blocksCells)
	}
	sep := lipgloss.NewStyle().Foreground(theme.Current().BorderMuted).Render(" ")
	parts := make([]string, 0, len(in.Plan))
	for _, it := range in.Plan {
		parts = append(parts, planMark(it, in.Spinner))
	}
	return strings.Join(parts, sep)
}

// finishedRow is the plan's row once every item is completed: one row saying
// so, indented one cell, with no target.
func finishedRow(in Input, w int) Row {
	done, total := planCounts(in.Plan)
	text := lipgloss.NewStyle().Foreground(theme.Current().StatusSuccess).Render(glyph.OK) +
		fmt.Sprintf(" Plan %d/%d done", done, total)
	return Row{Text: ansi.Truncate(" "+text, max(w, 1), "…")}
}
