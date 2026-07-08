package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
)

// ansiRe matches SGR (and empty) escape sequences that lipgloss emits.
// Shared between production rendering helpers and tests that need to
// inspect visible runes without the styling the borders now carry.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

const (
	inputBorderRows       = 2
	activityStripRows     = 1
	commandSuggestionRows = 1
	transcriptFrameRows   = 0
	statusLineRows        = 1
)

// View assembles the full-screen frame. Alt screen and mouse mode are
// declared here — in Bubble Tea v2 they are View fields rather than
// program options.
func (m Model) View() tea.View {
	v := tea.NewView(m.viewString())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) viewString() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}
	if m.settingsOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModel.View())
	}
	if m.memoryOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.memoryModel.View())
	}

	rows := []string{m.renderTranscriptFrame()}
	if panel := renderSwarmPanel(m.state.SwarmProgress(), m.spinnerFrame, m.width); panel != "" {
		rows = append(rows, panel)
	}
	rows = append(rows, m.renderInputArea(), m.renderStatusLine(m.width))
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
		rows = append(rows, renderQuestionPanel(q, inputInnerWidth))
		inputLine := lipgloss.JoinHorizontal(
			lipgloss.Top,
			inputPromptStyle.Render("❯ "),
			m.input.View(),
		)
		rows = append(rows, inputLine)
	} else if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			editLine := lipgloss.JoinHorizontal(
				lipgloss.Top,
				inputPromptStyle.Render("❯ "),
				m.input.View(),
			)
			rows = append(rows, editLine)
		} else {
			rows = append(rows, renderApprovalPanel(tc, m.state.SandboxInfo(), m.state.Config.Tools.Shell.AllowNetwork, inputInnerWidth))
		}
	} else {
		rows = append(rows, m.renderActivityStrip())
		if len(m.commandSuggestions) > 0 {
			rows = append(rows, m.renderCommandSuggestions())
		}
		inputLine := lipgloss.JoinHorizontal(
			lipgloss.Top,
			inputPromptStyle.Render("❯ "),
			m.input.View(),
		)
		rows = append(rows, inputLine)
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
	label := ""
	switch activity.Kind {
	case session.ActivityThinking:
		label = fmt.Sprintf("%s thinking", m.spinnerFrame)
	case session.ActivityTool:
		elapsed := m.now().Sub(activity.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		label = fmt.Sprintf("%s %s · %s", m.spinnerFrame, activity.Label, formatElapsed(elapsed))
	}
	return statusBusyStyle.Render(truncateRunes(label, available))
}

func (m Model) renderCommandSuggestions() string {
	available := max(m.width-4, 1)
	parts := make([]string, 0, len(m.commandSuggestions))
	separatorWidth := 2 * max(len(m.commandSuggestions)-1, 0)
	itemWidth := max((available-separatorWidth)/max(len(m.commandSuggestions), 1), 8)
	for i, cmd := range m.commandSuggestions {
		name := "/" + cmd.Name
		if cmd.Args != "" {
			name += " " + cmd.Args
		}
		item := name
		if cmd.Description != "" {
			item += " - " + cmd.Description
		}
		item = truncateRunes(item, itemWidth)
		if i == m.commandSuggestionIndex {
			item = promptPrefixStyle.Render(item)
		} else {
			item = mutedStyle.Render(item)
		}
		parts = append(parts, item)
	}
	return strings.Join(parts, "  ")
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
