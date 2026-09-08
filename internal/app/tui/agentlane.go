package tui

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/strutil"
)

// renderActivityLane renders the consolidated lane above the input: a
// separator rule row, one count-first chrome.Header caption ("2 agents · 1
// job"), then agent rows followed by job rows sharing one overflow budget
// capped at laneMaxRows. Agents render before jobs (agents are
// click-drillable; jobs are ambient), and the overflow row is a single
// shared "… N more" captioning both.
//
// Each agent row shows the child's dispatched model when the dispatcher
// populated it, and the provider only when it differs from the parent's
// ActiveRoute().Provider — a same-provider child adds no information the
// turn spinner doesn't already carry. Only views with a live Child state
// are listed: pipeline/SDD cards share the parent's state (Child == nil)
// and are already pinned by the run panel.
func (m Model) renderActivityLane() string {
	plan := m.lanePlan()
	if plan.total == 0 {
		return ""
	}
	width := max(m.leftWidth, 1)
	spinner := m.activeSpinnerFrame(session.ActivityTool)

	// Caption: count-first, pluralized, zero parts omitted.
	var parts []string
	if plan.nAgents > 0 {
		parts = append(parts, laneItem(plan.nAgents, "agent", "agents"))
	}
	if plan.nJobs > 0 {
		parts = append(parts, laneItem(plan.nJobs, "job", "jobs"))
	}
	if plan.nWatches > 0 {
		parts = append(parts, laneItem(plan.nWatches, "watch", "watches"))
	}
	caption := strings.Join(parts, dimSeparator)
	// Built at width-1: chromeRailWidth below truncates every line to
	// width-1 and then prefixes the one-cell rail, so a header built at
	// the full width loses its last cell to an ellipsis.
	header := chrome.Header(caption, "", max(width-1, 1))

	rows := make([]string, 0, len(plan.agents)+len(plan.jobTexts)+len(plan.watchTexts)+1)
	for _, v := range plan.agents {
		label := fmt.Sprintf("#%d  %s", v.ID, v.Label)
		if v.Model != "" {
			label += dimSeparator + v.Model
			if v.Provider != "" && v.Provider != m.state.ActiveRoute().Provider {
				label += " @ " + v.Provider
			}
		}
		line := label + dimSeparator + formatElapsed(max(time.Since(v.StartedAt), 0))
		rows = append(rows, gutterPrefix(spinner, dimColor)+
			dimStyle().Render(strutil.Truncate(line, max(width-4, 1), true)))
	}
	rows = append(rows, plan.jobTexts...)
	rows = append(rows, plan.watchTexts...)
	if plan.overflow > 0 {
		rows = append(rows, dimStyle().Render(fmt.Sprintf("… %d more", plan.overflow)))
	}

	out := renderLane(header, rows, width)
	return paintLane(out, m.leftWidth)
}

// agentLaneEntries returns the running subagents in the order the lane
// renders them, capped the same way. The click handler and the renderer
// must agree on this ordering or a click drills into the wrong child.
func (m Model) agentLaneEntries() []session.SubagentView {
	return m.lanePlan().agents
}

// laneRows reports the lane's rendered height for the frame's height
// budget. It must agree with renderActivityLane exactly; a mismatch pushes
// the input area or the status footer off the bottom of the screen.
//
// M-2: this computes the row count directly from lanePlan (never calling
// the renderer), matching renderActivityLane's layout: 1 separator row + 1
// caption row + shown item rows + an overflow row when total exceeds the
// cap. Max 2 + (laneMaxItems-1) + 1 = 7 = laneMaxRows.
func (m Model) laneRows() int {
	plan := m.lanePlan()
	if plan.total == 0 {
		return 0
	}
	rows := 2 + len(plan.agents) + len(plan.jobTexts) + len(plan.watchTexts)
	if plan.overflow > 0 {
		rows++
	}
	return rows
}
