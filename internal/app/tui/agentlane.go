package tui

import (
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/strutil"
)

// renderActivityLane renders the activity lane above the input: ONE separator
// rule and ONE count row, whatever is running.
//
// It used to render a row per running agent, then per job, then per watch, up to
// seven rows of chrome above the composer. Every row it occupied was subtracted
// from the transcript viewport, so a busy turn — which is exactly when a reader
// needs the transcript most — was also when they had the least of it.
//
// The count stays, because "is anything running?" is the question the band
// exists to answer at a glance. The DETAILS moved to the inspector's Agents tab,
// one click or keystroke away, and the row says so: a consolidated row that
// dropped the detail without saying where it went would be a removal wearing a
// consolidation's clothes.
//
// A job and a watch are counted here rather than listed, for the same reason.
// Both outlive the turn that spawned them, so their rows were ambient anyway —
// and a reader who wants to know WHICH job is running is asking a question the
// transcript's job-exit card answers, not this band.
func (m Model) renderActivityLane() string {
	plan := m.lanePlan()
	if plan.total == 0 {
		return ""
	}
	width := max(m.leftWidth, 1)

	// Count-first, pluralized, zero parts omitted — the todo panel's
	// convention, kept because it is what makes the row scannable.
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

	// Where the details are, and how to get them. The wording is deliberately
	// about the AFFORDANCE rather than about the inspector's internals: a
	// reader does not need to know the tab is called "Agents" to act on it, but
	// they do need to know the count is also a handle.
	//
	// Drop this segment first when the row is narrow — the count is what the
	// band is for. renderLane clips the tail, so the hint is appended rather
	// than fitted here.
	hint := dimSeparator + laneDetailsHint()

	// Truncate to width-1 so chromeRailWidth's one-cell rail prefix never
	// ellipsizes the tail (agentlane.go:67-69).
	body := dimStyle().Render(strutil.Truncate(caption+hint, max(width-1, 1), true))
	if plan.nAgents == 0 {
		// Nothing to inspect: jobs and watches have no inspector tab, so
		// offering one would be a dead end.
		body = dimStyle().Render(strutil.Truncate(caption, max(width-1, 1), true))
	}

	rows := []string{gutterPrefix(m.laneGlyph(plan), dimColor) + body}
	out := renderLane("", rows, width)
	return paintLane(out, m.leftWidth)
}

// laneDetailsHint names what opens the details.
//
// It is a function rather than a constant so the key and the surface are
// described in ONE place: the row has to agree with the click handler and with
// the Agents tab, and two literals would eventually describe different things.
func laneDetailsHint() string {
	return dimStyle().Render("click or ⏎ inspect agents")
}

// laneGlyph is the row's leading glyph: the live spinner while anything runs,
// so the band reads as activity rather than as a static label.
func (m Model) laneGlyph(plan lanePlan) string {
	if plan.total == 0 {
		return ""
	}
	return m.activeSpinnerFrame(session.ActivityTool)
}

// agentLaneEntries returns the running subagents the lane counts, in the
// inspector's order. There are no rows to click per child any more, so this is
// now only the count's source — but it stays a named accessor so the renderer
// and the count cannot disagree about WHICH children are running.
func (m Model) agentLaneEntries() []session.SubagentView {
	return m.lanePlan().agents
}

// laneRows reports the lane's rendered height for the frame's height budget. It
// must agree with renderActivityLane exactly; a mismatch pushes the input area
// or the status footer off the bottom of the screen.
//
// It is a constant two — the separator and the count — whenever anything runs,
// which is the whole point of the consolidation and also what makes the budget
// trivially correct.
func (m Model) laneRows() int {
	if m.lanePlan().total == 0 {
		return 0
	}
	return laneActivityRows
}

// openAgentLaneInspector opens the inspector on the Agents tab, which is where
// the lane's details live.
//
// It reports false when there is nothing to show, so a caller can fall back
// rather than opening an empty panel. The tab is opened through the host's own
// `open`, so the lane obeys the same placement rules as `/inspect agents` — a
// wide frame gets the side rail, a narrow one the dock — instead of duplicating
// that decision.
func (m *Model) openAgentLaneInspector() bool {
	if len(m.lanePlan().agents) == 0 {
		return false
	}
	if m.inspector == nil {
		return false
	}
	return m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
}
