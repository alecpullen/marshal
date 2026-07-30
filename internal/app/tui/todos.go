package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
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
// capped at ~25% of the frame height, and degrades to the collapsed
// one-liner when fewer than two rows are available.
const todoPanelMaxRows = 10

// renderTodoPanelBody renders the pinned todo panel: gutter-styled, no
// box, no header. Returns "" when there is nothing to show.
//
// Progress tracking is the one widget the hairline-gutter redesign makes
// taller rather than flatter — it used to be prepended to the transcript,
// where follow mode scrolled it out of view exactly when it mattered.
func renderTodoPanelBody(todos []native.TodoItem, mode todoPanelMode, frameHeight, width int) string {
	if len(todos) == 0 || mode == todoPanelHidden {
		return ""
	}
	done, inProgress := todoProgress(todos)
	if done == len(todos) {
		return mutedStyle().Render(fmt.Sprintf(" ✓ %d tasks done", len(todos)))
	}
	budget := todoPanelBudget(len(todos), frameHeight)
	if mode == todoPanelCollapsed || budget < 2 {
		return todoOneLine(todos, done, inProgress, width)
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
			summary := mutedStyle().Render(fmt.Sprintf(" ✓ %d done", lead))
			lines = append([]string{summary}, lines[lead:]...)
			focus = max(focus-lead+1, 0)
		}
	}
	return chrome.ClipLines(lines, focus, budget, theme.Current())
}

// todoPanelBudget is the expanded panel's row budget: never more than the
// item count, the hard cap, or ~25% of the frame.
func todoPanelBudget(n, frameHeight int) int {
	return min(n, min(todoPanelMaxRows, max(frameHeight/4, 1)))
}

// todoOneLine renders `▶ tasks 3/7 · implement expression parsing`.
func todoOneLine(todos []native.TodoItem, done, inProgress, width int) string {
	text := fmt.Sprintf("tasks %d/%d", done, len(todos))
	if inProgress >= 0 {
		text += " · " + todos[inProgress].Content
	}
	style := lipgloss.NewStyle().Foreground(theme.Current().FGDefault).Bold(true)
	return gutterPrefix("▶", theme.Current().AccentPrimary) +
		style.Render(ansi.Truncate(text, max(width-3, 1), ""))
}

// todoLine renders one todo row: ✓ teal done, ▶ coral bold in-progress,
// · muted pending — matching native.TodoItem's statuses exactly.
func todoLine(t native.TodoItem, width int) string {
	var glyph string
	var c color.Color
	labelStyle := lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
	switch t.Status {
	case native.TodoCompleted:
		glyph, c = "✓", theme.Current().StatusSuccess
		labelStyle = lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
	case native.TodoInProgress:
		glyph, c = "▶", theme.Current().AccentPrimary
		labelStyle = labelStyle.Bold(true)
	default:
		glyph, c = "·", theme.Current().FGMuted
	}
	return gutterPrefix(glyph, c) + labelStyle.Render(ansi.Truncate(t.Content, max(width-3, 1), ""))
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
