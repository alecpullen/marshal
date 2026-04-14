package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Soft monochrome palette with subtle blue accent
	colorText       = lipgloss.Color("252") // Soft white
	colorTextDim    = lipgloss.Color("248") // Light gray
	colorTextMuted  = lipgloss.Color("242") // Medium gray
	colorTextFaint  = lipgloss.Color("238") // Dark gray
	colorBg         = lipgloss.Color("235") // Dark background
	colorAccent     = lipgloss.Color("75")  // Soft blue (not aggressive red)
	colorAccentBold = lipgloss.Color("81")  // Brighter blue for emphasis

	// Semantic colors (subtle)
	colorSuccess = lipgloss.Color("114") // Soft green
	colorError   = lipgloss.Color("174") // Soft red
	colorWarn    = lipgloss.Color("180") // Soft yellow

	// User input - subtle blue
	styleUser = lipgloss.NewStyle().
			Foreground(colorAccent)

	// Executor output - clean light gray
	styleExecutor = lipgloss.NewStyle().
			Foreground(colorTextDim)

	// Marshal/thinking - muted gray
	styleMarshal = lipgloss.NewStyle().
			Foreground(colorTextMuted).
			Italic(true)

	// Thinking blocks - distinct with subtle background
	styleThinking = lipgloss.NewStyle().
			Foreground(colorTextMuted).
			Background(colorBg).
			Italic(true).
			Padding(0, 1).
			MarginTop(1).
			MarginBottom(1)

	styleThinkingCollapsed = lipgloss.NewStyle().
				Foreground(colorTextFaint).
				Italic(true)

	// Pass/fail badges - minimal
	stylePass = lipgloss.NewStyle().
			Foreground(colorSuccess).
			Padding(0, 1)

	styleFail = lipgloss.NewStyle().
			Foreground(colorError).
			Padding(0, 1)

	styleLint = lipgloss.NewStyle().
			Foreground(colorWarn).
			Italic(true)

	styleSystem = lipgloss.NewStyle().
			Foreground(colorTextMuted).
			Italic(true)

	styleFaint = lipgloss.NewStyle().
			Foreground(colorTextFaint)

	// Status bar - subtle
	styleStatusBar = lipgloss.NewStyle().
			Foreground(colorTextFaint).
			Background(colorBg).
			Padding(0, 1)

	styleStatusActive = lipgloss.NewStyle().
				Foreground(colorAccent).
				Bold(true)

	// Session indicator styles
	styleStatusInitialized = lipgloss.NewStyle().
				Foreground(colorSuccess).
				Bold(true)

	styleStatusUninitialized = lipgloss.NewStyle().
					Foreground(colorWarn).
					Italic(true)

	styleStatusPartial = lipgloss.NewStyle().
				Foreground(colorTextMuted).
				Italic(true)

	// Viewport content container - adds subtle margins
	styleViewportContent = lipgloss.NewStyle().
				Padding(0, 1) // Small horizontal padding for visual boundary

	// Prompt - blue accent
	stylePromptPrefix = lipgloss.NewStyle().
				Foreground(colorAccent)

	// Input box - minimal border
	styleInputBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorTextFaint).
				PaddingLeft(1)

	// Tool operation styles - very subtle
	styleToolOperation = lipgloss.NewStyle().
				Foreground(colorTextMuted)

	styleToolName = lipgloss.NewStyle().
			Foreground(colorAccent)

	styleToolPath = lipgloss.NewStyle().
			Foreground(colorTextDim)

	styleToolStatusRunning = lipgloss.NewStyle().
				Foreground(colorAccent)

	styleToolStatusReading = lipgloss.NewStyle().
				Foreground(colorTextMuted)

	styleToolStatusWriting = lipgloss.NewStyle().
				Foreground(colorAccent)

	styleToolStatusDone = lipgloss.NewStyle().
				Foreground(colorTextDim)

	styleToolStatusFailed = lipgloss.NewStyle().
				Foreground(colorError)

	styleToolSummary = lipgloss.NewStyle().
				Foreground(colorTextFaint).
				Italic(true)

	// Permission prompt - soft warning
	stylePermissionPrompt = lipgloss.NewStyle().
				Foreground(colorAccentBold).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Padding(1).
				Margin(1)

	stylePermissionYes = lipgloss.NewStyle().
				Foreground(colorSuccess)

	stylePermissionNo = lipgloss.NewStyle().
				Foreground(colorError)

	// Detail level indicator
	styleDetailLevel = lipgloss.NewStyle().
				Foreground(colorAccent)

	// Markdown styles - subtle
	styleMarkdownHeader = lipgloss.NewStyle().
				Foreground(colorAccentBold).
				MarginTop(1)

	styleMarkdownBold = lipgloss.NewStyle().
				Foreground(colorText).
				Bold(true)

	styleMarkdownCode = lipgloss.NewStyle().
				Foreground(colorTextMuted).
				Background(colorBg)

	styleMarkdownCodeBlock = lipgloss.NewStyle().
				Foreground(colorTextMuted).
				Background(colorBg).
				Padding(0, 1)

	styleMarkdownQuote = lipgloss.NewStyle().
				Foreground(colorTextMuted).
				BorderStyle(lipgloss.Border{Left: "│"}).
				BorderForeground(colorTextFaint).
				PaddingLeft(1)

	styleMarkdownList = lipgloss.NewStyle().
				Foreground(colorTextDim)

	// Intro/transition text - bold title style
	styleMarkdownIntro = lipgloss.NewStyle().
				Foreground(colorText).
				Bold(true).
				MarginTop(1).
				MarginBottom(1)

	// Completion/suggestion styles
	styleCompletionBox = lipgloss.NewStyle().
				Foreground(colorTextDim).
				Background(colorBg).
				Padding(0, 1).
				MarginBottom(1)

	styleCompletionHelp = lipgloss.NewStyle().
				Foreground(colorTextFaint).
				Italic(true).
				Padding(0, 1).
				MarginBottom(1)

	styleCompletionSelected = lipgloss.NewStyle().
				Foreground(colorAccentBold).
				Bold(true)
)
