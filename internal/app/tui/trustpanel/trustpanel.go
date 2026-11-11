// Package trustpanel asks the folder-trust question inside the TUI. It is
// modal: the dock routes all key input to it while it is open. Declining
// closes the panel and the session proceeds with the phase-1 config;
// trusting quits the program so the app can reinitialise with the project
// config in force. Trusting quits only when the prompt was not in place: an
// in-place panel (a daemon session, P4 D8) sends its choice to the model,
// which makes the call and lets the running program carry on.
package trustpanel

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/theme"
)

// ClosedMsg asks the host to close the panel (decline path).
type ClosedMsg struct{}

// DecidedMsg asks the host to send the decision the user chose and close the
// panel. Only an in-place panel sends it.
type DecidedMsg struct {
	Dir      string
	Decision acpwire.TrustDecision
}

var decisions = []acpwire.TrustDecision{
	acpwire.TrustPermanent,
	acpwire.TrustSession,
	acpwire.TrustDecline,
}

var labels = []string{
	"Trust permanently (saved for this path)",
	"Trust this session only",
	"Don't trust (user + default config only)",
}

// Panel is the modal trust question.
type Panel struct {
	workingDir string
	decide     func(acpwire.TrustDecision)
	cursor     int
	// inPlace is set by NewInPlace: a choice calls nothing and sends
	// DecidedMsg instead.
	inPlace bool
}

var _ dock.Panel = (*Panel)(nil)

// New builds the panel. decide is invoked synchronously on every terminal
// choice (keys 1-3, Enter, or Esc) before the returned command runs.
func New(workingDir string, decide func(acpwire.TrustDecision)) *Panel {
	return &Panel{workingDir: workingDir, decide: decide}
}

// NewInPlace builds the panel for a prompt the agent applies in place: a
// choice calls nothing and sends DecidedMsg, so the program keeps running.
func NewInPlace(workingDir string) *Panel {
	return &Panel{workingDir: workingDir, inPlace: true}
}

// Update handles the choice keys. All other input is swallowed.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	choose := func(d acpwire.TrustDecision) tea.Cmd {
		if p.inPlace {
			// The model makes the call off the Update goroutine: a config
			// reload over the relay can take seconds (P4 D8, decision 7).
			dir, decision := p.workingDir, d
			return func() tea.Msg { return DecidedMsg{Dir: dir, Decision: decision} }
		}
		p.decide(d)
		if d == acpwire.TrustDecline {
			return func() tea.Msg { return ClosedMsg{} }
		}
		return tea.Quit
	}
	switch k.String() {
	case "1":
		return choose(acpwire.TrustPermanent)
	case "2":
		return choose(acpwire.TrustSession)
	case "3", "esc":
		return choose(acpwire.TrustDecline)
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(decisions)-1 {
			p.cursor++
		}
	case "enter":
		return choose(decisions[p.cursor])
	}
	return nil
}

// Sizing keeps the prompt docked under the default height cap.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// View renders the question.
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 3 {
		return ""
	}
	pw := min(max(width-2, 30), width)
	var b strings.Builder
	fmt.Fprintf(&b, "This project (%s) has a .marshal/config.toml.\n", p.workingDir)
	b.WriteString("It can change policy rules, commands, and model selection.\n\n")
	for i, label := range labels {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
		if i == p.cursor {
			marker = "▸ "
			style = style.Bold(true)
		}
		fmt.Fprintf(&b, "%s%s\n", marker, style.Render(fmt.Sprintf("%d) %s", i+1, label)))
	}
	return chrome.PanelWithHints("Trust this project?", "↑↓ choose · ↵ confirm · esc decline",
		strings.TrimRight(b.String(), "\n"), pw, min(lipgloss.Height(b.String())+1, maxHeight), true, theme.Current())
}
