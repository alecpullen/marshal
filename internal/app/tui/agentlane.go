package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/sidepanel"
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

// agentLaneMaxRows caps the lane: a header row plus up to three subagent
// rows, the last of which becomes an overflow row when more are running.
// Every row here is subtracted from the transcript viewport, so the lane
// must not grow without bound.
const agentLaneMaxRows = 4

// renderAgentLane renders running background subagents as a persistent lane
// above the input so a running child stays visible after its transcript
// card scrolls away. Follows renderJobLane (jobs.go): no tint — position
// already separates it. Only views with a live Child state are listed:
// pipeline/SDD cards share the parent's state (Child == nil) and are
// already pinned by the run panel.
func (m Model) renderAgentLane() string {
	entries := m.agentLaneEntries()
	if len(entries) == 0 {
		return ""
	}
	// Count all running for the header (includes overflow)
	var allRunning int
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning && v.Child != nil {
			allRunning++
		}
	}
	width := max(m.leftWidth, 1)
	spinner := m.activeSpinnerFrame(session.ActivityTool)

	var b strings.Builder
	// Header: count-first, pluralized ("2 agents" / "1 agent"), with the
	// divider rule on the same line — matching the sidebar and todo panel
	// via sidepanel.Header.
	plural := "agents"
	if allRunning == 1 {
		plural = "agent"
	}
	b.WriteString(sidepanel.Header(fmt.Sprintf("%d %s", allRunning, plural), "", width))
	b.WriteString("\n")

	overflow := 0
	if allRunning > len(entries) {
		overflow = allRunning - len(entries)
	}
	for _, v := range entries {
		b.WriteString(dimStyle().Render(spinnerLabel(spinner, fmt.Sprintf("#%d  %s  %s",
			v.ID,
			strutil.Truncate(v.Label, max(width/2, 12), true),
			formatElapsed(max(time.Since(v.StartedAt), 0))))))
		b.WriteString("\n")
	}
	if overflow > 0 {
		b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more", overflow)))
		b.WriteString("\n")
	}

	out := chromeRailWidth(b.String(), dimColor, max(width-1, 1))
	return chrome.PaintBand(out, m.leftWidth, theme.Current().ChromeBG())
}

// agentLaneEntries returns the running subagents in the order the lane
// renders them, capped the same way. The click handler and the renderer
// must agree on this ordering or a click drills into the wrong child.
func (m Model) agentLaneEntries() []session.SubagentView {
	var running []session.SubagentView
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning && v.Child != nil {
			running = append(running, v)
		}
	}
	if rows := agentLaneMaxRows - 1; len(running) > rows {
		running = running[:rows-1]
	}
	return running
}

// agentLaneRows reports the lane's rendered height for the frame's height
// budget. It must agree with renderAgentLane exactly; a mismatch pushes the
// input area or the status footer off the bottom of the screen.
//
// M-2: this previously called renderAgentLane (computing the full render
// twice per frame — once here for the height budget, once in View for the
// actual output). Now it computes the row count directly from the running
// count, matching renderAgentLane's layout: 1 merged header+rule row +
// up to (agentLaneMaxRows-2) agent rows, with an overflow row when
// running exceeds the cap.
func (m Model) agentLaneRows() int {
	var running int
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning && v.Child != nil {
			running++
		}
	}
	if running == 0 {
		return 0
	}
	rows := agentLaneMaxRows - 1
	if running > rows {
		// merged header+rule + (rows-1) shown + overflow
		return 1 + (rows - 1) + 1
	}
	// merged header+rule + running rows
	return 1 + running
}
