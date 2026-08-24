package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/strutil"
)

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
	var running []session.SubagentView
	for _, v := range m.state.Subagents() {
		if v.Status == session.SubagentRunning && v.Child != nil {
			running = append(running, v)
		}
	}
	if len(running) == 0 {
		return ""
	}
	width := max(m.leftWidth, 1)
	gutter := gutterPrefix(glyph.Running, dimColor)

	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(dimStyle().Render(fmt.Sprintf("agents %d", len(running))))
	b.WriteString("\n")

	rows := agentLaneMaxRows - 1
	shown := running
	overflow := 0
	if len(shown) > rows {
		shown = shown[:rows-1]
		overflow = len(running) - len(shown)
	}
	for _, v := range shown {
		line := fmt.Sprintf("%d  %s  %s",
			v.ID,
			strutil.Truncate(v.Label, max(width/2, 12), true),
			formatElapsed(max(time.Since(v.StartedAt), 0)))
		b.WriteString(gutter)
		b.WriteString(dimStyle().Render(ansi.Truncate(line, max(width-gutterWidth, 1), "…")))
		b.WriteString("\n")
	}
	if overflow > 0 {
		b.WriteString(gutter)
		b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more", overflow)))
		b.WriteString("\n")
	}
	return b.String()
}

// agentLaneRows reports the lane's rendered height for the frame's height
// budget. It must agree with renderAgentLane exactly; a mismatch pushes the
// input area or the status footer off the bottom of the screen.
//
// M-2: this previously called renderAgentLane (computing the full render
// twice per frame — once here for the height budget, once in View for the
// actual output). Now it computes the row count directly from the running
// count, matching renderAgentLane's layout: 1 header row + min(running,
// agentLaneMaxRows-1) rows, with an overflow row when running exceeds the
// cap.
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
		// header + (rows-1) shown + overflow row
		return 1 + (rows - 1) + 1
	}
	// header + running rows
	return 1 + running
}
