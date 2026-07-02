package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/session"
)

type Model struct {
	state          *session.State
	input          textinput.Model
	editingCommand bool
}

func New(state *session.State) Model {
	input := textinput.New()
	input.Placeholder = "Ask Marshal..."
	input.Focus()
	input.CharLimit = 4000
	input.Width = 80

	return Model{
		state:          state,
		input:          input,
		editingCommand: false,
	}
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	tc := m.state.PendingApproval()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Always allow Ctrl+C to quit
		if msg.Type == tea.KeyCtrlC {
			m.state.Shutdown()
			return m, tea.Quit
		}

		if tc != nil {
			if m.editingCommand {
				switch msg.Type {
				case tea.KeyEsc:
					m.editingCommand = false
					m.input.Reset()
					m.input.Placeholder = "Ask Marshal..."
					return m, nil
				case tea.KeyEnter:
					value := strings.TrimSpace(m.input.Value())
					if value != "" {
						tc.ResponseChan <- session.UserApprovalDecision{Approved: true, Edited: value}
						m.editingCommand = false
						m.input.Reset()
						m.input.Placeholder = "Ask Marshal..."
						m.state.SetPendingApproval(nil)
					}
					return m, nil
				}
			} else {
				switch msg.String() {
				case "enter":
					tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
					m.state.SetPendingApproval(nil)
					return m, nil
				case "d":
					tc.ResponseChan <- session.UserApprovalDecision{Approved: false}
					m.state.SetPendingApproval(nil)
					return m, nil
				case "a":
					m.state.AddSessionRule(tc.Command)
					tc.ResponseChan <- session.UserApprovalDecision{Approved: true}
					m.state.SetPendingApproval(nil)
					return m, nil
				case "e":
					m.editingCommand = true
					m.input.SetValue(tc.Command)
					m.input.Placeholder = "Edit command..."
					m.input.Focus()
					return m, nil
				case "esc":
					m.state.Shutdown()
					return m, tea.Quit
				}
				// Ignore all other key inputs when approval prompt is shown and not editing
				return m, nil
			}
		} else {
			switch msg.Type {
			case tea.KeyEsc:
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

	if err := m.state.ProviderError(); err != nil {
		fmt.Fprintf(&b, "\nProvider Error\n")
		fmt.Fprintf(&b, "  %s\n", err.Error())
	}

	tc := m.state.PendingApproval()
	if tc != nil {
		fmt.Fprintf(&b, "\n┌── SECURITY APPROVAL REQUIRED ───────────────────────────────────────────┐\n")
		fmt.Fprintf(&b, "│ Agent wants to run:                                                     │\n")
		fmt.Fprintf(&b, "│   %s\n", tc.Command)
		fmt.Fprintf(&b, "│                                                                         │\n")
		fmt.Fprintf(&b, "│ Reason:                                                                 │\n")
		fmt.Fprintf(&b, "│   %s\n", tc.Reason)
		fmt.Fprintf(&b, "│                                                                         │\n")
		fmt.Fprintf(&b, "│ Risk Level: %s\n", tc.Risk)
		fmt.Fprintf(&b, "└─────────────────────────────────────────────────────────────────────────┘\n")
		if m.editingCommand {
			fmt.Fprintf(&b, "Edit command and press [Enter] to run, [Esc] to cancel\n")
		} else {
			fmt.Fprintf(&b, "[Enter] Approve  [d] Deny  [e] Edit  [a] Always Allow in this Session\n")
		}
	}

	if tc == nil || m.editingCommand {
		fmt.Fprintf(&b, "\n%s\n", m.input.View())
	}

	return b.String()
}
