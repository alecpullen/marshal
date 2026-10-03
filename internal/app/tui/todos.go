package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/commands"
	"marshal/internal/tools/native"
)

// todoLine renders one todo row: ✓ teal done, ▸ coral bold in-progress,
// · muted pending — matching native.TodoItem's statuses exactly.
func todoLine(t native.TodoItem, width int) string {
	var g string
	var c color.Color
	labelStyle := lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
	switch t.Status {
	case native.TodoCompleted:
		g, c = glyph.OK, theme.Current().StatusSuccess
		labelStyle = lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
	case native.TodoInProgress:
		g, c = glyph.Running, theme.Current().AccentPrimary
		labelStyle = labelStyle.Bold(true)
	default:
		g, c = glyph.Ambient, theme.Current().FGMuted
	}
	return gutterPrefix(g, c) + labelStyle.Render(ansi.Truncate(t.Content, max(width-3, 1), "…"))
}

// todoProgress returns the completed count and the index of the
// in-progress item (-1 when there is none).
func todoProgress(todos []native.TodoItem) (done, inProgress int) {
	inProgress = -1
	for i, t := range todos {
		switch t.Status {
		case native.TodoCompleted:
			done++
		case native.TodoInProgress:
			if inProgress < 0 {
				inProgress = i
			}
		}
	}
	return done, inProgress
}

func todosAllDone(todos []native.TodoItem) bool {
	if len(todos) == 0 {
		return false
	}
	done, _ := todoProgress(todos)
	return done == len(todos)
}

// viewedTodos returns the todo list of the session the transcript is
// currently showing: the drilled-in subagent's list while drilling, the
// parent's otherwise. Mirrors the transcriptState pattern used for the
// active-tool row.
func (m Model) viewedTodos() []native.TodoItem {
	if len(m.viewStack) > 0 {
		if child := m.viewStack[len(m.viewStack)-1].Child; child != nil {
			return child.Todos()
		}
	}
	return m.state.Todos()
}

// tasksDoc builds the Ctrl+T Tasks panel: one row per todo, using the same
// glyphs as todoLine. docpanel supplies the `esc close` hint.
//
// Each row also carries its step count (the steps under that task's header in
// the transcript, so the two views agree), its duration once completed, or its live elapsed time while in
// progress.
func tasksDoc(todos []native.TodoItem, stepCount map[string]int, now time.Time) commands.Doc {
	done, _ := todoProgress(todos)
	rows := make([]commands.Row, 0, len(todos))
	for _, t := range todos {
		g := glyph.Ambient
		switch t.Status {
		case native.TodoCompleted:
			g = glyph.OK
		case native.TodoInProgress:
			g = glyph.Running
		}
		var detail []string
		if n := stepCount[t.ID]; t.ID != "" && n > 0 {
			detail = append(detail, pluralCount(n, "step", "steps"))
		}
		switch {
		case t.Status == native.TodoCompleted && !t.StartedAt.IsZero() && !t.CompletedAt.IsZero():
			detail = append(detail, compactDuration(t.CompletedAt.Sub(t.StartedAt)))
		case t.Status == native.TodoInProgress && !t.StartedAt.IsZero():
			detail = append(detail, compactDuration(now.Sub(t.StartedAt)))
		}
		rows = append(rows, commands.Row{Text: g + " " + t.Content, Detail: strings.Join(detail, " · ")})
	}
	return commands.Doc{Title: fmt.Sprintf("Tasks %d/%d", done, len(todos)), Rows: rows}
}

// tasksOpen reports whether the Tasks panel is the open dock panel.
func (m Model) tasksOpen() bool {
	return m.tasksPanel != nil && m.dock.Panel() == dock.Panel(m.tasksPanel)
}

// toggleTasksPanel is Ctrl+T: close the Tasks panel if it is the open dock
// panel, otherwise open it on the viewed todo list. With no todos it leaves
// a notice instead of opening an empty panel.
func (m *Model) toggleTasksPanel() {
	if m.tasksOpen() {
		m.dock.CloseNow()
		m.tasksPanel = nil
		m.refreshViewport()
		return
	}
	todos := m.viewedTodos()
	if len(todos) == 0 {
		m.state.AddMessage(session.RoleSystem, "No task list in this session.", session.ContentTypePlain)
		m.refreshViewport()
		return
	}
	m.sheetPanel = nil // opening replaces the session sheet if it was up
	m.tasksPanel = docpanel.New(tasksDoc(todos, m.taskSteps, m.now()), m.state)
	m.dock.Open(m.tasksPanel)
	m.refreshViewport()
}
