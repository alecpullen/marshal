package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/native"
)

// todoPanelMode is the Ctrl+T visibility cycle for the pinned todo panel.
type todoPanelMode int

const (
	todoPanelExpanded todoPanelMode = iota
	todoPanelCollapsed
	todoPanelHidden
	todoPanelModeCount = 3
)

// todoPanelMaxRows caps the pinned panel. The panel is additionally
// capped at ~33% of the frame height, and degrades to the collapsed
// one-liner when fewer than two rows are available.
const todoPanelMaxRows = 10

// todoPanelMaxVisibleItems is the default number of todo item rows the
// expanded panel shows before clipping (the header row is additional).
const todoPanelMaxVisibleItems = 6

// renderTodoPanelBody renders the pinned todo panel: gutter-styled, no
// box, one counts header row. Returns "" when there is nothing to show.
// Every line wears the ▍ chrome rail (dim — the panel is ambient, not an
// input state), so it reads as one unit with the input stack below it.
//
// Progress tracking is the one widget the hairline-gutter redesign makes
// taller rather than flatter — it used to be prepended to the transcript,
// where follow mode scrolled it out of view exactly when it mattered.
func renderTodoPanelBody(todos []native.TodoItem, mode todoPanelMode, frameHeight, width int) string {
	if len(todos) == 0 || mode == todoPanelHidden {
		return ""
	}
	// Reserve one cell for the ▍ rail so inner width math stays honest.
	width = max(width-1, 1)
	done, inProgress := todoProgress(todos)
	if done == len(todos) {
		return chromeRail(theme.MutedStyle().Render(fmt.Sprintf(" "+glyph.OK+" %d tasks done", len(todos))), dimColor)
	}
	budget := todoPanelBudget(len(todos), frameHeight)
	if mode == todoPanelCollapsed || budget < 2 {
		return chromeRail(todoOneLine(todos, done, inProgress, width), dimColor)
	}

	lines := make([]string, 0, len(todos))
	for _, t := range todos {
		lines = append(lines, todoLine(t, width))
	}
	focus := inProgress
	if focus < 0 {
		focus = 0
	}
	if len(lines) > budget {
		// Collapse the leading run of completed items so the visible rows
		// skew toward remaining work.
		lead := 0
		for lead < len(todos) && todos[lead].Status == native.TodoCompleted {
			lead++
		}
		if lead > 1 {
			summary := theme.MutedStyle().Render(fmt.Sprintf(" "+glyph.OK+" %d done", lead))
			lines = append([]string{summary}, lines[lead:]...)
			focus = max(focus-lead+1, 0)
		}
	}
	header := theme.MutedStyle().Render(fmt.Sprintf(" tasks %d/%d", done, len(todos)))
	body := chrome.ClipLines(lines, focus, max(budget-1, 1), theme.Current())
	return chromeRailWidth(header+"\n"+body, dimColor, width)
}

// todoPanelBudget is the expanded panel's total row budget: one header row
// plus up to todoPanelMaxVisibleItems item rows, never more than the hard
// cap or ~33% of the frame. The header used to consume one of n's rows,
// so a three-item list rendered as header + 2 items with an "↑ more"
// indicator; the budget now always has room for the header.
func todoPanelBudget(n, frameHeight int) int {
	return min(1+min(n, todoPanelMaxVisibleItems), min(todoPanelMaxRows, max(frameHeight/3, 1)))
}

// todoOneLine renders `▸ tasks 3/7 · implement expression parsing`.
func todoOneLine(todos []native.TodoItem, done, inProgress, width int) string {
	text := fmt.Sprintf("tasks %d/%d", done, len(todos))
	if inProgress >= 0 {
		text += " · " + todos[inProgress].Content
	}
	style := lipgloss.NewStyle().Foreground(theme.Current().FGDefault).Bold(true)
	return gutterPrefix(glyph.Running, theme.Current().AccentPrimary) +
		style.Render(ansi.Truncate(text, max(width-3, 1), "…"))
}

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

// cycleTodoPanelMode advances the pinned todo panel's visibility cycle:
// expanded → collapsed → hidden. Used by Ctrl+T.
func (m *Model) cycleTodoPanelMode() {
	m.todoPanelMode = (m.todoPanelMode + 1) % todoPanelModeCount
	m.updateViewportHeight()
}

// toggleTodoPanelMode flips between expanded and collapsed without
// entering the hidden state. Used by mouse clicks so the panel never
// vanishes unexpectedly. Ctrl+T still cycles through all three states
// (expanded → collapsed → hidden) via cycleTodoPanelMode.
func (m *Model) toggleTodoPanelMode() {
	if m.todoPanelMode == todoPanelExpanded {
		m.todoPanelMode = todoPanelCollapsed
	} else {
		m.todoPanelMode = todoPanelExpanded
	}
	m.updateViewportHeight()
}

// todoSignature identifies a todo list by content and status, so the TUI
// can tell an agent rewrite from a re-render.
func todoSignature(todos []native.TodoItem) string {
	var b strings.Builder
	for _, t := range todos {
		b.WriteString(t.Content)
		b.WriteByte(0)
		b.WriteString(t.Status)
		b.WriteByte(0)
	}
	return b.String()
}
