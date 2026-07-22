package tui

import (
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
)

// renderLiveStrip renders the one-row activity strip that replaced the
// swarm roster panel, the SDD progress panel, and the browser bar. Only
// one source is shown at a time, in priority order: an active swarm run,
// then an active SDD run, then an open browser session. Returns "" when
// none of them is live.
//
// Continuous progress lives here; terminal transitions (a role finishing,
// a phase changing) print `·` events into the transcript instead.
func (m Model) renderLiveStrip() string {
	spinner := m.activeSpinnerFrame(session.ActivityTool)
	if p := m.state.SwarmProgress(); p.Active {
		return liveStripLine(spinner, spinnerLabel(spinner, swarmStripText(p)), m.width)
	}
	if p := m.state.SDDProgress(); p.Active {
		return liveStripLine(spinner, spinnerLabel(spinner, sddStripText(p)), m.width)
	}
	if bi := m.state.BrowserInfo(); bi.SessionOpen {
		return liveStripLine("·", browserStripText(bi, spinner), m.width)
	}
	return ""
}

// liveStripLine renders one gutter-prefixed row clipped to the frame
// width. The gutter glyph is the spinner frame while work is running, so
// the strip reads as a live line rather than a panel.
func liveStripLine(glyph, text string, width int) string {
	return gutterPrefix(glyph, dimColor) +
		statusBusyStyle().Render(strutil.Truncate(text, max(width-3, 1), false))
}

// swarmStripText renders `swarm 3/5 · implementer — round 1/2`.
func swarmStripText(p session.SwarmProgress) string {
	done, active, detail := 0, "", ""
	for _, r := range p.Roles {
		switch r.Status {
		case session.SwarmRoleDone, session.SwarmRoleFailed:
			done++
		case session.SwarmRoleActive:
			if active == "" {
				active, detail = r.Name, r.Detail
			}
		}
	}
	text := fmt.Sprintf("swarm %d/%d", done, len(p.Roles))
	if active != "" {
		text += " · " + active
		if detail != "" {
			text += " — " + detail
		}
	}
	return text
}

// sddStripText renders `sdd task 3/7 · implement parser`.
func sddStripText(p session.SDDProgress) string {
	text := fmt.Sprintf("sdd task %d/%d", p.DoneTasks, p.TotalTasks)
	for _, t := range p.Tasks {
		if t.Phase == session.SDDPhaseActive {
			text += " · " + t.Name
			if t.Detail != "" {
				text += " — " + t.Detail
			}
			return text
		}
	}
	if p.BranchReview == session.SDDPhaseActive {
		text += " · branch review"
	}
	return text
}

// browserStripText renders `🌐 example.com/docs · Title · standalone`
// plus the tool spinner while a browser action is in flight. 🌐 stays in
// the content (it is double-width and may never occupy the gutter).
func browserStripText(bi session.BrowserInfo, spinner string) string {
	text := browserGlyphStyle().Render("🌐") + " " + urlStyle().Render(truncateURL(bi.URL, 40))
	if bi.Title != "" {
		text += dimSep(bi.Title)
	}
	if bi.Mode != "" {
		text += dimSep(bi.Mode)
	}
	if bi.Active {
		text += dimSep(spinnerLabel(spinner, bi.ToolName))
	}
	return text
}
