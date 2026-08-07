package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/native"
)

// runPanelMaxRows caps the checklist rows inside the run panel. The summary
// and gate rows are not counted against it.
const runPanelMaxRows = 6

// renderRunPanel renders the single SDD progress surface as a full-width
// horizontal top bar: one summary line carrying the only spinner on screen
// during a run, the plan checklist windowed around the current task. After
// the run ends it collapses to a one-line summary until the next user turn
// clears it (keypress.go). The bar has no background per the unified
// chrome design. The human-gate question no longer lives here — the docked
// gate panel owns it (one surface, not two).
func renderRunPanel(p session.SDDProgress, spinner string, now time.Time, frameHeight, width int) string {
	if !p.Active && !p.Finished {
		return ""
	}
	width = max(width, 1)
	if p.Finished {
		return runPanelBar(runPanelFinishedLine(p, width), width)
	}
	sections := []string{runPanelSummaryLine(p, spinner, now, width)}
	if checklist := runPanelChecklist(p, frameHeight, width); checklist != "" {
		sections = append(sections, checklist)
	}
	return runPanelBar(strings.Join(sections, "\n"), width)
}

// runPanelBar renders content as a full-width horizontal bar with no
// background, replacing the old vertical chrome rail. It keeps the width
// layout semantics so the text spans the full frame.
func runPanelBar(content string, width int) string {
	if width < 1 {
		width = 1
	}
	return lipgloss.NewStyle().
		Width(width).
		MaxWidth(width).
		Render(content)
}

// runPanelSummaryLine renders `⠋ task 3/7 · implementing · fix 1/3 ·
// src/auth.go · 4m 12s`, omitting empty segments. The gutter glyph is the
// shared spinner frame; while the frame is gated (first 200ms of a turn)
// it falls back to the static Running glyph so the row never shifts.
func runPanelSummaryLine(p session.SDDProgress, spinner string, now time.Time, width int) string {
	text := fmt.Sprintf("task %d/%d", p.CurrentTask, p.TotalTasks)
	if p.Phase != "" {
		text += " · " + p.Phase
		// Per-phase elapsed distinguishes a slow test suite from a hang;
		// whole-run elapsed alone cannot.
		if !p.PhaseStartedAt.IsZero() {
			if d := now.Sub(p.PhaseStartedAt); d > 0 {
				text += " " + formatElapsed(d)
			}
		}
	}
	if p.FixRound > 0 {
		text += fmt.Sprintf(" · fix %d/%d", p.FixRound, p.MaxFixRounds)
	}
	if p.Detail != "" {
		text += " · " + p.Detail
	}
	if !p.StartedAt.IsZero() {
		elapsed := now.Sub(p.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		text += " · " + formatElapsed(elapsed)
	}
	g := spinner
	if g == "" {
		g = glyph.Running
	}
	return gutterPrefix(g, accentColor) +
		statusBusyStyle().Render(ansi.Truncate(text, max(width-3, 1), "…"))
}

// runPanelChecklist renders the plan tasks with todo-style status glyphs,
// windowed around the current task by chrome.ClipLines (which adds the
// ↑ more / ↓ more marker rows when it clips).
func runPanelChecklist(p session.SDDProgress, frameHeight, width int) string {
	if len(p.Tasks) == 0 {
		return ""
	}
	lines := make([]string, 0, len(p.Tasks))
	for i, title := range p.Tasks {
		lines = append(lines, todoLine(runPanelTodo(i, title, p), width))
	}
	focus := min(max(p.CurrentTask-1, 0), len(lines)-1)
	budget := min(len(lines), min(runPanelMaxRows, max(frameHeight/4, 2)))
	return chrome.ClipLines(lines, focus, budget, theme.Current())
}

// runPanelTodo maps a plan task's position in the run to a todo item so the
// checklist reuses the todo panel's row rendering: done before the current
// task, in-progress at it, pending after.
func runPanelTodo(i int, title string, p session.SDDProgress) native.TodoItem {
	status := native.TodoPending
	switch {
	case i < p.DoneTasks:
		status = native.TodoCompleted
	case i == p.CurrentTask-1:
		status = native.TodoInProgress
	}
	return native.TodoItem{Content: fmt.Sprintf("%d %s", i+1, title), Status: status}
}

// runPanelFinishedLine renders the collapsed post-run summary:
// `✓ sdd done — 7/7 tasks · 23m 41s` or
// `✗ sdd stopped — 3/7 tasks · 12s · <reason> — /sdd to resume`.
// When the panel is narrow the resume hint is dropped first, then the
// reason.
func runPanelFinishedLine(p session.SDDProgress, width int) string {
	elapsed := p.EndedAt.Sub(p.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	var g, verb string
	var c color.Color
	if p.Succeeded {
		g, c, verb = glyph.OK, theme.Current().StatusSuccess, "sdd done"
	} else {
		g, c, verb = glyph.Error, theme.Current().StatusError, "sdd stopped"
	}
	base := fmt.Sprintf("%s — %d/%d tasks · %s", verb, p.DoneTasks, p.TotalTasks, formatElapsed(elapsed))
	var reason, hint string
	if !p.Succeeded && p.Error != "" {
		reason = " · " + p.Error
	}
	// Both outcomes get a route to the detail: a successful run's work is on
	// a branch whose worktree has been removed, and a failed run's real
	// explanation is in the ledger and the per-task reports.
	hint = " — /run for details"
	if !p.Succeeded {
		hint = " — /run for details · /sdd to resume"
	}
	budget := max(width-3, 1)
	label := base + reason + hint
	if ansi.StringWidth(label) > budget && hint != "" {
		label = base + reason
	}
	if ansi.StringWidth(label) > budget && reason != "" {
		label = base
	}
	return gutterPrefix(g, c) + mutedStyle().Render(ansi.Truncate(label, budget, "…"))
}
