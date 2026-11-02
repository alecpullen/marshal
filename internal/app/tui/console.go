package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/help"
	"marshal/internal/app/tui/theme"
)

// consoleSection is one part of the console: a header row drawn into the
// outline, then the content rows inside "│ … │".
type consoleSection struct {
	label, right string      // the header's labels, already styled; both "" draws a plain divider
	color        color.Color // this section's border colour
	lines        []string    // content rows, drawn inside "│ … │"
}

// consoleAbove says how the framed part directly above the console ends, which
// decides the first section's opening row.
type consoleAbove int

const (
	aboveNothing consoleAbove = iota // nothing framed above: the first header is ╭─ … ─╮
	aboveFramed                      // a framed part above ends in a content row: the first header is ├─ … ─┤
	aboveOpen                        // a framed part above ends in a border row: an unlabelled first section draws no header
)

// sectionDrawsHeader is the one place that decides whether section i opens with
// a header row. renderConsole and inputHeaderRows both ask it, so the painted
// console and the row budget cannot disagree.
func sectionDrawsHeader(i int, label string, above consoleAbove) bool {
	if i > 0 {
		return true
	}
	// Under an open border row the console continues the outline without a
	// divider of its own, unless it carries a label to put in one.
	return above != aboveOpen || label != ""
}

// renderConsole draws the console: every section's header, then its content
// rows inside "│ … │", in that section's colour. It never draws a bottom
// border — the status line closes the outline.
//
// Sections carry plain text, not frame directives: chrome.Divider lines are
// understood by chrome.Frame.Render, not here, so a section that emitted one
// would print the private-use mark as content.
func renderConsole(sections []consoleSection, above consoleAbove, w int) string {
	if len(sections) == 0 {
		return ""
	}
	inner := max(w-4, 1)
	out := make([]string, 0, len(sections)*2)
	for i, s := range sections {
		if sectionDrawsHeader(i, s.label, above) {
			out = append(out, sectionHeader(i, s, above, w))
		}
		border := lipgloss.NewStyle().Foreground(s.color)
		for _, line := range s.lines {
			body := ansi.Truncate(line, inner, "…")
			body += strings.Repeat(" ", max(inner-ansi.StringWidth(body), 0))
			out = append(out, border.Render("│ ")+body+border.Render(" │"))
		}
	}
	return strings.Join(out, "\n")
}

// sectionHeader draws one section's top row: a corner when the console opens
// here, tees when it continues the outline, and a plain rule when the section
// carries no label.
func sectionHeader(i int, s consoleSection, above consoleAbove, w int) string {
	open, close, corner := "├─ ", "┤", "├"
	if i == 0 && above == aboveNothing {
		open, close, corner = "╭─ ", "╮", "╭"
	}
	if s.label == "" && s.right == "" {
		return lipgloss.NewStyle().Foreground(s.color).Render(corner + strings.Repeat("─", max(w-2, 1)) + close)
	}
	return chrome.BorderLine(open, s.label, s.right, close, w, s.color)
}

// consoleColor is the console's border colour. It reports what the composer is
// in the middle of; nothing else tints the chrome.
func (m Model) consoleColor() color.Color {
	p := m.sess.Pending()
	switch {
	case m.successPulse:
		return successColor
	case p.Sudo != nil:
		return warningColor
	case p.SkillGate != nil, p.Question != nil:
		return violetColor
	case m.hasPendingApproval():
		return warningColor
	default:
		return theme.Current().BorderMuted
	}
}

// consoleAbove reports how the framed part directly above the console ends: an
// open, framed docked panel counts as framed, otherwise the tray's state
// decides.
//
// During the budget pass inside livebox.Plan the memo still holds the previous
// frame's layout (it is replaced only after Plan returns). That is deliberate:
// re-planning here would recurse, and the only consequence is that the tray
// may shed one row differently on the frame the tray's own shape changes. The
// frame height stays correct because updateViewportHeight recomputes against
// the fresh memo and subtracts the rows actually planned.
func (m Model) consoleAbove() consoleAbove {
	if m.dock.IsOpen() && m.dockFramed {
		return aboveFramed
	}
	if m.boxMemo == nil {
		// A budget query before viewString has planned this frame's tray.
		// Planning it here would recurse — boxInput sizes itself from the
		// input area's rows — so report the idle case; inputHeaderRows
		// budget for a header regardless, which is what keeps the frame from
		// overflowing.
		return aboveNothing
	}
	layout := m.boxLayout()
	switch {
	case layout.Open:
		return aboveOpen
	case layout.Framed():
		return aboveFramed
	default:
		return aboveNothing
	}
}

// composerView renders the textarea with the active next-prompt suggestion
// overlaid as grey ghost text after the cursor. It carries no gutter: the
// console draws the gutter around it.
func (m *Model) composerView() string {
	placeholder := m.input.Placeholder
	if m.input.Value() == "" && m.suggestionGhost() != "" {
		m.input.Placeholder = ""
		defer func() { m.input.Placeholder = placeholder }()
	}
	view := m.input.View()
	if ghost := m.suggestionGhost(); ghost != "" {
		// No fallback position: when the cursor's row and column cannot be
		// derived from model state, the ghost is not drawn at all. A ghost in
		// the wrong place is worse than no ghost.
		if row, col, ok := m.ghostPosition(); ok {
			view = overlayGhost(view, row, col, renderGhost(ghost))
		}
	}
	return view
}

// consoleLines splits a rendered prompt view into the content rows the console
// draws inside its gutter.
func consoleLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// inputSections builds the console's sections in the order the input area
// resolves its states: one prompt or composer owns the body at a time, and the
// completion popup sits above the composer while it is open.
func (m Model) inputSections() []consoleSection {
	c := m.consoleColor()
	only := func(lines []string) []consoleSection {
		return []consoleSection{{color: c, lines: lines}}
	}
	prompt := func(label, right string, lines []string) []consoleSection {
		return []consoleSection{{
			label: consoleLabelStyle(labelColor(label)).Render(label),
			right: right,
			color: c,
			lines: lines,
		}}
	}
	// A prompt's keys ride in its header; the footer then keeps only the stop
	// key (help.Footer reads the same flag).
	keys := help.PromptKeys(m.footerHints())

	// While the Send now composer owns the input, the base editor renders no
	// cursor and none of its ordinary prompt controls: the completion popup,
	// the approval/question/gate/privileged prompts and the base draft are all
	// suspended, and showing a second live input would lie about where typing
	// goes.
	if m.steeringComposer != nil {
		return only([]string{mutedStyle().Render("Send now owns the input — Esc returns to the previous surface.")})
	}

	// The prompts own every keypress while they are pending, so the textarea
	// is deliberately not rendered beside them. One Pending read serves the
	// three prompt cases below.
	pending := m.sess.Pending()
	if sa := pending.Sudo; sa != nil {
		if m.sudoAuthModel != nil {
			return prompt("⚠ Sudo password", "", consoleLines(m.sudoAuthModel.View()))
		}
		return prompt("⚠ Sudo password", "", consoleLines(renderSudoAuthPanel(*sa, max(m.leftWidth-4, 1))))
	}
	if sg := pending.SkillGate; sg != nil {
		if m.skillGateModel != nil {
			return prompt("? Skill gate", keys, consoleLines(m.skillGateModel.View()))
		}
		return prompt("? Skill gate", keys, consoleLines(renderSkillGatePanel(*sg, max(m.leftWidth-4, 1))))
	}
	if q := pending.Question; q != nil {
		if m.questionModel != nil {
			return prompt("? Question", keys, consoleLines(m.questionModel.View()))
		}
		return prompt("? Question", keys, consoleLines(renderQuestionPanel(*q, max(m.leftWidth-4, 1))))
	}
	if a, ok := m.pendingApprovalDisplay(); ok {
		st := m.sess.Status()
		switch {
		case a.Access:
			// A typed access request is presented by the accesspanel, which
			// owns the decision. When another panel holds the dock the request
			// is queued: disclose it without rendering a second approve/deny
			// form, and leave the main draft untouched.
			if m.dock.IsOpen() && !m.accessPanelOwnsDock() {
				return only([]string{mutedStyle().Render(accessWaitingRow)})
			}
			return nil
		case m.editingApproval():
			// The isolated edit editor renders its own ❯ prompt (it is
			// configured exactly like the composer), so its View already
			// includes it — do not prepend it again. The chat textarea is
			// deliberately not rendered while the edit owns the input.
			return only(consoleLines(m.approvalEdit.input.View()))
		case m.approvalModel != nil:
			return prompt("⚠ Approval", keys, consoleLines(m.approvalModel.View()))
		default:
			return prompt("⚠ Approval", keys, consoleLines(renderApprovalPanel(a, st.Sandbox, max(m.leftWidth-4, 1))))
		}
	}

	// The doctor repair's masked key field sits after the prompt chain and
	// before the composer: the doctor panel is already closed while it is up,
	// and it owns every keystroke, so the textarea is deliberately not rendered
	// beside it.
	if m.doctorSecret != nil {
		return prompt("⚠ API key", mutedStyle().Render(m.doctorSecret.provider), m.doctorSecretLines())
	}

	var sections []consoleSection
	if popup, ok := m.completionSection(); ok {
		sections = append(sections, popup)
	}
	var composer []string
	if m.sess.Status().SDD.Active {
		composer = append(composer, mutedStyle().Render("SDD running — /stop to cancel, wait for completion to resume typing"))
	}
	composer = append(composer, consoleLines(m.composerView())...)
	return append(sections, consoleSection{color: c, lines: composer})
}

// labelColor is the colour a prompt's section label is drawn in, matching the
// colour consoleColor gives that prompt's border.
func labelColor(label string) color.Color {
	switch label {
	case "? Question", "? Skill gate":
		return violetColor
	default:
		return warningColor
	}
}

// completionSection builds the command popup as a console section: its label is
// the popup's title, its right label the popup's hint, and its rows are the
// match rows the old panel drew.
func (m Model) completionSection() (consoleSection, bool) {
	p := m.activeCompletionPopup()
	if p == nil {
		return consoleSection{}, false
	}
	matches := p.matches()
	if len(matches) == 0 {
		return consoleSection{}, false
	}
	inner := max(m.leftWidth-4, 1)
	limit := min(len(matches), completionPopupMax)
	rows := make([]string, 0, limit)
	// The popup's reconcileOffset() is the single source of truth for the
	// scroll position. The renderer only reads p.viewOffset.
	offset := p.viewOffset
	for i := 0; i < limit; i++ {
		mi := offset + i
		marker := "  "
		style := mutedStyle()
		if mi == p.index {
			marker = "▸ "
			style = promptPrefixStyle()
		}
		display := matches[mi].Text
		if matches[mi].Kind == completionCommand && !strings.HasPrefix(display, "/") {
			display = "/" + display
		}
		row := marker + highlightMatches(display, matches[mi].matchedIdxs)
		if matches[mi].Description != "" {
			row += "  " + matches[mi].Description
		}
		rows = append(rows, style.Render(ansi.Cut(row, 0, inner)))
	}
	title, hint := "Commands", "↑↓ select · ↵ run · esc dismiss"
	if p == m.filePopup {
		title, hint = "Files", "↑↓ select · ↵ accept · esc dismiss"
	} else if p == m.setPopup {
		title, hint = "Settings", "↑↓ select · ↵ accept · esc dismiss"
	}
	return consoleSection{
		label: sectionLabelStyle().Render(title),
		right: mutedStyle().Render(hint),
		color: m.consoleColor(),
		lines: rows,
	}, true
}

// sectionLabelStyle styles a section label the way chrome draws a focused
// panel title: accent-secondary and bold, with the bold dropped in monochrome.
func sectionLabelStyle() lipgloss.Style {
	return consoleLabelStyle(theme.Current().AccentSecondary)
}

// consoleLabelStyle styles a section label in c, dropping the bold weight in
// monochrome.
func consoleLabelStyle(c color.Color) lipgloss.Style {
	style := lipgloss.NewStyle().Foreground(c)
	if !theme.IsMonochromeTheme(theme.Current()) {
		style = style.Bold(true)
	}
	return style
}

// inputHeaderRows counts the console header rows the input area adds. The
// popup's header is not counted here: inputChromeRows already budgets it as
// completionPanelChromeRows.
func (m Model) inputHeaderRows() int {
	sections := m.inputSections()
	above := m.consoleAbove()
	rows := 0
	for i, s := range sections {
		if sectionDrawsHeader(i, s.label, above) {
			rows++
		}
	}
	if _, ok := m.completionSection(); ok {
		rows--
	}
	return max(rows, 0)
}
