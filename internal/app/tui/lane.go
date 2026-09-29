package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
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

// renderLane renders a lane's chrome: a separator rule row, then the
// pre-glyphed content rows. Each rows entry is one full display line,
// pre-glyphed by the caller. Returns "" when there are no rows.
//
// header is an OPTIONAL label emitted above the rows. An empty header adds no
// row: the caller building "header\nrows" with an empty header would produce a
// leading blank line, and every row the lane occupies is subtracted from the
// transcript viewport — so a blank one is a line of the conversation nobody can
// read. That is why an empty header is skipped rather than formatted.
//
// The content is built at width-1 so chromeRailWidth prefixes the one-cell rail
// without ellipsizing the rule (the invariant documented at agentlane.go). A
// trailing newline is preserved so stacked regions keep their separation.
func renderLane(header string, rows []string, width int) string {
	if len(rows) == 0 {
		return ""
	}
	body := strings.Join(rows, "\n")
	if header != "" {
		body = header + "\n" + body
	}
	return laneSeparator(width) +
		chromeRailWidth(body+"\n", dimColor, max(width-1, 1))
}

// paintLane paints a lane's rendered content as a full-width band, the
// identical tail both renderers apply today.
func paintLane(s string, leftWidth int) string {
	return chrome.PaintBand(s, leftWidth, theme.Current().ChromeBG())
}

// laneActivityRows is the lane's rendered height when it shows: the separator
// rule plus the count row.
//
// There is no per-kind cap any more. The lane used to cap each kind's rows and
// surrender a slot to an "… N more" row; with one count row, the cap and the
// overflow row are gone, and the height budget is the constant above — which is
// what makes it impossible for the count and the renderer to disagree.
const laneActivityRows = 2

// lanePlan is the consolidated lane's content, computed ONCE so the renderer and
// laneRows cannot disagree (agentlane.go).
//
// It carries counts and the running agents only: the per-job and per-watch rows
// the lane used to render are gone, because they are counted rather than listed.
// agents remains a slice rather than a count because the renderer decides
// whether to offer the inspector — which is only reachable when there is at least
// one child to show — and because the ORDER has to be the same one the inspector
// lists, so a reader who clicks does not land on a different child from the one
// they read.
type lanePlan struct {
	agents   []session.SubagentView // running children, in the order the inspector lists them
	total    int                    // running agents + jobs + watches
	nAgents  int                    // running children, for the caption
	nJobs    int                    // running jobs, for the caption
	nWatches int                    // watches, for the caption
}

// lanePlan computes the consolidated lane's content.
//
// It counts rather than lists. A running child is the one thing kept as a slice,
// because the renderer decides from it whether to offer the inspector and the
// ORDER has to match what the inspector shows — a reader who reads "2 agents" and
// clicks expects to see those two, in that order.
//
// Only views with a live Child state count: pipeline/SDD cards share the parent's
// state (Child == nil) and are already pinned by the run panel, so counting them
// here would double-report the same work in two bands.
func (m Model) lanePlan() lanePlan {
	var agents []session.SubagentView
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning && v.Child != nil {
			agents = append(agents, v)
		}
	}
	jobs := m.runningJobs()
	watches := m.runningWatches()
	total := len(agents) + len(jobs) + len(watches)
	if total == 0 {
		return lanePlan{}
	}
	return lanePlan{
		agents:   agents,
		total:    total,
		nAgents:  len(agents),
		nJobs:    len(jobs),
		nWatches: len(watches),
	}
}
