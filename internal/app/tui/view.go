package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ansiRe matches SGR (and empty) escape sequences that lipgloss emits.
// Shared between production rendering helpers and tests that need to
// inspect visible runes without the styling the borders now carry.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

const (
	transcriptFrameRows = 0
	statusLineRows      = 1
	completionPopupMax  = 8
)

// View assembles the full-screen frame. Alt screen and mouse mode are
// declared here — in Bubble Tea v2 they are View fields rather than
// program options.
func (m Model) View() tea.View {
	v := tea.NewView(m.viewString())
	v.AltScreen = true
	// MouseModeNone (the zero value): do not capture mouse events. This lets
	// the terminal emulator perform native click-drag text selection.
	// Scrolling is keyboard-driven (PgUp/PgDn/Ctrl+U/Ctrl+D/End).
	v.MouseMode = tea.MouseModeNone
	return v
}

func (m *Model) viewString() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}
	if m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight {
		return m.tooSmallView()
	}
	dockView := m.dock.View(m.width, m.height)
	m.updateViewportHeight()

	rows := []string{m.renderTranscriptFrame()}
	if todo := m.renderTodoPanel(); todo != "" {
		rows = append(rows, todo)
	}
	if sd := m.state.SDDProgress(); sd.Active {
		rows = append(rows, sddPanel(sd, m.width))
	}
	if strip := m.renderLiveStrip(); strip != "" {
		rows = append(rows, strip)
	}
	if dockView != "" {
		rows = append(rows, dockView)
	}
	rows = append(rows, m.renderInputArea(), m.renderStatusLine(m.width))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) renderTranscriptFrame() string {
	vpWidth := max(m.width, 1)
	vpHeight := max(m.viewport.Height(), 1)
	content := lipgloss.NewStyle().Width(vpWidth).Height(vpHeight).Render(m.viewport.View())
	if !m.viewportFollow && m.viewport.TotalLineCount() > m.viewport.Height() {
		hint := mutedStyle().Render("↑ scrolled — End to follow")
		return lipgloss.JoinVertical(lipgloss.Left, hint, content)
	}
	return content
}

func (m Model) renderInputArea() string {
	inputInnerWidth := max(m.width-4, 1)
	rows := make([]string, 0, 4)

	if q := m.state.PendingQuestion(); q != nil {
		if m.questionModel != nil {
			rows = append(rows, m.questionModel.View())
		} else {
			rows = append(rows, renderQuestionPanel(q, inputInnerWidth))
		}
		rows = append(rows, m.gutteredInput())
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
// multi-line block above the input area. Returns "" when no popup is
// visible. Capped at completionPopupMax rows. The popup scrolls so the
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
	available := max(m.width-4, 1)
	rows := make([]string, 0, completionPopupMax)
	max := completionPopupMax
	if len(matches) < max {
		max = len(matches)
	}
	// The popup's reconcileOffset() is the single source of truth for
	// scroll position. The renderer only reads p.viewOffset.
	offset := p.viewOffset
	for i := 0; i < max; i++ {
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
		row = ansi.Cut(row, 0, available)
		rows = append(rows, style.Render(row))
	}
	return strings.Join(rows, "\n")
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
