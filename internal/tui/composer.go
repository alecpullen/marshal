// internal/tui/composer.go
// Task composer modal overlay with multi-line editor, file chips, and options.

package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ComposerModel is the task composer modal overlay.
type ComposerModel struct {
	// Core components
	textarea textarea.Model

	// State
	value      string
	submitted  bool
	cancelled  bool
	width      int
	height     int

	// File chips (pinned context files)
	pinnedFiles []fileChip
	selectedChip int // -1 = none selected

	// Options
	options     []composerOption
	selectedOpt int // -1 = none selected

	// Focus state: 0 = textarea, 1 = chips row, 2 = options row
	focus int
}

type fileChip struct {
	path     string
	language string // for icon/color
}

type composerOption struct {
	name    string
	key     string
	value   bool
	display string
}

func newComposerModel() ComposerModel {
	ta := textarea.New()
	ta.Placeholder = "Describe the task..."
	ta.SetWidth(60)
	ta.SetHeight(5)
	ta.Focus()

	return ComposerModel{
		textarea:    ta,
		pinnedFiles: []fileChip{},
		focus:       0,
		options: []composerOption{
			{name: "max rounds", key: "rounds", value: true, display: "3 rounds"},
			{name: "branch isolation", key: "branch", value: true, display: "✓ branch"},
			{name: "auto commit", key: "commit", value: true, display: "✓ commit"},
			{name: "dry run", key: "dryrun", value: false, display: "dry run"},
		},
		selectedChip: -1,
		selectedOpt:  -1,
	}
}

func (m *ComposerModel) SetValue(v string) {
	m.textarea.SetValue(v)
	m.textarea.Focus()
	m.focus = 0
}

func (m ComposerModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m ComposerModel) Update(msg tea.Msg) (ComposerModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+enter":
			m.value = m.textarea.Value()
			if m.value != "" {
				m.submitted = true
			}
			return m, nil

		case "esc":
			m.value = m.textarea.Value()
			m.cancelled = true
			return m, nil

		case "tab":
			// Cycle focus: textarea -> chips -> options
			m.focus = (m.focus + 1) % 3
			if m.focus == 0 {
				m.textarea.Focus()
				m.selectedChip = -1
				m.selectedOpt = -1
			} else if m.focus == 1 {
				m.textarea.Blur()
				if len(m.pinnedFiles) > 0 {
					m.selectedChip = 0
				} else {
					m.selectedChip = -1
				}
				m.selectedOpt = -1
			} else {
				m.textarea.Blur()
				m.selectedChip = -1
				m.selectedOpt = 0
			}
			return m, nil

		case "shift+tab":
			// Cycle focus backwards
			m.focus = (m.focus + 2) % 3
			if m.focus == 0 {
				m.textarea.Focus()
				m.selectedChip = -1
				m.selectedOpt = -1
			} else if m.focus == 1 {
				m.textarea.Blur()
				if len(m.pinnedFiles) > 0 {
					m.selectedChip = 0
				}
				m.selectedOpt = -1
			} else {
				m.textarea.Blur()
				m.selectedChip = -1
				m.selectedOpt = 0
			}
			return m, nil

		case "space":
			if m.focus == 2 && m.selectedOpt >= 0 {
				// Toggle option
				opt := &m.options[m.selectedOpt]
				opt.value = !opt.value
				// Update display
				if opt.key == "rounds" {
					if opt.value {
						opt.display = "3 rounds"
					} else {
						opt.display = "1 round"
					}
				} else if opt.key == "dryrun" {
					if opt.value {
						opt.display = "✓ dry run"
					} else {
						opt.display = "dry run"
					}
				} else {
					if opt.value {
						opt.display = "✓ " + opt.name
					} else {
						opt.display = opt.name
					}
				}
			}
			return m, nil

		case "left":
			if m.focus == 1 && m.selectedChip > 0 {
				m.selectedChip--
			}
			if m.focus == 2 && m.selectedOpt > 0 {
				m.selectedOpt--
			}
			return m, nil

		case "right":
			if m.focus == 1 && m.selectedChip < len(m.pinnedFiles)-1 {
				m.selectedChip++
			}
			if m.focus == 2 && m.selectedOpt < len(m.options)-1 {
				m.selectedOpt++
			}
			return m, nil

		case "x":
			// Remove selected chip
			if m.focus == 1 && m.selectedChip >= 0 && m.selectedChip < len(m.pinnedFiles) {
				m.pinnedFiles = append(m.pinnedFiles[:m.selectedChip], m.pinnedFiles[m.selectedChip+1:]...)
				if m.selectedChip >= len(m.pinnedFiles) {
					m.selectedChip = len(m.pinnedFiles) - 1
				}
				if len(m.pinnedFiles) == 0 {
					m.selectedChip = -1
				}
			}
			return m, nil

		case "ctrl+f":
			// Signal to app.go to open fuzzy picker
			return m, func() tea.Msg { return OpenFuzzyMsg{} }
		}
	}

	// Update textarea if focused
	if m.focus == 0 {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m ComposerModel) View(w, h int) string {
	m.width = w
	m.height = h

	// Modal container style
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBr3).
		Background(colBg2).
		Padding(1, 2).
		Width(min(70, w-4))

	// Title
	title := lipgloss.NewStyle().
		Foreground(colTx).
		Bold(true).
		MarginBottom(1).
		Render("task")

	// Textarea view
	taView := m.textarea.View()

	// File chips row
	chipsRow := m.renderChipsRow()

	// Options row
	optionsRow := m.renderOptionsRow()

	// Run button
	buttonStyle := lipgloss.NewStyle().
		Background(colBl).
		Foreground(colBg).
		Padding(0, 2).
		Bold(true)

	if m.focus == 2 && m.selectedOpt == -1 {
		// Button is focused
		buttonStyle = buttonStyle.Border(lipgloss.RoundedBorder()).
			BorderForeground(colTx)
	}

	runButton := buttonStyle.Render("[run task ↵]")

	// Assemble content
	content := lipgloss.JoinVertical(lipgloss.Left,
		title,
		taView,
		"",
		chipsRow,
		"",
		optionsRow,
		"",
		runButton,
	)

	// Render modal centered
	modal := modalStyle.Render(content)
	return lipgloss.Place(w, h,
		lipgloss.Center, lipgloss.Center,
		modal,
	)
}

func (m ComposerModel) renderChipsRow() string {
	if len(m.pinnedFiles) == 0 {
		// Empty state with add button
		addStyle := lipgloss.NewStyle().
			Foreground(colTx3).
			Italic(true)
		if m.focus == 1 {
			addStyle = addStyle.Foreground(colBl)
		}
		return addStyle.Render("📎 ctrl+f to add files")
	}

	var chips []string
	for i, chip := range m.pinnedFiles {
		// Truncate path
		display := chip.path
		if len(display) > 25 {
			display = "..." + display[len(display)-22:]
		}

		style := lipgloss.NewStyle().
			Background(colBg3).
			Foreground(colTx2).
			Padding(0, 1).
			MarginRight(1)

		if m.focus == 1 && i == m.selectedChip {
			style = style.Background(colBl).Foreground(colBg)
		}

		langIcon := m.languageIcon(chip.language)
		chipStr := fmt.Sprintf("%s %s ×", langIcon, display)
		chips = append(chips, style.Render(chipStr))
	}

	// Add button
	addStyle := lipgloss.NewStyle().
		Foreground(colTx3).
		Padding(0, 1)
	if m.focus == 1 && m.selectedChip == -1 {
		addStyle = addStyle.Background(colBl).Foreground(colBg)
	}

	chips = append(chips, addStyle.Render("+ add"))

	return lipgloss.JoinHorizontal(lipgloss.Left, chips...)
}

func (m ComposerModel) renderOptionsRow() string {
	var opts []string
	for i, opt := range m.options {
		style := lipgloss.NewStyle().
			Foreground(colTx3).
			Padding(0, 1)

		if m.focus == 2 && i == m.selectedOpt {
			style = style.Foreground(colBl).Underline(true)
		}

		if opt.value {
			style = style.Foreground(colTx)
		}

		opts = append(opts, style.Render(opt.display))
	}

	return lipgloss.JoinHorizontal(lipgloss.Left, opts...)
}

func (m ComposerModel) languageIcon(lang string) string {
	switch lang {
	case "go":
		return "🐹"
	case "ts", "tsx":
		return "📘"
	case "js", "jsx":
		return "📒"
	case "py":
		return "🐍"
	case "rs":
		return "🦀"
	default:
		return "📄"
	}
}

func (m ComposerModel) StatusSegments() []string {
	fileCount := len(m.pinnedFiles)
	fileStr := fmt.Sprintf("%d file pinned", fileCount)
	if fileCount != 1 {
		fileStr = fmt.Sprintf("%d files pinned", fileCount)
	}

	// Estimate tokens (rough: ~200 tokens per file + task length/4)
	tokenEst := fileCount*200 + len(m.textarea.Value())/4
	tokenStr := fmt.Sprintf("~%d tok context", tokenEst)

	return []string{fileStr, tokenStr}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
