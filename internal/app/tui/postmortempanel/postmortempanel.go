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
	"github.com/charmbracelet/x/ansi"

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
	// agentVisible reports that the current agent-stage question, its complete
	// disclosure and both choices were rendered at the current geometry. The
	// affirmative answer is refused until they have been: the analysis pass
	// runs with Act + System, so the user must see what it may reach first.
	agentVisible bool
}

var _ dock.Panel = (*Panel)(nil)

// New builds the panel at its first question.
func New() *Panel { return &Panel{} }

// InvalidateVisibility prevents affirmative agent consent until the agent
// question is rendered again at the current geometry. It never changes the
// selected choice or the result, and is idempotent.
func (p *Panel) InvalidateVisibility() { p.agentVisible = false }

// Update handles the choice keys. All other input is swallowed.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	yes := func() tea.Cmd {
		if p.stage == 0 {
			p.stage = 1
			// The agent question has not been seen yet, whatever was on screen
			// a moment ago.
			p.agentVisible = false
			return nil
		}
		if !p.agentVisible {
			// The disclosure is not on screen at this geometry: leave the
			// question open rather than consent to an unseen capability. No
			// and Esc still answer.
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
//
// Both answers are part of the question, so they are the LAST content the panel
// gives up: when the dock's height budget is tight the explanatory lines are
// dropped first and the choices stay visible and reachable. A question whose
// answers scrolled off the bottom would be a dead end — the user could not
// exit or decline from the keyboard.
//
// The agent stage is the exception. Its disclosure is mandatory, so the panel
// renders the question, both disclosure lines and both choices together or not
// at all: at a geometry that cannot hold all five rows it shows a resize
// instruction instead and leaves the affirmative answer refused (see Update).
// No and Esc stay reachable at every size, so the question is never a dead end.
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 3 {
		return ""
	}
	pw := min(max(width-2, 30), width)
	// The frame's two border columns and their one-cell gutters take four
	// cells; the panel width reserves two of them, so the body keeps pw-2.
	inner := max(pw-2, 1)
	// The chrome header takes one row, so the body must fit in maxHeight-1.
	contentBudget := maxHeight - 1
	answers := p.answerRows()
	if p.stage == 0 {
		// Stage zero discloses nothing beyond the question itself.
		p.agentVisible = false
		rows := []string{"Run a session postmortem before exiting?", "", answers[0], answers[1]}
		if len(rows) > contentBudget {
			// Even the compact form does not fit — the dock's floor should make
			// this unreachable — so keep the answers and the question, in that
			// order of importance.
			rows = answers
		}
		return p.render(rows, pw, maxHeight)
	}
	rows := []string{
		"Include agent analysis?",
		"Agent analysis has system access and editing approval.",
		"It can access and edit files outside this workspace.",
		answers[0],
		answers[1],
	}
	if rowsFit(rows, inner, contentBudget) {
		p.agentVisible = true
		return p.render(rows, pw, maxHeight)
	}
	// The complete disclosure and the choices do not fit together, so nothing
	// is disclosed: replace it with an instruction that says how to get it and
	// keep the safe answer on screen. The instruction shortens when the width
	// is what is missing, because it must survive the same budget it reports.
	p.agentVisible = false
	for _, hint := range []string{
		"Resize the terminal to read the full disclosure.",
		"Resize to read the full disclosure.",
	} {
		for _, shrunk := range [][]string{
			{hint, "", answers[0], answers[1]},
			{hint, answers[0], answers[1]},
		} {
			if rowsFit(shrunk, inner, contentBudget) {
				return p.render(shrunk, pw, maxHeight)
			}
		}
	}
	return p.render(answers, pw, maxHeight)
}

// answerRows renders the Yes/No choices, marking the selected one.
func (p *Panel) answerRows() []string {
	answers := make([]string, 0, 2)
	for i, label := range []string{"Yes", "No"} {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
		if i == p.cursor {
			marker = "▸ "
			style = style.Bold(true)
		}
		answers = append(answers, marker+style.Render(label))
	}
	return answers
}

// rowsFit reports whether every row is inside the frame's inner width and the
// whole set fits the body's row budget. Widths are physical cells, not bytes:
// a styled row carries escape sequences that occupy no column.
func rowsFit(rows []string, inner, budget int) bool {
	if len(rows) > budget {
		return false
	}
	for _, row := range rows {
		if ansi.StringWidth(row) > inner {
			return false
		}
	}
	return true
}

// render frames rows at the panel's docked width and height cap.
func (p *Panel) render(rows []string, pw, maxHeight int) string {
	content := strings.Join(rows, "\n")
	return chrome.PanelWithHints("Exit postmortem", "y/n or ↑↓/←→ choose · ↵ confirm · esc no",
		content, pw, min(lipgloss.Height(content)+1, maxHeight), true, theme.Current())
}
