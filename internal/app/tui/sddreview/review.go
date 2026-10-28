// Package sddreview renders the candidate plan review dock panel shown after
// /sdd new finishes authoring. It lets the user accept a clean candidate
// (which leaves the artifact in place for a later /sdd), keep the candidate
// for editing, or deliberately discard it after a confirmation. It never
// starts a pipeline runner itself.
package sddreview

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
	"marshal/internal/sddauthor"
	"marshal/internal/strutil"
)

// diagError is the error severity's wire value. It is an untyped constant so
// it compares against pipeline.DiagSeverity without the TUI importing the
// agent-side pipeline package (see boundary_test.go).
const diagError = "error"

// AcceptMsg is emitted when the user accepts the reviewed candidate. Panel
// names the review that emitted it so a delayed acceptance cannot close a
// replacement review.
type AcceptMsg struct{ Panel *Panel }

// KeepMsg asks the host to close this exact review without deleting its file.
type KeepMsg struct{ Panel *Panel }

// DiscardMsg asks the host to delete the candidate of this exact review.
type DiscardMsg struct{ Panel *Panel }

// minReviewInner is the narrowest panel interior that can still show the
// candidate identity and the discard action on one row each. Below it the
// render withholds the arming state rather than claim it showed a path the
// user could not read.
const minReviewInner = 20

// Panel is a dock.Panel that reviews one authored plan candidate.
type Panel struct {
	result sddauthor.Result

	// discardArmed reports that `d` has been pressed once. A second
	// consecutive `d` confirms the discard; any other key disarms it.
	discardArmed bool
	// visible reports that the current geometry rendered the candidate
	// identity, the discard action and the confirmation state. A positive
	// discard action is refused until they have been seen at this geometry,
	// so a hidden or undersized panel can neither arm nor confirm.
	visible bool
	// err is a failed discard, shown in the panel until the next attempt.
	err error
}

var _ dock.Panel = (*Panel)(nil)

// New returns a review panel for the given authoring result.
func New(result sddauthor.Result) *Panel {
	return &Panel{result: result}
}

// CandidatePath returns the authored plan artifact path, for discard.
func (p *Panel) CandidatePath() string {
	if path := p.result.Request.PlanPath; path != "" {
		return path
	}
	// A result built without a request still names its candidate through the
	// inspection, so keeping it for editing stays actionable.
	if insp := p.result.Inspection; insp != nil && insp.Plan != nil {
		return insp.Plan.Path
	}
	return ""
}

// Sizing keeps the review panel docked under the default height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// SetError displays a failed discard in the active panel and disarms discard.
// The candidate is still on disk, so the panel stays open for a retry or a
// keep rather than closing on an outcome that never happened.
func (p *Panel) SetError(err error) {
	p.err = err
	p.discardArmed = false
}

// InvalidateVisibility disarms discard when the geometry changes. A resize
// must not carry a destructive confirmation into a frame the user has not
// seen, and the next render re-earns the permission.
func (p *Panel) InvalidateVisibility() {
	p.visible = false
	p.discardArmed = false
}

// hasErrors reports whether the candidate inspection has error diagnostics.
func (p *Panel) hasErrors() bool {
	return p.result.Inspection == nil || p.result.Inspection.HasErrors()
}

// Update handles Enter (accept when clean), Esc (keep, or disarm a pending
// discard) and the two-press `d` discard confirmation.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	key := k.String()
	// wasArmed is read before the generic disarm below, so Esc can tell
	// "disarm the pending discard and stay" from "keep the candidate and go".
	wasArmed := p.discardArmed
	if key != "d" {
		p.discardArmed = false
	}
	switch key {
	case "enter":
		if p.hasErrors() {
			// A blocked candidate may be kept (Esc) and edited, but never
			// accepted and never run.
			return nil
		}
		return func() tea.Msg { return AcceptMsg{Panel: p} }
	case "esc":
		if wasArmed {
			// Esc while armed only disarms. A second Esc then keeps the file.
			return nil
		}
		return func() tea.Msg { return KeepMsg{Panel: p} }
	case "d":
		if !p.visible {
			// The frame this key was pressed in never showed which candidate
			// `d` would delete.
			return nil
		}
		if p.discardArmed {
			p.discardArmed = false
			p.err = nil
			return func() tea.Msg { return DiscardMsg{Panel: p} }
		}
		// A fresh attempt clears the previous failure rather than leaving a
		// stale error beside a new confirmation.
		p.err = nil
		p.discardArmed = true
		return nil
	}
	return nil
}

// View renders the candidate path, task and operation counts, strategy, every
// diagnostic and a persistent action row, within the dock height budget.
func (p *Panel) View(width, maxHeight int) string {
	th := theme.Current()
	pw := layout.PanelWidth(width)
	// The frame's border columns and gutters take four cells; the panel width
	// reserves two of them, so the body keeps pw-2.
	inner := max(pw-2, 1)

	// The header row, one body row and the action row are the minimum that
	// can still show what `d` would delete.
	if maxHeight < 3 || inner < minReviewInner {
		p.visible = false
		return theme.MutedStyle().Render("Review plan")
	}

	path := p.CandidatePath()
	lines := p.bodyRows(th)

	// The action row is persistent: it names the discard action and, while
	// armed, the candidate it would delete. Reserving it keeps the keys
	// reachable when the diagnostics overflow the body.
	action := p.actionRow(path)
	bodyH := maxHeight - 2 // the chrome header and the action row
	if bodyH < 1 {
		p.visible = false
		return theme.MutedStyle().Render("Review plan")
	}

	content := chrome.ClipLines(lines, 0, bodyH, th) + "\n" + action
	ph := min(lipgloss.Height(content)+1, maxHeight)
	// This render shows the path, the discard action and the arming state, so
	// the next `d` may act on it.
	p.visible = true
	return chrome.PanelWithHints("Review plan", p.hints(), content, pw, ph, true, th)
}

// bodyRows builds the review's logical body rows: the candidate identity,
// the compile summary, the strategy and one row per diagnostic.
func (p *Panel) bodyRows(th theme.Theme) []string {
	lines := []string{theme.MutedStyle().Render("candidate: " + strutil.ShowControls(p.CandidatePath()))}

	insp := p.result.Inspection
	if insp == nil {
		// A nil inspection is blocked, so Enter cannot accept it, but the
		// candidate can still be kept and edited.
		return append(lines, errorStyle().Render("  no inspection available for this candidate"))
	}

	tasks := 0
	if insp.IR != nil {
		tasks = len(insp.IR.Tasks)
	}
	lines = append(lines,
		theme.MutedStyle().Render(fmt.Sprintf(
			"%d task(s) · %d deterministic ops · %d fallback ops · est. %d model calls",
			tasks, insp.Report.Total.DetOps, insp.Report.Total.AgentOps, insp.Report.Total.EstCalls)),
		theme.MutedStyle().Render("strategy: "+string(insp.EffectiveStrategy)),
	)

	if len(insp.Diagnostics) > 0 {
		lines = append(lines, "")
		for _, d := range insp.Diagnostics {
			row := fmt.Sprintf("  task %d: %s", d.TaskN, strutil.ShowControls(d.Message))
			if d.Severity == diagError {
				lines = append(lines, errorStyle().Render(row))
			} else {
				lines = append(lines, warnStyle().Render(row))
			}
		}
	}
	return lines
}

// actionRow renders the persistent footer: the discard confirmation while
// armed, the failure that kept the panel open, or the two available actions.
func (p *Panel) actionRow(path string) string {
	switch {
	case p.err != nil:
		return errorStyle().Render("discard failed: " + strutil.ShowControls(p.err.Error()) + " · esc keep")
	case p.discardArmed:
		return warnStyle().Render("d again to discard " + strutil.ShowControls(path) + " · esc keep")
	default:
		return theme.MutedStyle().Render("esc keep · d discard")
	}
}

// hints is the top-border key hint. It stays actionable at any width, where
// the border label may be dropped and the action row carries the keys.
func (p *Panel) hints() string {
	if p.discardArmed {
		return "d again to discard"
	}
	return "esc keep · d discard"
}

func errorStyle() lipgloss.Style {
	if theme.IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError)
}

func warnStyle() lipgloss.Style {
	if theme.IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning)
}
