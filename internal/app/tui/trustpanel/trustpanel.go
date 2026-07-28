// Package trustpanel asks the folder-trust question inside the TUI. It is
// modal: the dock routes all key input to it while it is open. Declining
// closes the panel and the session proceeds with the phase-1 config;
// trusting quits the program so the app can reinitialise with the project
// config in force.
package trustpanel

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/theme"
	"marshal/internal/trust"
)

// ClosedMsg asks the host to close the panel (decline path).
type ClosedMsg struct{}

var decisions = []trust.Decision{
	trust.DecisionTrustPermanent,
	trust.DecisionTrustSession,
	trust.DecisionDontTrust,
}

var labels = []string{
	"Trust permanently (saved for this path)",
	"Trust this session only",
	"Don't trust (user + default config only)",
}

// Panel is the modal trust question.
type Panel struct {
	workingDir string
	decide     func(trust.Decision)
	cursor     int
}

var _ dock.Panel = (*Panel)(nil)

// New builds the panel. decide is invoked synchronously on every terminal
// choice (keys 1-3, Enter, or Esc) before the returned command runs.
func New(workingDir string, decide func(trust.Decision)) *Panel {
	return &Panel{workingDir: workingDir, decide: decide}
}

// Update handles the choice keys. All other input is swallowed.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	choose := func(d trust.Decision) tea.Cmd {
		p.decide(d)
		if d == trust.DecisionDontTrust {
			return func() tea.Msg { return ClosedMsg{} }
		}
		return tea.Quit
	}
	switch k.String() {
	case "1":
		return choose(trust.DecisionTrustPermanent)
	case "2":
		return choose(trust.DecisionTrustSession)
	case "3", "esc":
		return choose(trust.DecisionDontTrust)
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
	fmt.Fprintf(&b, "This project has a .marshal/config.toml.\n")
	b.WriteString("It can change providers, policy rules, and commands.\n\n")
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
