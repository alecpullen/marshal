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
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

// Row is one cast member to display.
type Row struct {
	Title  string
	Detail string
	Badge  string
	Err    string
	// Warn is a non-blocking caution rendered under the row: something the
	// user should see before starting, but not a reason to refuse.
	Warn string
}

// StartMsg is emitted when the user presses Enter and no row has an error.
type StartMsg struct {
	Strategy string
}

// CancelMsg is emitted when the user presses Esc.
type CancelMsg struct{}

// Panel is a dock.Panel that renders a pre-flight cast list.
type Panel struct {
	title    string
	rows     []Row
	meta     []string
	strategy string
}

var _ dock.Panel = (*Panel)(nil)

// New creates a cast list panel with the given title, cast rows, optional
// metadata lines, and initial execution strategy.
func New(title string, rows []Row, meta []string, strategy string) *Panel {
	return &Panel{title: title, rows: rows, meta: meta, strategy: strategy}
}

// SetVerifyRow updates the verify-gate row's detail after a proposal fills
// in commands, clearing its warning. It is a no-op when the panel has no
// verify-gate row.
func (p *Panel) SetVerifyRow(detail string) {
	for i := len(p.rows) - 1; i >= 0; i-- {
		if p.rows[i].Title == "verify gate" {
			p.rows[i].Warn = ""
			p.rows[i].Detail = detail
			return
		}
	}
}

// VerifyGateUnknown reports whether the verify-gate row is in the unknown
// state (a warning present and no detail), meaning the offer-to-fill flow
// should be offered.
func (p *Panel) VerifyGateUnknown() bool {
	for i := len(p.rows) - 1; i >= 0; i-- {
		if p.rows[i].Title == "verify gate" {
			return p.rows[i].Warn != "" && p.rows[i].Detail == ""
		}
	}
	return false
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
// CancelMsg. Left/Right cycles the execution strategy.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		switch k.String() {
		case "enter":
			if p.blocked() {
				return nil
			}
			return func() tea.Msg { return StartMsg{Strategy: p.strategy} }
		case "esc":
			return func() tea.Msg { return CancelMsg{} }
		case "right":
			p.cycleStrategy(1)
			return nil
		case "left":
			p.cycleStrategy(-1)
			return nil
		}
	}
	return nil
}

var strategies = []string{"agent", "adaptive", "strict"}

func (p *Panel) cycleStrategy(dir int) {
	idx := 0
	for i, s := range strategies {
		if s == p.strategy {
			idx = i
			break
		}
	}
	idx = (idx + dir + len(strategies)) % len(strategies)
	p.strategy = strategies[idx]
}

// Sizing keeps the cast list docked under the default height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

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

	// Strategy row.
	stratRow := Row{
		Title:  "execution strategy",
		Detail: p.strategy,
	}
	rows = append(rows, renderRow(stratRow, inner))

	// Cast rows.
	for _, r := range p.rows {
		rows = append(rows, renderRow(r, inner))
	}

	// Blocked indicator.
	if p.blocked() {
		rows = append(rows, "")
		rows = append(rows, errorStyle().Render("  "+glyph.Warning+" fix errors above before starting"))
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

// renderRow renders a single cast row (or the strategy row) into a string,
// including any Err/Warn continuation lines.
func renderRow(r Row, inner int) string {
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
	if r.Warn != "" {
		line += "\n" + warnStyle().Render("    "+glyph.Warning+" "+r.Warn)
	}
	return line
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

func warnStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning)
}
