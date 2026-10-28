// Package sddreview renders the candidate plan review dock panel shown after
// /sdd new finishes authoring. It lets the user accept a clean candidate
// (which leaves the artifact in place for a later /sdd), keep the candidate
// for editing, or deliberately discard it after a confirmation. It never
// starts a pipeline runner itself.
package sddreview

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

// diagWarning is the warning severity's wire value, declared beside diagError
// for the same boundary reason.
const diagWarning = "warning"

// reviewBreakpoints are the cells ansi.Wrap may break a body row at. The path
// separators let a long candidate path wrap on a directory boundary instead of
// mid-name; a hyphen is always a breakpoint.
const reviewBreakpoints = " /-_.,:"

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

	// scroll is the first visible physical body row. It is clamped to the
	// content by every render, so a resize or a shorter candidate can never
	// leave the window past the end of what there is to read.
	scroll int
	// bodyH is how many physical body rows the last render could show. PgUp
	// and PgDn move by one body page of exactly that size.
	bodyH int
	// rowCount is how many physical body rows the last render measured, so the
	// end key and the clamp know where the content stops.
	rowCount int
	// inner is the body width of the last render, in cells. Rows are wrapped
	// and the action row is composed against it.
	inner int
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
// discard), the two-press `d` discard confirmation, and the body scroll keys.
// Scrolling only moves the window: it never accepts, keeps or deletes.
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
	case "up":
		p.scrollBy(-1)
		return nil
	case "down":
		p.scrollBy(1)
		return nil
	case "pgup":
		p.scrollBy(-max(p.bodyH, 1))
		return nil
	case "pgdown":
		p.scrollBy(max(p.bodyH, 1))
		return nil
	case "home":
		p.scroll = 0
		return nil
	case "end":
		p.scroll = max(p.rowCount-p.bodyH, 0)
		return nil
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
//
// Body rows are wrapped to the panel's real interior width before they are
// counted, so a long diagnostic or path becomes several physical rows instead
// of being cut off by the frame. The body is then windowed to the rows the
// budget leaves once the frame header, the action row and — only when the body
// overflows — the overflow-position row are reserved.
func (p *Panel) View(width, maxHeight int) string {
	th := theme.Current()
	pw := layout.PanelWidth(width)
	// The frame's border columns and gutters take four cells; the panel width
	// reserves two of them, so the body keeps pw-2.
	p.inner = max(pw-2, 1)
	inner := p.inner

	// The header row, one body row and the action row are the minimum that
	// can still show what `d` would delete.
	if maxHeight < 3 || inner < minReviewInner {
		p.visible = false
		return theme.MutedStyle().Render("Review plan")
	}

	rows := p.bodyRows(th)
	// The chrome header and the action row are paid for first: the action row
	// names the discard action, the failure, or the candidate it would delete
	// while armed, so it has to survive the diagnostics overflowing the body.
	bodyH := maxHeight - 2
	if bodyH < 1 {
		p.visible = false
		return theme.MutedStyle().Render("Review plan")
	}
	// The overflow-position row is charged to the body only when the body does
	// not fit. A one-row body keeps that row for content; the action row and
	// the border hint still say the rest is reachable.
	overflow := len(rows) > bodyH && bodyH > 1
	if overflow {
		bodyH--
	}

	p.rowCount = len(rows)
	p.bodyH = bodyH
	p.scroll = clampScroll(p.scroll, len(rows), bodyH)

	end := min(p.scroll+bodyH, len(rows))
	content := make([]string, 0, bodyH+2)
	content = append(content, rows[p.scroll:end]...)
	if overflow {
		content = append(content, p.positionRow(p.scroll, end, len(rows), inner))
	}
	content = append(content, p.actionRow(p.CandidatePath()))

	joined := strings.Join(content, "\n")
	ph := min(lipgloss.Height(joined)+1, maxHeight)
	// This render shows the path, the discard action and the arming state, so
	// the next `d` may act on it.
	p.visible = true
	return chrome.PanelWithHints("Review plan", p.hints(), joined, pw, ph, true, th)
}

// scrollBy moves the body window by delta physical rows, clamped to the rows
// and body height the last render measured. It only ever moves the window: it
// never accepts, keeps or deletes the candidate.
func (p *Panel) scrollBy(delta int) {
	p.scroll = clampScroll(p.scroll+delta, p.rowCount, p.bodyH)
}

// clampScroll pins offset to the windows a body of bodyH rows can show out of
// total rows, so a resize or a shorter candidate can never leave the body
// scrolled past its end.
func clampScroll(offset, total, bodyH int) int {
	maxOffset := max(total-bodyH, 0)
	return min(max(offset, 0), maxOffset)
}

// positionRow states where the visible body sits within the whole body, so a
// scrolled review never looks like the complete list.
func (p *Panel) positionRow(start, end, total, width int) string {
	row := fmt.Sprintf("↑↓ rows %d-%d of %d · pgup/pgdn", start+1, end, total)
	return theme.MutedStyle().Render(ansi.Truncate(row, max(width, 1), "…"))
}

// wrapText wraps one logical row to width cells. Each row is wrapped on its
// own so a physical row never spans a logical one, and a blank logical row
// stays exactly one blank row.
func wrapText(s string, width int) []string {
	if width < 1 {
		return []string{s}
	}
	return strings.Split(ansi.Wrap(s, width, reviewBreakpoints), "\n")
}

// fitRow composes one row from a prefix, a variable middle and a suffix within
// width cells, shortening only the middle. The fixed ends are the keys the row
// exists to keep reachable, so a long path or error must not push them out.
func fitRow(prefix, middle, suffix string, width int) string {
	room := width - ansi.StringWidth(prefix) - ansi.StringWidth(suffix)
	if room < 1 {
		return ansi.Truncate(prefix+suffix, max(width, 1), "…")
	}
	return prefix + ansi.Truncate(middle, room, "…") + suffix
}

// bodyRows builds the review's body rows and returns them as physical rows,
// already wrapped to the width of the last render: the candidate identity, the
// compile summary, the strategy and one row per diagnostic. Each diagnostic
// names its severity and location in its text, so no diagnostic depends on
// colour to be understood. Nothing here truncates: a row too long for the
// panel becomes several rows.
func (p *Panel) bodyRows(th theme.Theme) []string {
	width := max(p.inner, 1)
	muted := theme.MutedStyle()
	rows := make([]string, 0, 8)
	add := func(text string, style lipgloss.Style) {
		for _, row := range wrapText(text, width) {
			rows = append(rows, style.Render(row))
		}
	}

	add("candidate: "+strutil.ShowControls(p.CandidatePath()), muted)

	insp := p.result.Inspection
	if insp == nil {
		// A nil inspection is blocked, so Enter cannot accept it, but the
		// candidate can still be kept and edited.
		add("  plan error: no inspection available for this candidate", errorStyle())
		return rows
	}

	tasks := 0
	if insp.IR != nil {
		tasks = len(insp.IR.Tasks)
	}
	add(fmt.Sprintf(
		"%d task(s) · %d deterministic ops · %d fallback ops · est. %d model calls",
		tasks, insp.Report.Total.DetOps, insp.Report.Total.AgentOps, insp.Report.Total.EstCalls), muted)
	add("strategy: "+string(insp.EffectiveStrategy), muted)

	if len(insp.Diagnostics) > 0 {
		add("", muted)
		for _, d := range insp.Diagnostics {
			// TaskN is 0 for plan-level diagnostics; naming the location and
			// the severity in the text keeps every row readable without colour.
			where := "plan"
			if d.TaskN > 0 {
				where = fmt.Sprintf("task %d", d.TaskN)
			}
			severity, style := diagWarning, warnStyle()
			if d.Severity == diagError {
				severity, style = diagError, errorStyle()
			}
			row := fmt.Sprintf("  %s %s", where, severity)
			if d.Line > 0 {
				row += fmt.Sprintf(" · line %d", d.Line)
			}
			add(row+": "+strutil.ShowControls(d.Message), style)
		}
	}
	return rows
}

// actionRow renders the persistent footer: the discard confirmation while
// armed, the failure that kept the panel open, or the available actions. Its
// keys are composed against the body width so a long path or error can only be
// shortened itself, never the keys the row exists to keep reachable.
func (p *Panel) actionRow(path string) string {
	width := max(p.inner, 1)
	switch {
	case p.err != nil:
		return errorStyle().Render(fitRow("discard failed: ", strutil.ShowControls(p.err.Error()), " · esc keep", width))
	case p.discardArmed:
		return warnStyle().Render(fitRow("d again to discard ", strutil.ShowControls(path), " · esc keep", width))
	default:
		keys := "esc keep · d discard"
		if p.rowCount > p.bodyH {
			// The body is scrolled, so the keys that move it are actionable
			// too; they go after the actions the row already names.
			keys += " · ↑↓ scroll"
		}
		return theme.MutedStyle().Render(ansi.Truncate(keys, width, "…"))
	}
}

// hints is the top-border key hint. It stays actionable at any width, where
// the border label may be dropped and the action row carries the keys.
func (p *Panel) hints() string {
	if p.discardArmed {
		return "d again to discard · esc keep"
	}
	if p.rowCount > p.bodyH {
		return "↑↓ scroll · esc keep · d discard"
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
