package tui

import "github.com/charmbracelet/lipgloss"

const (
	inputBoxRows   = 3 // bordered input: top border + text row + bottom border
	statusLineRows = 1
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

	transcript := lipgloss.NewStyle().Padding(0, 1).Render(m.viewport.View())
	return lipgloss.JoinVertical(
		lipgloss.Left,
		transcript,
		m.renderInputArea(),
		m.renderStatusLine(m.width),
	)
}

func (m Model) renderInputArea() string {
	inputStyle := lipgloss.NewStyle().
		Width(max(m.width-2, 1)).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(panelBorderColor).
		Padding(0, 1)
	inputLine := lipgloss.JoinHorizontal(
		lipgloss.Top,
		promptPrefixStyle.Render("❯ "),
		m.input.View(),
	)
	return inputStyle.Render(inputLine)
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
