package steerpanel

import (
	"crypto/rand"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

type SubmitMsg struct{ ComposerID, SessionID, TurnID, Text string }
type CloseMsg struct{ ComposerID string }

type Panel struct {
	id, sessionID, turnID string
	input                 textarea.Model
}

var _ dock.Panel = (*Panel)(nil)

func New(sessionID, turnID string) *Panel {
	input := textarea.New()
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.CharLimit = 0 // validate the complete expanded draft in the parent
	input.Placeholder = "Steer the current turn..."
	input.KeyMap.InsertNewline.SetKeys("shift+enter")
	input.Focus()
	return &Panel{id: rand.Text(), sessionID: sessionID, turnID: turnID, input: input}
}

func (p *Panel) ID() string          { return p.id }
func (p *Panel) Text() string        { return p.input.Value() }
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter", "alt+enter":
			out := SubmitMsg{ComposerID: p.id, SessionID: p.sessionID, TurnID: p.turnID, Text: p.Text()}
			return func() tea.Msg { return out }
		case "esc":
			id := p.id
			return func() tea.Msg { return CloseMsg{ComposerID: id} }
		case "ctrl+c":
			return nil // parent retains its existing global interrupt behavior
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func (p *Panel) View(width, maxHeight int) string {
	if width <= 0 || maxHeight <= 0 {
		return ""
	}
	th := theme.Current()
	pw := layout.PanelWidth(width)
	text := lipgloss.NewStyle().Foreground(th.FGDefault)
	muted := lipgloss.NewStyle().Foreground(th.FGMuted)
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Text, styles.Blurred.Text = text, text
	styles.Focused.Placeholder, styles.Blurred.Placeholder = muted, muted
	styles.Focused.Prompt, styles.Blurred.Prompt = lipgloss.NewStyle(), lipgloss.NewStyle()
	styles.Focused.CursorLine, styles.Blurred.CursorLine = text, text
	p.input.SetStyles(styles)
	p.input.SetWidth(max(1, pw-2))
	p.input.SetHeight(max(1, maxHeight-4))
	rows := []string{
		"Target: parent conversation",
		"Stops foreground work; background tasks continue.",
		p.input.View(),
		muted.Render("Enter send now; Shift+Enter newline; Esc back"),
	}
	return chrome.Panel("Send now", strings.Join(rows, "\n"), pw, maxHeight, true, th)
}
