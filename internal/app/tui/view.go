package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"marshal/internal/app/session"
)

const (
	inputBoxRows          = 3 // bordered input: top border + text row + bottom border
	activityStripRows     = 1
	commandSuggestionRows = 1
	inputAreaRowsReserved = activityStripRows + inputBoxRows
	transcriptFrameRows   = 2
	statusLineRows        = 1
)

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return m.fallbackView()
	}
	if m.settingsOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.settingsModel.View())
	}
	if m.memoryOpen {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.memoryModel.View())
	}

	transcript := m.renderTranscriptFrame()
	return lipgloss.JoinVertical(
		lipgloss.Left,
		transcript,
		m.renderInputArea(),
		m.renderStatusLine(m.width),
	)
}

func (m Model) renderTranscriptFrame() string {
	return lipgloss.NewStyle().
		Width(max(m.width-2, 1)).
		Height(max(m.viewport.Height, 1)).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("238")).
		Render(m.viewport.View())
}

func (m Model) renderInputArea() string {
	inputStyle := lipgloss.NewStyle().
		Width(max(m.width-2, 1)).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Padding(0, 1)

	rows := make([]string, 0, 4)

	if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			editLine := lipgloss.JoinHorizontal(
				lipgloss.Top,
				promptPrefixStyle.Render("❯ "),
				m.input.View(),
			)
			rows = append(rows, inputStyle.Render(editLine))
		} else {
			rows = append(rows, inputStyle.Render(renderApprovalPanel(tc, max(m.width-4, 1))))
		}
	} else {
		rows = append(rows, m.renderActivityStrip())
		if len(m.commandSuggestions) > 0 {
			rows = append(rows, m.renderCommandSuggestions())
		}
		inputLine := lipgloss.JoinHorizontal(
			lipgloss.Top,
			promptPrefixStyle.Render("❯ "),
			m.input.View(),
		)
		rows = append(rows, inputStyle.Render(inputLine))
	}

	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) renderActivityStrip() string {
	innerWidth := max(m.width-2, 1)
	available := max(innerWidth-2, 1)
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
	return lipgloss.NewStyle().
		Width(innerWidth).
		Padding(0, 1).
		Render(statusBusyStyle.Render(truncateRunes(label, available)))
}

func (m Model) renderCommandSuggestions() string {
	innerWidth := max(m.width-2, 1)
	available := max(innerWidth-2, 1)
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
	line := strings.Join(parts, "  ")
	return lipgloss.NewStyle().
		Width(innerWidth).
		Padding(0, 1).
		Render(line)
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
