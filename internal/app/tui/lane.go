package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
)

// laneSeparator is the rule that opens a lane, marking where the
// transcript ends. Without it the lanes blend into the todo panel and the
// input area directly beneath them.
//
// It reuses renderTurnSeparator's construction — the one `─` rule already
// sanctioned in a codebase that otherwise forbids box-drawing chrome — so
// the two horizontal rules on screen match.
func laneSeparator(width int) string {
	bar := lipgloss.NewStyle().Foreground(dimColor).Render(glyph.Rail)
	w := max(width-1, 1)
	return bar +
		lipgloss.NewStyle().Foreground(theme.Current().BorderMuted).Render(strings.Repeat("─", w)) +
		"\n"
}

// laneItem renders a count-first, pluralized caption part, matching the
// todo panel's "tasks %d/%d" convention: "1 job" / "3 jobs".
func laneItem(n int, singular, plural string) string {
	word := plural
	if n == 1 {
		word = singular
	}
	return fmt.Sprintf("%d %s", n, word)
}

// renderLane renders a lane's chrome: a separator rule row, then a
// count-first header row, then the pre-glyphed item rows. Each rows entry
// is one full display line, pre-glyphed by the caller. Returns "" when
// there are no rows.
//
// The header and rows are built at width-1 so chromeRailWidth prefixes the
// one-cell rail without ellipsizing the rule (the invariant documented at
// agentlane.go:67-69). A trailing newline is preserved so stacked regions
// keep their separation.
func renderLane(header string, rows []string, width int) string {
	if len(rows) == 0 {
		return ""
	}
	return laneSeparator(width) +
		chromeRailWidth(header+"\n"+strings.Join(rows, "\n")+"\n", dimColor, max(width-1, 1))
}

// paintLane paints a lane's rendered content as a full-width band, the
// identical tail both renderers apply today.
func paintLane(s string, leftWidth int) string {
	return chrome.PaintBand(s, leftWidth, theme.Current().ChromeBG())
}

// laneMaxItems caps visible item rows; laneMaxRows is the hard row cap
// (separator + caption + items + optional overflow).
const (
	laneMaxItems = 4
	laneMaxRows  = 7
)

// lanePlan is the consolidated lane's rendered content, computed once so
// the renderer and laneRows cannot disagree (M-2 pattern, agentlane.go:115).
// agents are the visible running subagents in click order; jobTexts are the
// rendered job rows aligned 1:1 with the shown jobs; overflow counts the
// running agents+jobs not shown; total is the running count of both. nAgents
// and nJobs are the full running counts (including overflow) for the caption.
type lanePlan struct {
	agents   []session.SubagentView // visible, click-order
	jobTexts []string               // rendered job rows, aligned 1:1 with shown jobs
	overflow int                    // running agents+jobs not shown
	total    int
	nAgents  int // full running agent count (for the caption)
	nJobs    int // full running job count (for the caption)
}

// lanePlan computes the consolidated lane's content. Agents come first
// (they are click-drillable; jobs are ambient), then jobs fill the remaining
// shown slots from runningJobs(). When total exceeds laneMaxItems, one slot
// is surrendered to the shared overflow row.
func (m Model) lanePlan() lanePlan {
	var agents []session.SubagentView
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning && v.Child != nil {
			agents = append(agents, v)
		}
	}
	jobs := m.runningJobs()
	total := len(agents) + len(jobs)
	if total == 0 {
		return lanePlan{}
	}
	shown := min(total, laneMaxItems)
	overflow := 0
	if total > laneMaxItems {
		shown = laneMaxItems - 1
		overflow = total - shown
	}
	// Agents take priority; jobs fill the remaining shown slots.
	nAgents := min(len(agents), shown)
	visibleAgents := agents[:nAgents]
	remaining := shown - nAgents

	width := max(m.leftWidth, 1)
	var jobTexts []string
	for i := 0; i < remaining && i < len(jobs); i++ {
		j := jobs[i]
		line := fmt.Sprintf("%s  %s  %s",
			j.ID,
			strutil.Truncate(j.Command, max(width/2, 12), true),
			formatElapsed(max(time.Since(j.StartedAt), 0)))
		jobTexts = append(jobTexts, dimStyle().Render(glyph.Job+" "+line))
	}
	return lanePlan{
		agents:   visibleAgents,
		jobTexts: jobTexts,
		overflow: overflow,
		total:    total,
		nAgents:  len(agents),
		nJobs:    len(jobs),
	}
}
