// internal/tui/help.go
// Keyboard shortcuts help overlay. Static content, two-column layout.

package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HelpModel is a static help overlay showing keyboard shortcuts.
type HelpModel struct {
	width  int
	height int
}

func newHelpModel() HelpModel {
	return HelpModel{}
}

func (m HelpModel) Init() tea.Cmd {
	return nil
}

func (m HelpModel) Update(msg tea.Msg) (HelpModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m HelpModel) View(w, h int) string {
	innerW := w - 8
	if innerW < 40 {
		innerW = 40
	}

	// Title
	title := lipgloss.NewStyle().
		Foreground(colTx).
		Bold(true).
		Render("keyboard shortcuts")

	// Build sections
	sections := []struct {
		name  string
		items []string
	}{
		{
			name: "Navigation",
			items: []string{
				"↑/↓      scroll main panel",
				"tab      cycle focus (main → files → tasks)",
				"esc      clear prompt / close overlay",
				"ctrl+c   quit marshal",
			},
		},
		{
			name: "Task Control",
			items: []string{
				"↵        submit task (or queue if running)",
				":cancel  abort running task",
				":retry   retry last completed task",
				":clear   clear log panel",
			},
		},
		{
			name: "Composer",
			items: []string{
				"tab      open from prompt bar",
				"ctrl+↵   submit task",
				"ctrl+f   add pinned files",
				"space    toggle option",
			},
		},
		{
			name: "Overlays",
			items: []string{
				"c        config view",
				"d        diff viewer (sidebar focused)",
				":sessions session browser",
				"?        this help",
			},
		},
		{
			name: "Diff Viewer",
			items: []string{
				"tab      next file",
				"shift+tab prev file",
				"↑/↓      scroll",
				"q/esc    close",
			},
		},
		{
			name: "File Editor",
			items: []string{
				"ctrl+s   save",
				"ctrl+e   open in external editor",
				"esc      close (prompts if unsaved)",
			},
		},
	}

	// Render two columns of sections
	var leftCol, rightCol []string
	mid := len(sections) / 2
	if len(sections)%2 == 1 {
		mid++
	}

	for i, sec := range sections {
		secStr := m.renderSection(sec.name, sec.items, innerW/2-2)
		if i < mid {
			leftCol = append(leftCol, secStr)
		} else {
			rightCol = append(rightCol, secStr)
		}
	}

	leftStr := strings.Join(leftCol, "\n\n")
	rightStr := strings.Join(rightCol, "\n\n")

	// Combine columns
	colW := innerW/2 - 1
	content := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(colW).Render(leftStr),
		"  ",
		lipgloss.NewStyle().Width(colW).Render(rightStr),
	)

	// Assemble full view
	parts := []string{
		title,
		"",
		content,
		"",
		stylePromptHint.Render("q / esc  close"),
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBr3).
		Background(colBg2).
		Padding(1, 2).
		Width(w - 4).
		Height(h - 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
}

func (m HelpModel) renderSection(name string, items []string, w int) string {
	header := lipgloss.NewStyle().
		Foreground(colBl).
		Bold(true).
		Render(name)

	var lines []string
	for _, item := range items {
		// Split on first space to highlight key
		parts := strings.SplitN(item, " ", 2)
		if len(parts) == 2 {
			key := lipgloss.NewStyle().Foreground(colAm).Render(parts[0])
			desc := lipgloss.NewStyle().Foreground(colTx2).Render(parts[1])
			lines = append(lines, key+desc)
		} else {
			lines = append(lines, lipgloss.NewStyle().Foreground(colTx2).Render(item))
		}
	}

	return header + "\n" + strings.Join(lines, "\n")
}
