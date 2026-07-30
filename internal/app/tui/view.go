package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
	"marshal/internal/strutil"
)

// ansiRe matches SGR (and empty) escape sequences that lipgloss emits.
// Shared between production rendering helpers and tests that need to
// inspect visible runes without the styling the borders now carry.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

const (
	transcriptFrameRows = 0
	// statusLineRows aliases layout.StatusLineRows. The source of truth
	// lives in internal/app/tui/layout; the alias keeps in-package call
	// sites (model.go, view_test.go, input_area_test.go) referencing the
	// unqualified name. The dock package duplicates the value because tui
	// imports dock but not vice versa; see dock/dock_test.go for the
	// guard test that pins the duplicate.
	statusLineRows     = layout.StatusLineRows
	completionPopupMax = 8
	// completionPanelChromeRows is the number of rows the chrome.Panel title
	// adds above the completion popup's match rows.
	completionPanelChromeRows = 1
	// minTranscriptRows is the transcript floor reserved when budgeting the
	// textarea's MaxHeight. When a 3-row transcript and a 1-row input do not
	// both fit, the transcript floor yields first — a zero-row input cannot
	// be typed into, a one-row transcript is still usable.
	minTranscriptRows = 3
)

// View assembles the full-screen frame. Alt screen and mouse mode are
// declared here — in Bubble Tea v2 they are View fields rather than
// program options.
func (m Model) View() tea.View {
	v := tea.NewView(m.viewString())
	v.AltScreen = true
	// Mouse capture is a trade, so it is configurable (tui.mouse_capture,
	// default on). With MouseModeCellMotion the wheel scrolls the
	// transcript — necessary because AltScreen means there is no terminal
	// scrollback to fall back on — and native click-drag text selection
	// needs the terminal's override modifier (Option/Alt in iTerm2, Ghostty,
	// Kitty, Terminal.app). With MouseModeNone the terminal owns the mouse
	// entirely and scrolling is keyboard-only (PgUp/PgDn/Ctrl+U/Ctrl+D/End).
	if m.state != nil && !m.state.Config.TUI.MouseCapture {
		v.MouseMode = tea.MouseModeNone
	} else {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m *Model) viewString() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}
	if m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight {
		return m.tooSmallView()
	}
	dockView := m.dock.View(m.leftWidth, m.height)
	m.updateViewportHeight()

	var left string
	if m.dock.FullFrameOpen() {
		// A FullFrame panel owns everything above the status line: the
		// transcript, todo panel, live strip, and input area are hidden.
		left = dockView
	} else {
		rows := []string{m.renderTranscriptFrame()}
		// The spinner groups with the transcript whose progress it
		// describes, keeping the todo list adjacent to the input.
		rows = append(rows, m.renderTurnSpinner())
		if todo := m.renderTodoPanel(); todo != "" {
			rows = append(rows, todo)
		}
		if sd := m.state.SDDProgress(); sd.Active {
			rows = append(rows, sddPanel(sd, m.leftWidth))
		}
		if strip := m.renderLiveStrip(); strip != "" {
			rows = append(rows, strip)
		}
		if dockView != "" {
			rows = append(rows, dockView)
		}
		rows = append(rows, m.renderInputArea())
		left = lipgloss.JoinVertical(lipgloss.Left, rows...)
	}
	if m.railEnabled() {
		railHeight := m.height - statusLineRows
		if rv := m.rail.View(m.railData(), m.railWidth, railHeight); rv != "" {
			left = lipgloss.JoinHorizontal(lipgloss.Top, left, rv)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, left, m.renderStatusLine(m.width))
}

func (m Model) renderTranscriptFrame() string {
	vpWidth := max(m.leftWidth, 1)
	vpHeight := max(m.viewport.Height(), 1)
	content := lipgloss.NewStyle().Width(vpWidth).Height(vpHeight).Render(m.viewport.View())
	if m.scrollHintRows() > 0 {
		hint := mutedStyle().Render("↑ scrolled — End to follow")
		return lipgloss.JoinVertical(lipgloss.Left, hint, content)
	}
	return content
}

// renderTurnSpinner renders the pinned spinner row directly above the todo
// panel. It answers one question — is the agent still running? — and so is
// driven by the turn-level busy flag rather than session.Activity, which
// resets to ActivityIdle between phases. Elapsed time only: the phase detail
// belongs to the transcript's live blocks.
//
// The row is always reserved (see turnSpinnerRows); it renders blank while
// idle so the transcript frame does not shift when a turn starts.
func (m Model) renderTurnSpinner() string {
	if !m.busy || m.turnStartedAt.IsZero() {
		return ""
	}
	elapsed := m.now().Sub(m.turnStartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	text := spinnerLabel(m.turnSpinnerFrame(), formatElapsed(elapsed))
	return statusBusyStyle().Render(" " + strutil.Truncate(text, max(m.leftWidth-1, 1), false))
}

func (m Model) renderInputArea() string {
	inputInnerWidth := max(m.leftWidth-4, 1)
	rows := make([]string, 0, 4)

	if q := m.state.PendingQuestion(); q != nil {
		if m.questionModel != nil {
			rows = append(rows, m.questionModel.View())
		} else {
			rows = append(rows, renderQuestionPanel(q, inputInnerWidth))
		}
		// The main textarea is deliberately NOT rendered here: while a
		// question is pending every keypress routes to the question form
		// (handleQuestion), so a visible textarea would be a dead input
		// that swallows nothing yet appears typable.
	} else if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			rows = append(rows, m.gutteredInput())
		} else if m.approvalModel != nil {
			rows = append(rows, m.approvalModel.View())
		} else {
			rows = append(rows, renderApprovalPanel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, inputInnerWidth))
		}
	} else {
		if m.state.SDDProgress().Active {
			rows = append(rows, mutedStyle().Render("SDD running — /stop to cancel, wait for completion to resume typing"))
		}
		if popup := m.renderCompletionPopup(); popup != "" {
			rows = append(rows, popup)
		}
		rows = append(rows, m.gutteredInput())
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// inputBarColor picks the ▍ state-bar color. This is the input box's old
// border-color semantics compressed into one cell (spec: "state moves to
// the ▍❯ prompt").
func (m Model) inputBarColor() color.Color {
	switch {
	case m.successPulse:
		return tealColor
	case m.state.PendingQuestion() != nil:
		return violetColor
	case m.state.PendingApproval() != nil:
		return warningColor
	case m.state.SDDProgress().Active, !m.input.Focused():
		return dimColor
	default:
		return coralColor
	}
}

// gutteredInput renders the textarea with the ▍ state bar prepended to
// every display line.
func (m Model) gutteredInput() string {
	bar := lipgloss.NewStyle().Foreground(m.inputBarColor()).Render("▍")
	lines := strings.Split(m.input.View(), "\n")
	for i := range lines {
		lines[i] = bar + lines[i]
	}
	return strings.Join(lines, "\n")
}

// chromeRail prefixes every line of s with the ▍ state bar in color c — the
// same rail the input box wears — so stacked panels (approval, question,
// todos) read as one contained unit with the input. A trailing newline on s
// is preserved; empty input stays empty.
func chromeRail(s string, c color.Color) string {
	if s == "" {
		return ""
	}
	trailing := strings.HasSuffix(s, "\n")
	body := strings.TrimRight(s, "\n")
	bar := lipgloss.NewStyle().Foreground(c).Render("▍")
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = bar + lines[i]
	}
	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out
}

// highlightMatches bolds runes at the given byte indices using the
// active theme's AccentPrimary color. The indices are byte positions in
// the (ASCII-dominated) text. For non-ASCII text the highlight may
// misalign on multi-byte runes — acceptable for file paths/commands.
func highlightMatches(text string, idxs []int) string {
	if len(idxs) == 0 {
		return text
	}
	iSet := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		iSet[i] = true
	}
	var b strings.Builder
	hl := lipgloss.NewStyle().Bold(true).Foreground(activeTheme.AccentPrimary)
	for i, r := range []rune(text) {
		if iSet[i] {
			b.WriteString(hl.Render(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// renderCompletionPopup renders the active F18 completion popup as a
// guttered panel above the input area, using the same chrome.Panel dressing
// as settings, pickers, and docked panels. Returns "" when no popup is
// visible. Capped at completionPopupMax match rows. The popup scrolls so the
// selected item (p.index) is always visible.
func (m Model) renderCompletionPopup() string {
	p := m.activeCompletionPopup()
	if p == nil {
		return ""
	}
	matches := p.matches()
	if len(matches) == 0 {
		return ""
	}
	width := max(m.leftWidth-4, 1)
	inner := max(width-2, 1) // chrome.Panel reserves 2 cells for the ▍ gutter
	rows := make([]string, 0, completionPopupMax)
	limit := completionPopupMax
	if len(matches) < limit {
		limit = len(matches)
	}
	// The popup's reconcileOffset() is the single source of truth for
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
		row = ansi.Cut(row, 0, inner)
		rows = append(rows, style.Render(row))
	}
	title := "Commands"
	if p == m.filePopup {
		title = "Files"
	} else if p == m.setPopup {
		title = "Settings"
	}
	return chrome.PanelWithHints(title, "↑↓ select · ↵ accept · esc dismiss",
		strings.Join(rows, "\n"), width, limit+1, true, theme.Current())
}

func (m Model) tooSmallView() string {
	boxW := max(m.rawWidth, 1)
	boxH := max(m.rawHeight, 1)
	msg := fmt.Sprintf("Terminal too small\nResize to at least %d×%d", minTerminalWidth, minTerminalHeight)
	wrapped := ansi.Wrap(msg, boxW, "")
	trimmedLines := strings.Split(wrapped, "\n")
	if len(trimmedLines) > boxH {
		trimmedLines = trimmedLines[:boxH]
	}
	for i, line := range trimmedLines {
		trimmedLines[i] = ansi.Cut(line, 0, boxW)
	}
	return lipgloss.Place(boxW, boxH, lipgloss.Center, lipgloss.Center,
		mutedStyle().Render(strings.Join(trimmedLines, "\n")),
	)
}

func (m Model) fallbackView() string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		mutedStyle().Render("Marshal — waiting for terminal resize..."),
	)
}
