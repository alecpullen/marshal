package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
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
func renderRunEvent(ev *acpwire.WireRunEvent, d *acpwire.NodeDetail, expanded bool, width int) string {
	g, c, headline := runEventHeadline(ev)
	cw := contentWidth(width)

	var b strings.Builder
	b.WriteString(gutterPrefix(g, c))
	b.WriteString(lipgloss.NewStyle().Foreground(c).Render(ansi.Truncate(headline, max(cw, 1), "…")))
	b.WriteString("\n")

	// The wire caps its body; the detail holds all of it. Which body this is
	// determines what "… N more lines" counts against.
	body := ev.Body
	if d != nil && d.Body != "" {
		body = d.Body
	}
	if body == "" {
		return b.String()
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	hidden := 0
	if !expanded && len(lines) > runEventCollapsedBodyLines {
		// BodyLines counts the whole body, which the wire may have cut, so
		// the hidden count comes from it rather than from the lines in hand.
		hidden = ev.BodyLines - runEventCollapsedBodyLines
		lines = lines[:runEventCollapsedBodyLines]
	}
	for _, line := range lines {
		b.WriteString(continuation())
		b.WriteString(theme.MutedStyle().Render(ansi.Truncate(line, max(cw, 1), "…")))
		b.WriteString("\n")
	}
	if hidden > 0 {
		b.WriteString(continuation())
		b.WriteString(theme.MutedStyle().Render(fmt.Sprintf("… %d more lines — ctrl+g to expand", hidden)))
		b.WriteString("\n")
	}
	return b.String()
}

// runEventHeadline returns the glyph, color, and one-line summary for an
// event kind.
func runEventHeadline(ev *acpwire.WireRunEvent) (string, color.Color, string) {
	th := theme.Current()
	task := ""
	if ev.TaskN > 0 {
		task = fmt.Sprintf("task %d · ", ev.TaskN)
	}
	switch ev.Kind {
	case acpwire.RunEventVerifyFailed:
		text := fmt.Sprintf("%s%s failed", task, ev.Title)
		if ev.Detail != "" {
			text += " — " + ev.Detail
		}
		return glyph.Error, th.StatusError, text

	case acpwire.RunEventGateSkipped:
		return glyph.Warning, th.StatusWarning,
			fmt.Sprintf("%sverification skipped — %s", task, ev.Title)

	case acpwire.RunEventReview:
		if ev.Detail == "clean" {
			return glyph.OK, th.StatusSuccess, task + "review clean"
		}
		return glyph.Warning, th.StatusWarning,
			fmt.Sprintf("%sreview · %s · %s", task, ev.Severity, ev.Title)

	case acpwire.RunEventCommit:
		return glyph.Brand, th.FGMuted,
			fmt.Sprintf("%s%s  %s", task, ev.Title, ev.Detail)

	case acpwire.RunEventRetry:
		text := fmt.Sprintf("%s%s", task, ev.Title)
		if ev.Detail != "" {
			text += " — " + ev.Detail
		}
		return glyph.Warning, th.StatusWarning, text

	case acpwire.RunEventConcern:
		return glyph.Warning, th.StatusWarning,
			fmt.Sprintf("%sconcern — %s", task, ev.Title)

	case acpwire.RunEventTaskDone:
		text := fmt.Sprintf("%sdone", task)
		if ev.Detail != "" {
			text += " — " + ev.Detail
		}
		return glyph.OK, th.StatusSuccess, text
	}
	return glyph.Ambient, th.FGMuted, ev.Title
}
