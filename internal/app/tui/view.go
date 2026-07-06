package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

	rows := []string{m.renderTranscriptFrame()}
	if panel := renderSwarmPanel(m.state.SwarmProgress(), m.spinnerFrame, m.width); panel != "" {
		rows = append(rows, panel)
	}
	rows = append(rows, m.renderInputArea(), m.renderStatusLine(m.width))
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) renderTranscriptFrame() string {
	return transcriptFrameStyle.
		Width(max(m.width-2, 1)).
		Height(max(m.viewport.Height, 1)).
		Render(m.viewport.View())
}

func (m Model) renderInputArea() string {
	inputInnerWidth := max(m.width-4, 1)

	rows := make([]string, 0, 4)

	if tc := m.state.PendingApproval(); tc != nil {
		if m.editingCommand {
			editLine := lipgloss.JoinHorizontal(
				lipgloss.Top,
				inputPromptStyle.Render("❯ "),
				m.input.View(),
			)
			rows = append(rows, editLine)
		} else {
			rows = append(rows, renderApprovalPanel(tc, inputInnerWidth))
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
	// Pad every interior row to the box's inner width with the panel
	// background so the dark fill reaches both vertical borders and no
	// terminal-background gap is visible inside the box.
	content = fillRowsToWidth(content, inputInnerWidth)
	return inputBoxStyle.Render(content)
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
	return lipgloss.NewStyle().
		Width(available).
		Background(panelBgColor).
		Render(statusBusyStyle.Render(truncateRunes(label, available)))
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
			item = mutedStyle.Copy().Background(panelBgColor).Render(item)
		}
		parts = append(parts, item)
	}
	line := strings.Join(parts, "  ")
	return lipgloss.NewStyle().
		Width(available).
		Background(panelBgColor).
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

// contentWidth returns the visible width of a styled line after dropping any
// trailing whitespace, i.e. the width of its real glyphs.
func contentWidth(line string) int {
	stripped := ansiRe.ReplaceAllString(line, "")
	trimmed := strings.TrimRight(stripped, " ")
	return ansi.StringWidth(trimmed)
}

// fillRowsToWidth pads every line of a multi-line block to width with
// panelBg-styled spaces, first stripping any unstyled trailing pad the
// children (notably the textarea) inserted so the fill is the only trailing
// whitespace and therefore carries the input box's background colour.
//
// Bubbles' textarea pads short lines to its SetWidth via its embedded
// viewport with raw, unstyled spaces, and its placeholder styling closes the
// SGR with resets just before that pad. Neither the textarea's Base
// background nor any enclosing lipgloss style can re-colour those naked
// cells because the resets terminate the active background mid-line, so they
// would render as the terminal's default background inside the input box.
// Stripping the trailing pad and re-emitting a per-space-styled fill is the
// only way to guarantee the dark fill reaches both vertical borders.
func fillRowsToWidth(block string, width int) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		w := contentWidth(line)
		if w >= width {
			continue
		}
		kept := ansi.Truncate(line, w, "")
		pad := strings.Repeat(" ", width-w)
		lines[i] = kept + inputFillStyle.Render(pad)
	}
	return strings.Join(lines, "\n")
}
