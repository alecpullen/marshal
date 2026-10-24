package livebox

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// planWindowItems is how many items the plan section draws one by one; a
// longer list is summarised by the "✓ N done" and "+N more" entries.
const planWindowItems = 4

// planMarksMax is the longest plan the folded border draws one mark per item.
const planMarksMax = 12

// planWindow is the plan's visible slice: every item when the list is short
// enough, else up to planWindowItems from the anchor. The anchor is the first
// in-progress item, else the first item that is not completed. doneBefore
// counts the completed items before the anchor, and moreAfter counts the items
// the window leaves out.
func planWindow(items []PlanItem) (shown []PlanItem, doneBefore, moreAfter int) {
	shown, _, doneBefore, moreAfter = planWindowAt(items)
	return shown, doneBefore, moreAfter
}

// planWindowAt is planWindow plus the window's offset in the list, so an entry
// can still name its item by its position.
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
		// window is empty and every item counts as done before it. The
		// section draws no item rows for a finished plan (decision 8).
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

// planOpen reports whether the plan still has work to draw: a finished plan is
// its header alone (decision 8).
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

// planEntry is one cell of the plan section: a window item, or the summary
// entry standing in for the items the window leaves out.
type planEntry struct {
	mark   string // the styled status mark; "" for a summary entry
	text   string // the cleaned content, or the summary's label
	tail   string // the in-progress elapsed clock, styled; "" when none
	dim    bool   // completed text is muted
	target Target
}

// planEntries is the window as the section draws it: the window's items
// between a "✓ N done" entry and a "+N more" entry when the window trimmed the
// list.
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
	return ansi.Truncate(head+styledText(text, e.dim)+tail, room, "…")
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

// styledText mutes a completed entry's content.
func styledText(s string, dim bool) string {
	if dim {
		return muted(s)
	}
	return s
}

// planHeader is the plan section's header, which the stacked layout puts in
// the top border's segment: the label, the blocks, the counts, the stale age
// and the finished notice.
func planHeader(in Input) string {
	done, total := planCounts(in.Plan)
	header := "plan  " + Blocks(done, total, 10) + fmt.Sprintf(" %d/%d", done, total)
	if in.Stale > 0 {
		header += "  " + warn("stale "+formatElapsed(in.Stale))
	}
	if total > 0 && done == total {
		header += " done"
	}
	return header
}

// planStacked is the stacked plan section: the header and the window as two
// columns of entries, filled column-major. The left column takes ceil(n/2)
// entries. A finished plan is the header alone (decision 8).
func planStacked(in Input, w int) (header string, rows []Row) {
	header = planHeader(in)
	if !planOpen(in.Plan) {
		return header, nil
	}
	entries := planEntries(in)
	n := len(entries)
	w = max(w, 3)
	cell := max((w-2)/2, 1)
	left := (n + 1) / 2
	for i := 0; i < min(left, n); i++ {
		target := entries[i].target
		text := planCell(entries[i], cell)
		if j := i + left; j < n {
			gap := max(cell-ansi.StringWidth(text), 0) + 2
			text += strings.Repeat(" ", gap) + planCell(entries[j], cell)
			if target.Kind == TargetNone {
				target = entries[j].target
			}
		}
		rows = append(rows, Row{Text: ansi.Truncate(text, w, "…"), Target: target})
	}
	return header, rows
}

// planSplit is the split layout's right pane, one entry per line at
// RightWidth. An entry whose content is longer than the pane wraps onto one
// continuation line indented by two cells, then truncates. The header sits in
// the top border (Task 5), so this is entries only. A finished plan draws no
// lines: its header is the whole section (decision 8).
func planSplit(in Input, rows int) []string {
	if rows <= 0 || !planOpen(in.Plan) {
		return nil
	}
	entries := planEntries(in)
	out := make([]string, 0, min(rows, len(entries)))
	for _, e := range entries {
		if len(out) >= rows {
			break
		}
		head, tail := planHeadTail(e)
		first, rest := wrapCells(e.text, max(RightWidth-ansi.StringWidth(head), 1))
		if rest == "" {
			out = append(out, ansi.Truncate(head+styledText(first, e.dim)+tail, RightWidth, "…"))
			continue
		}
		if len(out)+1 >= rows {
			// There is no room for the continuation line, so the entry
			// truncates on one line instead of wrapping.
			out = append(out, planCell(e, RightWidth))
			continue
		}
		out = append(out, ansi.Truncate(head+styledText(first, e.dim), RightWidth, "…"))
		room := max(RightWidth-2-ansi.StringWidth(tail), 1)
		out = append(out, "  "+ansi.Truncate(styledText(cut(rest, room), e.dim)+tail, RightWidth-2, "…"))
	}
	return out
}

// wrapCells splits s after room cells: the first line, and the rest.
func wrapCells(s string, room int) (first, rest string) {
	lines := strings.SplitN(ansi.Hardwrap(s, max(room, 1), false), "\n", 2)
	if len(lines) == 1 {
		return lines[0], ""
	}
	return lines[0], lines[1]
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

// planFolded is the folded layout's bottom border: the label and the marks (or
// the blocks once the list is long), the position, the active item's text, and
// the border fill to ╯. Everything but the marks is in BorderMuted.
func planFolded(in Input, width int) string {
	border := lipgloss.NewStyle().Foreground(theme.Current().BorderMuted)
	width = max(width, 2)
	if len(in.Plan) == 0 {
		return border.Render("╰" + strings.Repeat("─", width-2) + "╯")
	}
	done, total := planCounts(in.Plan)
	pos, active := fmt.Sprintf("%d/%d", done, total), ""
	for i, it := range in.Plan {
		if it.Status == "in_progress" {
			pos, active = fmt.Sprintf("%d/%d", i+1, total), clean(it.Content, 120)
			break
		}
	}
	line := border.Render("╰─ plan ") + planMarks(in, planMarksMax, 8) + border.Render(" "+pos)
	// The text keeps the closing " ─", at least one fill cell and ╯.
	if room := width - ansi.StringWidth(line) - ansi.StringWidth(" ─╯") - 1; active != "" && room > 0 {
		line += border.Render(" ") + border.Render(cut(active, room))
	}
	line += border.Render(" ─")
	if fill := width - ansi.StringWidth(line) - 1; fill > 0 {
		line += border.Render(strings.Repeat("─", fill))
	}
	return line + border.Render("╯")
}

// idleRow is the plan's row when the box is not drawn: the finished plan, or
// the position and the item the plan is on.
func idleRow(in Input, width int) Row {
	th := theme.Current()
	done, total := planCounts(in.Plan)
	var text string
	if total > 0 && done == total {
		text = lipgloss.NewStyle().Foreground(th.StatusSuccess).Render(glyph.OK) +
			fmt.Sprintf(" Plan %d/%d done", done, total)
	} else {
		text = lipgloss.NewStyle().Foreground(th.AccentPrimary).Render(glyph.Running) +
			fmt.Sprintf(" Plan %d/%d", done, total)
		if focus := clean(planFocus(in.Plan), 120); focus != "" {
			text += " · " + focus
		}
	}
	return Row{Text: ansi.Truncate(text, max(width, 1), "…"), Frame: FramePlain}
}
