package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/textfield"
)

// sudoAuthModel renders the privileged-command password prompt inline in the
// input area. The command is shown verbatim so the user authorises exactly
// what will run; the password is masked (textinput.EchoPassword, the same
// primitive the provider connect flow uses at connect.go:465). Enter submits,
// Esc denies — a dismissed prompt never executes.
type sudoAuthModel struct {
	sa     *session.PendingSudoAuth
	input  textfield.Model
	width  int
	done   bool
	denied bool
}

func newSudoAuthModel(sa *session.PendingSudoAuth, width int) *sudoAuthModel {
	ti := textfield.New()
	ti.EchoMode = textinput.EchoPassword
	ti.SetVirtualCursor(true)
	ti.Focus()
	ti.Placeholder = "sudo password"
	return &sudoAuthModel{sa: sa, input: ti, width: max(width, 30)}
}

func (m *sudoAuthModel) SetSize(width int) { m.width = max(width, 30) }

func (m *sudoAuthModel) Update(msg tea.Msg) (*sudoAuthModel, tea.Cmd) {
	if m.done {
		return m, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter":
			m.done = true
			return m, nil
		case "esc":
			m.done = true
			m.denied = true
			return m, nil
		}
	}
	// Everything else (including paste) goes to the masked field.
	ti, cmd := m.input.Update(msg)
	m.input = ti
	return m, cmd
}

// Decision returns the response to send. A denied prompt carries no password.
func (m *sudoAuthModel) Decision() session.SudoAuthDecision {
	if m.denied {
		return session.SudoAuthDecision{OK: false}
	}
	return session.SudoAuthDecision{OK: true, Password: []byte(m.input.Value())}
}

func (m *sudoAuthModel) IsDone() bool { return m.done }

func (m *sudoAuthModel) View() string {
	gutter := gutterPrefix(glyph.Question, warningColor)
	indent := strings.Repeat(" ", 3)
	contentWidth := max(m.width-4, 1)

	var b strings.Builder
	title := "Run as root?"
	for j, line := range strings.Split(ansi.Wrap(title, contentWidth, WrapBreakpoints), "\n") {
		if j == 0 {
			b.WriteString(gutter)
		} else {
			b.WriteString(indent)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	// The command is shown verbatim: the user authorises the literal text.
	for _, line := range strings.Split(ansi.Wrap(m.sa.Command, contentWidth, WrapBreakpoints), "\n") {
		b.WriteString(indent)
		b.WriteString(line)
		b.WriteString("\n")
	}
	if m.sa.Reason != "" {
		for _, line := range strings.Split(ansi.Wrap(m.sa.Reason, contentWidth, WrapBreakpoints), "\n") {
			b.WriteString(indent)
			b.WriteString(mutedStyle().Render(line))
			b.WriteString("\n")
		}
	}
	if m.sa.Error != "" {
		for _, line := range strings.Split(ansi.Wrap(m.sa.Error, contentWidth, WrapBreakpoints), "\n") {
			b.WriteString(indent)
			b.WriteString(warningStyle().Render(line))
			b.WriteString("\n")
		}
	}
	if m.sa.AllowUnlock {
		b.WriteString(indent)
		b.WriteString(mutedStyle().Render("unlock is on: sudo's own timestamp grace applies (Defaults timestamp_timeout)"))
		b.WriteString("\n")
	}
	b.WriteString(indent)
	b.WriteString(m.input.View())
	b.WriteString("\n")
	b.WriteString(indent)
	b.WriteString(mutedStyle().Render("[↵] authenticate  [Esc] deny"))
	b.WriteString("\n")
	return chromeRail(b.String(), warningColor)
}

// renderSudoAuthPanel is the fallback static panel shown before the
// interactive model is built (mirrors renderSkillGatePanel's role).
func renderSudoAuthPanel(sa *session.PendingSudoAuth, width int) string {
	m := newSudoAuthModel(sa, width)
	return m.View()
}
