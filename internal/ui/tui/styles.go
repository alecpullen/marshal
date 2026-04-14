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

	styleLint = lipgloss.NewStyle().
			Foreground(colorYellow).
			Italic(true)

	styleSystem = lipgloss.NewStyle().
			Foreground(colorFaint).
			Italic(true)

	styleFaint = lipgloss.NewStyle().
			Foreground(colorFaint)

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

	// Tool operation styles for compact display
	styleToolOperation = lipgloss.NewStyle().
				Foreground(colorFaint)

	styleToolName = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBlue)

	styleToolPath = lipgloss.NewStyle().
			Foreground(lipgloss.NoColor{})

	styleToolStatusRunning = lipgloss.NewStyle().
				Foreground(colorYellow)

	styleToolStatusReading = lipgloss.NewStyle().
				Foreground(colorBlue)

	styleToolStatusWriting = lipgloss.NewStyle().
				Foreground(colorYellow)

	styleToolStatusDone = lipgloss.NewStyle().
				Foreground(colorGreen)

	styleToolStatusFailed = lipgloss.NewStyle().
				Foreground(colorRed)

	styleToolSummary = lipgloss.NewStyle().
				Foreground(colorFaint).
				Italic(true)

	// Permission prompt styles
	stylePermissionPrompt = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorYellow).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorYellow).
				Padding(1).
				Margin(1)

	stylePermissionYes = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorGreen)

	stylePermissionNo = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorRed)
)
