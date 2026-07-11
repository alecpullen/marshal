package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/help"
)

// ansiRe matches SGR (and empty) escape sequences that lipgloss emits.
// Shared between production rendering helpers and tests that need to
// inspect visible runes without the styling the borders now carry.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

const (
	inputBorderRows     = 2
	activityStripRows   = 1
	transcriptFrameRows = 0
	footerRows          = 1
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

func (m Model) viewString() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}
	if m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight {
		return m.tooSmallView()
	}
	if m.settingsOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModel.View())
	}
	if m.memoryOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.memoryModel.View())
	}
	if m.helpOpen {
		return help.Overlay(m.width, m.height)
	}

	rows := []string{m.renderTranscriptFrame()}
	// Swarm roles are tool-driven; use ActivityTool as the gating kind.
	swarmSpinner := m.activeSpinnerFrame(session.ActivityTool)
	if panel := renderSwarmPanel(m.state.SwarmProgress(), swarmSpinner, m.width); panel != "" {
		rows = append(rows, panel)
	}
	rows = append(rows, m.renderInputArea(), m.renderHelpFooter(), m.renderStatusLine(m.width))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) renderTranscriptFrame() string {
	if !m.viewportFollow && m.viewport.TotalLineCount() > m.viewport.Height() {
		hint := mutedStyle.Render("↑ scrolled — End to follow")
		vpHeight := max(m.viewport.Height()-1, 1)
		vpView := lipgloss.NewStyle().
			Width(max(m.width-2, 1)).
			Height(vpHeight).
			Render(m.viewport.View())
		return lipgloss.JoinVertical(lipgloss.Left, hint, vpView)
	}
	return lipgloss.NewStyle().
		Width(max(m.width-2, 1)).
		Height(max(m.viewport.Height(), 1)).
		Render(m.viewport.View())
}

func (m Model) renderInputArea() string {
	inputInnerWidth := max(m.width-4, 1)

	rows := make([]string, 0, 4)

	if q := m.state.PendingQuestion(); q != nil {
		if m.questionModel != nil {
			rows = append(rows, m.questionModel.View())
		} else {
			rows = append(rows, renderQuestionPanel(q, inputInnerWidth))
			// The ❯ prompt is rendered inside the textarea by SetPromptFunc.
			rows = append(rows, m.input.View())
		}
	} else if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			rows = append(rows, m.input.View())
		} else if m.approvalModel != nil {
			rows = append(rows, m.approvalModel.View())
		} else {
			rows = append(rows, renderApprovalPanel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, inputInnerWidth))
		}
	} else {
		if strip := m.renderActivityStrip(); strip != "" {
			rows = append(rows, strip)
		}
		if popup := m.renderCompletionPopup(); popup != "" {
			rows = append(rows, popup)
		}
		rows = append(rows, m.input.View())
	}

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)
	border := coralColor
	if !m.input.Focused() {
		border = mauveColor
	}
	return inputBoxStyle.BorderForeground(border).Width(inputInnerWidth).Render(content)
}

func (m Model) renderActivityStrip() string {
	available := max(m.width-4, 1)
	activity := m.state.Activity()
	spinner := m.activeSpinnerFrame(activity.Kind)
	label := ""
	switch activity.Kind {
	case session.ActivityThinking:
		label = spinnerLabel(spinner, "thinking")
	case session.ActivityTool:
		elapsed := m.now().Sub(activity.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		label = spinnerLabel(spinner, fmt.Sprintf("%s · %s", activity.Label, formatElapsed(elapsed)))
	default:
		return ""
	}
	return statusBusyStyle.Render(truncateRunes(label, available))
}

// renderHelpFooter returns the persistent keybinding hint bar shown below
// the input area and above the status line.
func (m Model) renderHelpFooter() string {
	hints := help.FooterHints{
		Busy:            m.busy,
		EditingCommand:  m.editingCommand,
		ApprovalPending: m.state.PendingApproval() != nil,
		QuestionPending: m.state.PendingQuestion() != nil,
		PopupOpen:       m.activeCompletionPopup() != nil,
	}
	return mutedStyle.Width(max(m.width, 1)).Render(help.Footer(hints))
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
// visible. Capped at completionPopupMax rows.
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
	for i := 0; i < max; i++ {
		marker := "  "
		style := mutedStyle
		if i == p.index {
			marker = "▸ "
			style = promptPrefixStyle
		}
		row := marker + highlightMatches(matches[i].Text, matches[i].matchedIdxs)
		if matches[i].Description != "" {
			row += "  " + matches[i].Description
		}
		row = truncateRunes(row, available)
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
		mutedStyle.Render(strings.Join(trimmedLines, "\n")),
	)
}

func (m Model) fallbackView() string {
	if m.settingsOpen {
		return m.settingsModel.View()
	}
	if m.memoryOpen {
		return m.memoryModel.View()
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		mutedStyle.Render("Marshal — waiting for terminal resize..."),
	)
}
