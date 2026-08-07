package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// runEventCollapsedBodyLines caps how much of a run event's body (a verify
// failure's compiler output) shows before the user expands it. The full
// text is always retained — this is a display cap, not a truncation of the
// stored event.
const runEventCollapsedBodyLines = 12

// renderRunEvent renders one plan-run event as transcript rows: a
// gutter-prefixed headline plus, for events that carry one, an indented
// body block. The body is capped until expanded (ctrl+g or a per-item
// override), matching how tool results and subagent summaries behave.
//
// This is the surface that makes a run legible: without it the only
// evidence of a failed gate or a rejected review is a transient one-word
// phase label in the run panel.
func renderRunEvent(ev session.RunEvent, expanded bool, width int) string {
	g, c, headline := runEventHeadline(ev)
	cw := contentWidth(width)

	var b strings.Builder
	b.WriteString(gutterPrefix(g, c))
	b.WriteString(lipgloss.NewStyle().Foreground(c).Render(ansi.Truncate(headline, max(cw, 1), "…")))
	b.WriteString("\n")

	if ev.Body == "" {
		return b.String()
	}
	lines := strings.Split(strings.TrimRight(ev.Body, "\n"), "\n")
	hidden := 0
	if !expanded && len(lines) > runEventCollapsedBodyLines {
		hidden = len(lines) - runEventCollapsedBodyLines
		lines = lines[:runEventCollapsedBodyLines]
	}
	for _, line := range lines {
		b.WriteString(continuation())
		b.WriteString(mutedStyle().Render(ansi.Truncate(line, max(cw, 1), "…")))
		b.WriteString("\n")
	}
	if hidden > 0 {
		b.WriteString(continuation())
		b.WriteString(mutedStyle().Render(fmt.Sprintf("… %d more lines — ctrl+g to expand", hidden)))
		b.WriteString("\n")
	}
	return b.String()
}

// runEventHeadline returns the glyph, color, and one-line summary for an
// event kind.
func runEventHeadline(ev session.RunEvent) (string, color.Color, string) {
	th := theme.Current()
	task := ""
	if ev.TaskN > 0 {
		task = fmt.Sprintf("task %d · ", ev.TaskN)
	}
	switch ev.Kind {
	case session.RunEventVerifyFailed:
		text := fmt.Sprintf("%s%s failed", task, ev.Title)
		if ev.Detail != "" {
			text += " — " + ev.Detail
		}
		return glyph.Error, th.StatusError, text

	case session.RunEventGateSkipped:
		return glyph.Warning, th.StatusWarning,
			fmt.Sprintf("%sverification skipped — %s", task, ev.Title)

	case session.RunEventReview:
		if ev.Detail == "clean" {
			return glyph.OK, th.StatusSuccess, task + "review clean"
		}
		return glyph.Warning, th.StatusWarning,
			fmt.Sprintf("%sreview · %s · %s", task, ev.Severity, ev.Title)

	case session.RunEventCommit:
		return glyph.Brand, th.FGMuted,
			fmt.Sprintf("%s%s  %s", task, ev.Title, ev.Detail)

	case session.RunEventRetry:
		text := fmt.Sprintf("%s%s", task, ev.Title)
		if ev.Detail != "" {
			text += " — " + ev.Detail
		}
		return glyph.Warning, th.StatusWarning, text

	case session.RunEventConcern:
		return glyph.Warning, th.StatusWarning,
			fmt.Sprintf("%sconcern — %s", task, ev.Title)

	case session.RunEventTaskDone:
		text := fmt.Sprintf("%sdone", task)
		if ev.Detail != "" {
			text += " — " + ev.Detail
		}
		return glyph.OK, th.StatusSuccess, text
	}
	return glyph.Ambient, th.FGMuted, ev.Title
}
