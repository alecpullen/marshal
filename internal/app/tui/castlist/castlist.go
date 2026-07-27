// Package castlist renders the pre-flight cast list shown before /sdd and
// /swarm runs. It has no role-resolution logic; it only renders rows produced
// by routing.Cast.
package castlist

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

// Row is one cast member to display.
type Row struct {
	Title  string
	Detail string
	Badge  string
	Err    string
}

// StartMsg is emitted when the user presses Enter and no row has an error.
type StartMsg struct{}

// CancelMsg is emitted when the user presses Esc.
type CancelMsg struct{}

// Panel is a dock.Panel that renders a pre-flight cast list.
type Panel struct {
	title string
	rows  []Row
	meta  []string
}

var _ dock.Panel = (*Panel)(nil)

// New creates a cast list panel with the given title, cast rows, and
// optional metadata lines (e.g. run mode, model info).
func New(title string, rows []Row, meta []string) *Panel {
	return &Panel{title: title, rows: rows, meta: meta}
}

// blocked reports whether any row has a non-empty Err.
func (p *Panel) blocked() bool {
	for _, r := range p.rows {
		if r.Err != "" {
			return true
		}
	}
	return false
}

// Update handles key events. Enter emits StartMsg when unblocked; Esc emits
// CancelMsg.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		switch k.String() {
		case "enter":
			if p.blocked() {
				return nil
			}
			return func() tea.Msg { return StartMsg{} }
		case "esc":
			return func() tea.Msg { return CancelMsg{} }
		}
	}
	return nil
}

// View renders the cast list inside the dock height budget.
func (p *Panel) View(width, maxHeight int) string {
	th := theme.Current()

	pw := layout.PanelWidth(width)
	inner := pw - 3

	if maxHeight < 3 {
		return mutedStyle().Render("Cast list")
	}

	var rows []string

	// Metadata lines.
	for _, line := range p.meta {
		rows = append(rows, mutedStyle().Render(line))
	}
	if len(p.meta) > 0 {
		rows = append(rows, "")
	}

	// Cast rows.
	for _, r := range p.rows {
		right := ""
		if r.Detail != "" {
			right = detailStyle().Render(r.Detail)
		}
		if r.Badge != "" {
			right += " " + badgeStyle().Render(r.Badge)
		}
		rightWidth := lipgloss.Width(right)

		titleBudget := inner - rightWidth - 1
		if titleBudget < 1 {
			titleBudget = 1
		}
		label := r.Title
		if ansi.StringWidth(label) > titleBudget {
			label = ansi.Truncate(label, titleBudget, "…")
		}

		gap := inner - lipgloss.Width(label) - rightWidth
		if gap < 1 {
			gap = 1
		}

		line := "  " + label + strings.Repeat(" ", gap) + right

		if r.Err != "" {
			line += "\n" + errorStyle().Render("    "+r.Err)
		}

		rows = append(rows, line)
	}

	// Blocked indicator.
	if p.blocked() {
		rows = append(rows, "")
		rows = append(rows, errorStyle().Render("  ⚠ fix errors above before starting"))
	}

	listH := maxHeight - 1
	if listH < 1 {
		listH = 1
	}
	body := chrome.ClipLines(rows, 0, listH, th)

	hints := "↵ start"
	if p.blocked() {
		hints = "↵ blocked"
	}
	hints += " · Esc cancel"

	ph := min(lipgloss.Height(body)+1, maxHeight)
	return chrome.PanelWithHints(p.title, hints, body, pw, ph, true, th)
}

func isMono() bool {
	_, ok := theme.Current().FGDefault.(lipgloss.NoColor)
	return ok
}

func mutedStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}

func detailStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}

func badgeStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusInfo)
}

func errorStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError)
}
