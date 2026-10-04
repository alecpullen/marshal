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
func (m Model) stripMaxRows() int {
	if m.height < nowBarCompactHeight {
		return 3
	}
	return 6
}

// todoStripRows is the height the pinned todo list takes above the transcript.
func (m Model) todoStripRows() int {
	return len(stripWindow(m.todoStrip, m.stripMaxRows()).rows)
}

// stripRow is one line of the strip: a todo, or a "✓ 3 done" / "+2 more" count.
type stripRow struct {
	todo  *db.TodoItem
	index int // 1-based position of the todo in the list
	text  string
}

type stripView struct {
	rows  []stripRow
	total int
}

// stripWindow picks the rows to show. A list that fits is shown whole. A
// longer one is a window around the active todo, with the finished ones
// before it and the waiting ones after it counted on one line each.
func stripWindow(todos []db.TodoItem, maxRows int) stripView {
	n := len(todos)
	v := stripView{total: n}
	if n == 0 {
		return v
	}
	active := -1
	for i, t := range todos {
		if t.Status == "in_progress" {
			active = i
			break
		}
	}
	if active < 0 {
		for i, t := range todos {
			if t.Status != "completed" {
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
		// Each summary row ("N done", "+N more") costs a row of the budget.
		// Try one todo of context before the active one; when that leaves no
		// room for the active todo itself (a tiny budget), start at it.
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
		v.rows = append(v.rows, stripRow{text: fmt.Sprintf("%d done", start)})
	}
	for i := start; i < end; i++ {
		t := todos[i]
		v.rows = append(v.rows, stripRow{todo: &t, index: i + 1})
	}
	if end < n {
		v.rows = append(v.rows, stripRow{text: fmt.Sprintf("+%d more", n-end)})
	}
	return v
}

// renderTodoStrip draws the pinned list: "✓ 1/4 Read", "▸ 2/4 Write",
// "· 3/4 Test". It is empty unless the last turn is working from the list.
func (m Model) renderTodoStrip() string {
	view := stripWindow(m.todoStrip, m.stripMaxRows())
	if len(view.rows) == 0 {
		return ""
	}
	th := theme.Current()
	room := max(m.leftWidth-gutterWidth, 1)
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
	return strings.Join(lines, "\n")
}
