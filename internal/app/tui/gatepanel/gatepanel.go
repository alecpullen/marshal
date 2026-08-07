// Package gatepanel renders the question a plan-run subagent raised and
// collects the human's answer.
//
// It exists to replace a three-surface arrangement: the question was
// printed into the transcript, repeated as a warning row in the run panel,
// and answered through an input box that looked idle but was silently
// modal — enter resumed the run, esc killed it, and neither was advertised
// anywhere.
package gatepanel

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
)

// AnswerMsg is emitted when the user submits a non-empty answer.
type AnswerMsg struct{ Text string }

// StopMsg is emitted when the user presses Esc to abandon the run.
type StopMsg struct{}

// reportLines caps how much of the implementer's report the panel shows.
// The question is the point; the report is supporting evidence.
const reportLines = 6

// Panel is a dock.Panel that asks the human a subagent's question.
type Panel struct {
	gate  session.SDDGate
	input textfield.Model
}

var _ dock.Panel = (*Panel)(nil)

// New creates a gate panel for the open gate.
func New(g session.SDDGate) *Panel {
	ti := textfield.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	return &Panel{gate: g, input: ti}
}

// Update handles key events. Enter submits a non-empty answer; Esc stops
// the run. Everything else goes to the answer field.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		switch k.String() {
		case "enter":
			answer := strings.TrimSpace(p.input.Value())
			if answer == "" {
				return nil
			}
			return func() tea.Msg { return AnswerMsg{Text: answer} }
		case "esc":
			return func() tea.Msg { return StopMsg{} }
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

// Sizing keeps the gate panel under the default docked height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// View renders the question, its context, and the answer field.
func (p *Panel) View(width, maxHeight int) string {
	th := theme.Current()
	pw := layout.PanelWidth(width)
	inner := pw - 3
	if inner < 1 {
		inner = 1
	}

	var rows []string

	// The question, first and unmissable.
	for _, line := range strings.Split(ansi.Wrap(p.gate.Question, inner, ""), "\n") {
		rows = append(rows, "  "+line)
	}

	// The report, as supporting evidence, capped and muted.
	if body := strings.TrimSpace(p.gate.Report); body != "" && maxHeight > 8 {
		rows = append(rows, "")
		lines := strings.Split(body, "\n")
		if len(lines) > reportLines {
			lines = lines[:reportLines]
		}
		for _, line := range lines {
			rows = append(rows, mutedStyle().Render("  "+ansi.Truncate(line, inner, "…")))
		}
	}

	rows = append(rows, "")
	rows = append(rows, "  "+p.input.View())

	listH := maxHeight - 1
	if listH < 1 {
		listH = 1
	}
	body := chrome.ClipLines(rows, len(rows)-1, listH, th)

	ph := min(lipgloss.Height(body)+1, maxHeight)
	return chrome.PanelWithHints(p.title(), "↵ answer · esc stop the run", body, pw, ph, true, th)
}

// title names the asking task so the question has a subject.
func (p *Panel) title() string {
	if p.gate.TaskTitle != "" {
		return fmt.Sprintf("Task %d needs an answer — %s", p.gate.TaskN, p.gate.TaskTitle)
	}
	return fmt.Sprintf("Task %d needs an answer", p.gate.TaskN)
}

func mutedStyle() lipgloss.Style {
	if _, mono := theme.Current().FGDefault.(lipgloss.NoColor); mono {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}
