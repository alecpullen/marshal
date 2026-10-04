package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/db"
)

// stripMaxRows is the most rows the pinned todo list takes, summary rows
// included; a short terminal gets fewer so the transcript keeps its room.
// The strip and the band below the transcript share this budget (see
// bandMaxRows), so the two surfaces together never cost the transcript more
// than the strip used to on its own.
func (m Model) stripMaxRows() int {
	if m.height < nowBarCompactHeight {
		return 3
	}
	return 6
}

// todoRef is a todo with its 1-based position in the full list. Both
// surfaces number todos against the list the agent wrote, so a row reads
// "3/4" whether it is drawn into the strip above the transcript or the band
// below it.
type todoRef struct {
	todo  db.TodoItem
	index int
}

// activeRefs is the strip's half of the list: the finished todos, whose work
// has been collapsed into them, and the one in progress. The waiting todos
// belong to the band instead, so they are not repeated at the top.
func activeRefs(todos []db.TodoItem) []todoRef {
	return todoRefs(todos, func(t db.TodoItem) bool {
		return t.Status == "completed" || t.Status == "in_progress"
	})
}

// pendingRefs is the band's half of the list: the todos the agent has not
// started. They stack at the bottom of the transcript so the top strip reads
// as the work done and under way.
func pendingRefs(todos []db.TodoItem) []todoRef {
	return todoRefs(todos, func(t db.TodoItem) bool {
		return t.Status != "completed" && t.Status != "in_progress"
	})
}

func todoRefs(todos []db.TodoItem, keep func(db.TodoItem) bool) []todoRef {
	refs := make([]todoRef, 0, len(todos))
	for i, t := range todos {
		if keep(t) {
			refs = append(refs, todoRef{todo: t, index: i + 1})
		}
	}
	return refs
}

// todoStripView is what the strip above the transcript shows.
func (m Model) todoStripView() stripView {
	return todoWindow(activeRefs(m.todoStrip), len(m.todoStrip), m.stripMaxRows(),
		func(n int) string { return fmt.Sprintf("%d done", n) },
		func(n int) string { return fmt.Sprintf("+%d more", n) })
}

// todoBandView is what the band below the transcript shows: the waiting
// todos, in list order, under the work already done.
//
// It is a plain head window rather than a window around the active todo:
// none of these todos is in progress, so there is no focus to centre on and
// what matters is what comes next.
func (m Model) todoBandView() stripView {
	refs := pendingRefs(m.todoStrip)
	v := stripView{total: len(m.todoStrip)}
	maxRows := m.bandMaxRows()
	if len(refs) > maxRows {
		v.rows = append(v.rows, stripRow{text: fmt.Sprintf("+%d waiting", len(refs)-maxRows)})
		refs = refs[:maxRows]
	}
	for _, r := range refs {
		t := r.todo
		v.rows = append(v.rows, stripRow{todo: &t, index: r.index})
	}
	return v
}

// todoStripRows is the height the pinned todo list takes above the transcript.
func (m Model) todoStripRows() int { return len(m.todoStripView().rows) }

// todoBandRows is the height the waiting-todo band takes below the
// transcript, above the now bar.
func (m Model) todoBandRows() int { return len(m.todoBandView().rows) }

// bandMaxRows is what is left of the strip's row budget once the strip has
// taken its own rows, so the two surfaces together stay within
// stripMaxRows and a long plan cannot squeeze the transcript twice over.
// Early in a turn the strip is short (the active todo and little else) and
// the band gets most of the budget; once the finished stack fills the strip
// the band is down to the one row that counts the rest.
func (m Model) bandMaxRows() int {
	used := len(m.todoStripView().rows)
	return max(m.stripMaxRows()-used, 1)
}

// stripRow is one line of a todo surface: a todo, or a summary count
// standing in for the todos that did not fit.
type stripRow struct {
	todo  *db.TodoItem
	index int // 1-based position of the todo in the full list
	text  string
}

type stripView struct {
	rows  []stripRow
	total int
}

// todoWindow picks the rows to show. A set that fits is shown whole. A
// longer one is a window around the active todo, with the todos before and
// after it counted on one line each by leading and trailing. total is the
// length of the whole list, which is what the rows number against — the set
// passed in is only one half of it.
func todoWindow(refs []todoRef, total, maxRows int, leading, trailing func(int) string) stripView {
	n := len(refs)
	v := stripView{total: total}
	if n == 0 {
		return v
	}
	active := -1
	for i, r := range refs {
		if r.todo.Status == "in_progress" {
			active = i
			break
		}
	}
	if active < 0 {
		for i, r := range refs {
			if r.todo.Status != "completed" {
				active = i
				break
			}
		}
	}
	if active < 0 {
		active = n - 1
	}
	start, end := 0, n
	if n > maxRows {
		// Each summary row costs a row of the budget. Try one todo of
		// context before the active one; when that leaves no room for the
		// active todo itself (a tiny budget), start at it.
		window := func(from int) (int, int) {
			visible := maxRows
			if from > 0 {
				visible--
			}
			to := min(from+visible, n)
			if to < n {
				visible--
				to = from + max(visible, 1)
			}
			return from, to
		}
		start, end = window(max(active-1, 0))
		if active >= end {
			start, end = window(active)
		}
	}
	if start > 0 {
		v.rows = append(v.rows, stripRow{text: leading(start)})
	}
	for i := start; i < end; i++ {
		t := refs[i].todo
		v.rows = append(v.rows, stripRow{todo: &t, index: refs[i].index})
	}
	if end < n {
		v.rows = append(v.rows, stripRow{text: trailing(n - end)})
	}
	return v
}

// stripWindow windows a whole list, numbering against itself. It is the
// general form of todoWindow, used where a surface draws the entire list.
func stripWindow(todos []db.TodoItem, maxRows int) stripView {
	refs := make([]todoRef, 0, len(todos))
	for i, t := range todos {
		refs = append(refs, todoRef{todo: t, index: i + 1})
	}
	return todoWindow(refs, len(todos), maxRows,
		func(n int) string { return fmt.Sprintf("%d done", n) },
		func(n int) string { return fmt.Sprintf("+%d more", n) })
}

// renderTodoRows draws a window's rows: a todo, or a summary count. The
// strip above the transcript and the band below it share it, so a todo reads
// the same wherever it is shown.
func renderTodoRows(view stripView, width int) []string {
	if len(view.rows) == 0 {
		return nil
	}
	th := theme.Current()
	room := max(width-gutterWidth, 1)
	lines := make([]string, 0, len(view.rows))
	for _, r := range view.rows {
		if r.todo == nil {
			g, gc := glyph.Ambient, th.FGMuted
			if strings.HasSuffix(r.text, "done") {
				g, gc = glyph.OK, th.StatusSuccess
			}
			lines = append(lines, gutterPrefix(g, gc)+mutedStyle().Render(r.text))
			continue
		}
		pos := fmt.Sprintf("%d/%d", r.index, view.total)
		title := ansi.Truncate(r.todo.Content, max(room-ansi.StringWidth(pos)-1, 1), "…")
		switch r.todo.Status {
		case "completed":
			lines = append(lines, gutterPrefix(glyph.OK, th.StatusSuccess)+mutedStyle().Render(pos+" "+title))
		case "in_progress":
			lines = append(lines, gutterPrefix(glyph.Running, accentColor)+
				mutedStyle().Render(pos)+" "+lipgloss.NewStyle().Bold(true).Foreground(th.FGEmphasis).Render(title))
		default:
			lines = append(lines, gutterPrefix(glyph.Ambient, th.FGMuted)+mutedStyle().Render(pos+" "+title))
		}
	}
	return lines
}

// renderTodoStrip draws the strip pinned above the transcript: the finished
// todos, whose work has collapsed into them, and the one in progress
// ("✓ 1/4 Read", "▸ 2/4 Write"). The waiting todos are the band's, below
// the transcript. It is empty unless the last turn is working from the list.
func (m Model) renderTodoStrip() string {
	return strings.Join(renderTodoRows(m.todoStripView(), m.leftWidth), "\n")
}

// renderTodoBand draws the band pinned below the transcript, above the now
// bar: the todos the agent has not started yet ("· 3/4 Test", "· 4/4 Ship").
// They sit under the work rather than above it, so the top of the screen is
// the work done and the bottom is what is still to come.
func (m Model) renderTodoBand() string {
	return strings.Join(renderTodoRows(m.todoBandView(), m.leftWidth), "\n")
}
