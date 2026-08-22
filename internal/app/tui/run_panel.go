package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// renderRunPanel renders the run's orientation row: where the run is and
// roughly how much is left, in exactly one line.
//
// It deliberately does not show the plan checklist. Every row this panel
// occupies is subtracted from the transcript viewport (model.go), and the
// transcript is where a run's actual content now lives — verify output,
// review findings, commits. /run shows the checklist on demand instead, at
// no cost to the transcript.
//
// After the run ends it collapses to a one-line summary until the next user
// turn clears it (keypress.go).
func renderRunPanel(p session.SDDProgress, spinner string, now time.Time, width int) string {
	if !p.Active && !p.Finished {
		return ""
	}
	width = max(width, 1)
	if p.Finished {
		return runPanelBar(runPanelFinishedLine(p, width), width)
	}
	return runPanelBar(runPanelSummaryLine(p, spinner, now, width), width)
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

// runSeg is one segment of the summary line. Higher priority drops first;
// priority 0 is never dropped. glue joins the segment to its predecessor
// with a space rather than the separator, so a phase and its elapsed time
// read as one phrase while remaining independently droppable.
type runSeg struct {
	text     string
	priority int
	glue     bool
}

// joinRunSegs renders segments left to right, gluing where asked.
func joinRunSegs(segs []runSeg) string {
	var b strings.Builder
	for i, s := range segs {
		switch {
		case i == 0:
		case s.glue:
			b.WriteString(" ")
		default:
			b.WriteString(dimSeparator)
		}
		b.WriteString(s.text)
	}
	return b.String()
}

// runPanelSummaryLine renders `⠋ task 4/7 · 43% · verifying 2m 14s ·
// ~6–25m left`, dropping whole segments as width shrinks rather than
// truncating mid-word. The drop order is documented in the run-panel layout
// spec; the task counter is the floor and always survives.
func runPanelSummaryLine(p session.SDDProgress, spinner string, now time.Time, width int) string {
	segs := []runSeg{{text: fmt.Sprintf("task %d/%d", p.CurrentTask, p.TotalTasks), priority: 0}}

	// Percent counts completed tasks only: the in-flight task is not done,
	// and counting it would overstate progress on every render. Rounded,
	// not truncated — integer division renders 3/7 as 42%, which is wrong
	// by a percentage point on most fractions.
	if p.TotalTasks > 0 {
		segs = append(segs, runSeg{text: formatPercent(p.DoneTasks, p.TotalTasks), priority: 6})
	}
	if p.Phase != "" {
		segs = append(segs, runSeg{text: p.Phase, priority: 1})
		if !p.PhaseStartedAt.IsZero() {
			if d := now.Sub(p.PhaseStartedAt); d > 0 {
				segs = append(segs, runSeg{text: formatElapsed(d), priority: 2, glue: true})
			}
		}
	}
	if p.FixRound > 0 {
		segs = append(segs, runSeg{
			text:     fmt.Sprintf("fix %d/%d", p.FixRound, p.MaxFixRounds),
			priority: 3,
		})
	}
	if low, high, openEnded, ok := estimateRemaining(p, now); ok {
		segs = append(segs, runSeg{text: formatETA(low, high, openEnded), priority: 4})
	}
	if !p.StartedAt.IsZero() {
		if d := now.Sub(p.StartedAt); d > 0 {
			segs = append(segs, runSeg{text: formatElapsed(d), priority: 5})
		}
	}
	if p.Detail != "" {
		segs = append(segs, runSeg{text: p.Detail, priority: 7})
	}

	// Drop the lowest-priority segment until the line fits, always keeping
	// segs[0]. Mirrors the status line's loop (status.go).
	budget := max(width-3, 1)
	text := joinRunSegs(segs)
	for len(segs) > 1 && ansi.StringWidth(text) > budget {
		worst := 1
		for i := 2; i < len(segs); i++ {
			if segs[i].priority > segs[worst].priority {
				worst = i
			}
		}
		segs = append(segs[:worst], segs[worst+1:]...)
		text = joinRunSegs(segs)
	}

	g := spinner
	if g == "" {
		g = glyph.Running
	}
	return gutterPrefix(g, accentColor) +
		statusBusyStyle().Render(ansi.Truncate(text, budget, "…"))
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
	return gutterPrefix(g, c) + theme.MutedStyle().Render(ansi.Truncate(label, budget, "…"))
}
