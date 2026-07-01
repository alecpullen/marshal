package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/session"
)

type Model struct {
	state *session.State
	input textinput.Model
}

func New(state *session.State) Model {
	input := textinput.New()
	input.Placeholder = "Ask Marshal..."
	input.Focus()
	input.CharLimit = 4000
	input.Width = 80

	return Model{
		state: state,
		input: input,
	}
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.state.Shutdown()
			return m, tea.Quit
		case tea.KeyEnter:
			value := strings.TrimSpace(m.input.Value())
			if value == "" {
				return m, nil
			}
			m.state.AddMessage(session.RoleUser, value)
			m.input.Reset()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Marshal\n")
	fmt.Fprintf(&b, "Status: project=%s cwd=%s local-only=%t\n\n",
		m.state.Config.Project.Name,
		m.state.WorkingDir,
		!m.state.Config.Privacy.RemoteProvidersAllowed,
	)

	fmt.Fprintf(&b, "Transcript\n")
	messages := m.state.Messages()
	if len(messages) == 0 {
		fmt.Fprintf(&b, "  No messages yet.\n")
	}
	for _, message := range messages {
		fmt.Fprintf(&b, "  %s: %s\n", message.Role, message.Content)
	}

	fmt.Fprintf(&b, "\nStreaming Output\n")
	fmt.Fprintf(&b, "  No model output yet.\n")
	fmt.Fprintf(&b, "\nCommand Palette\n")
	fmt.Fprintf(&b, "  No commands available yet.\n")
	fmt.Fprintf(&b, "\nTool Log\n")
	fmt.Fprintf(&b, "  No tool calls yet.\n")
	fmt.Fprintf(&b, "\nDiff\n")
	fmt.Fprintf(&b, "  No patch proposed.\n")
	fmt.Fprintf(&b, "\n%s\n", m.input.View())

	return b.String()
}
