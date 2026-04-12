package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorGreen  = lipgloss.Color("2")
	colorRed    = lipgloss.Color("1")
	colorYellow = lipgloss.Color("3")
	colorBlue   = lipgloss.Color("4")
	colorFaint  = lipgloss.Color("8")

	styleUser = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBlue)

	styleExecutor = lipgloss.NewStyle().
			Foreground(lipgloss.NoColor{})

	styleMarshal = lipgloss.NewStyle().
			Foreground(colorYellow)

	stylePass = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorGreen)

	styleFail = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorRed)

	styleSystem = lipgloss.NewStyle().
			Foreground(colorFaint).
			Italic(true)

	styleStatusBar = lipgloss.NewStyle().
			Foreground(colorFaint)

	styleStatusActive = lipgloss.NewStyle().
				Foreground(colorYellow)

	stylePromptPrefix = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorBlue)

	styleInputBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorFaint).
				PaddingLeft(1)
)
