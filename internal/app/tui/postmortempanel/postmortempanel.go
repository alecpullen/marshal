// Package postmortempanel asks the exit postmortem questions inside the TUI.
// It is modal: the dock routes all key input to it while it is open. It asks
// up to two yes/no questions — first whether to run a postmortem at all, then
// whether to include the agent analysis pass — and reports the terminal
// answer as a DoneMsg. The panel performs no session I/O; the host runs the
// chosen path.
package postmortempanel

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/theme"
)

// Result is the panel's terminal answer.
type Result int

const (
	// ResultDeclined: do not run a postmortem; quit.
	ResultDeclined Result = iota
	// ResultExtractOnly: extract and write the report, then quit.
	ResultExtractOnly
	// ResultWithAgent: extract and write the report, then run the agent
	// analysis pass before quitting.
	ResultWithAgent
)

// DoneMsg reports the user's terminal answer to the host. The panel itself
// never touches session state.
type DoneMsg struct{ Result Result }

// Panel is the modal exit-postmortem question.
type Panel struct {
	// stage is 0 at "run a postmortem?" and 1 at "include agent analysis?".
	stage int
	// cursor selects the highlighted option (0 = Yes, 1 = No).
	cursor int
}

var _ dock.Panel = (*Panel)(nil)

// New builds the panel at its first question.
func New() *Panel { return &Panel{} }

// Update handles the choice keys. All other input is swallowed.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	yes := func() tea.Cmd {
		if p.stage == 0 {
			p.stage = 1
			return nil
		}
		return done(ResultWithAgent)
	}
	no := func() tea.Cmd {
		if p.stage == 0 {
			return done(ResultDeclined)
		}
		return done(ResultExtractOnly)
	}
	switch k.String() {
	case "y", "Y":
		return yes()
	case "n", "N", "esc":
		return no()
	case "left", "h", "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "right", "l", "down", "j":
		if p.cursor < 1 {
			p.cursor++
		}
	case "enter":
		if p.cursor == 0 {
			return yes()
		}
		return no()
	}
	return nil
}

// done wraps a terminal answer as a command that delivers a DoneMsg to the
// host model.
func done(result Result) tea.Cmd {
	return func() tea.Msg { return DoneMsg{Result: result} }
}

// Sizing keeps the prompt docked under the default height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// View renders the current question.
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 3 {
		return ""
	}
	pw := min(max(width-2, 30), width)
	question := "Run a session postmortem before exiting?"
	if p.stage == 1 {
		question = "Include agent analysis?"
	}
	var b strings.Builder
	b.WriteString(question + "\n\n")
	if p.stage == 1 {
		b.WriteString("The agent pass runs with system access: it may edit this file at an absolute path outside the workspace.\n\n")
	}
	for i, label := range []string{"Yes", "No"} {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
		if i == p.cursor {
			marker = "▸ "
			style = style.Bold(true)
		}
		b.WriteString(marker + style.Render(label) + "\n")
	}
	return chrome.PanelWithHints("Exit postmortem", "y/n or ↑↓/←→ choose · ↵ confirm · esc no",
		strings.TrimRight(b.String(), "\n"), pw, min(lipgloss.Height(b.String())+1, maxHeight), true, theme.Current())
}
