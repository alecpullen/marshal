package accesspanel

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

type ChoiceMsg struct{ Choice session.AccessChoice }
type option struct{ label, mode string }
type Panel struct {
	request          session.AccessRequest
	options          []option
	selected, offset int
	// visible records whether the last render was large enough to show the
	// choices. A hidden or undersized panel must never approve.
	visible bool
}

var _ dock.Panel = (*Panel)(nil)

// Minimum geometry for an actionable panel. Below this the panel cannot show
// its options, so Enter must not turn a hidden selection into a grant.
const (
	minPanelWidth  = 20
	minPanelHeight = 4
)

func New(request session.AccessRequest) *Panel {
	p := &Panel{request: request, options: []option{{label: "Deny"}}}
	switch {
	case request.MissingEditing && request.MissingSystem:
		p.options = append(p.options, option{label: "Act + system access", mode: "act"})
	case request.MissingEditing:
		p.options = append(p.options, option{label: "Act", mode: "act"})
	case request.MissingSystem:
		p.options = append(p.options, option{label: "Allow system access for this conversation"})
	}
	return p
}

func (p *Panel) Request() session.AccessRequest { return p.request }
func (p *Panel) Sizing() dock.Sizing            { return dock.Docked }

func (p *Panel) decision(approved bool) tea.Cmd {
	choice := session.AccessChoice{
		RequestID: p.request.RequestID, SessionID: p.request.SessionID,
		Current: p.request.Current, Approved: approved,
	}
	if approved {
		choice.Mode = p.options[p.selected].mode
	}
	return func() tea.Msg { return ChoiceMsg{Choice: choice} }
}

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "up", "left", "shift+tab":
		p.selected = (p.selected + len(p.options) - 1) % len(p.options)
	case "down", "right", "tab":
		p.selected = (p.selected + 1) % len(p.options)
	case "pgup":
		p.offset = max(0, p.offset-1)
	case "pgdown":
		p.offset++
	case "enter":
		// Deny is always safe. Any grant needs a rendered, actionable panel.
		if p.selected != 0 && !p.visible {
			return nil
		}
		return p.decision(p.selected != 0)
	case "esc":
		return p.decision(false)
	}
	return nil
}

func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
}

// accessLabel renders the full effective tuple as the user saw it: canonical
// mode, every active modifier, and the access revision.
func accessLabel(s session.AccessState) string {
	mode := "Ask"
	switch s.Mode {
	case "act", "edit", "copilot", "auto":
		mode = "Act"
	}
	parts := []string{mode}
	if s.Auto {
		parts = append(parts, "Auto")
	}
	if s.System {
		parts = append(parts, "System")
	}
	parts = append(parts, fmt.Sprintf("rev %d", s.Revision))
	return strings.Join(parts, " · ")
}

func (p *Panel) View(width, maxHeight int) string {
	p.visible = false
	if maxHeight <= 0 || width <= 0 {
		return ""
	}
	pw := layout.PanelWidth(width)
	if pw < minPanelWidth || maxHeight < minPanelHeight {
		return ""
	}
	p.visible = true
	inner := max(1, pw-2)
	title := "Allow system access?"
	requested := "system access; mode unchanged"
	switch {
	case p.request.MissingEditing && p.request.MissingSystem:
		title, requested = "Allow editing and system access?", "Act + system access"
	case p.request.MissingEditing:
		title, requested = "Allow editing?", "Act"
	}
	title = "⚠ " + title
	rows := []string{
		"Current: " + accessLabel(p.request.Current),
		"Request: " + requested + "; for this conversation",
	}
	scope := "Editing uses normal Act; policy and sandbox still apply; sudo is separate."
	if p.request.MissingSystem {
		scope = "System access does not grant sudo; policy and sandbox still apply."
	}
	rows = append(rows, scope)
	actionRows := 1
	if maxHeight >= 10+len(p.options) {
		actionRows = len(p.options)
	}
	detailRows := max(0, maxHeight-1-len(rows)-actionRows)
	detail := clean(p.request.Reason)
	if p.request.Actor != "" || p.request.StepID != 0 {
		attribution := "Requested"
		if p.request.Actor != "" {
			attribution = "From " + clean(p.request.Actor)
		}
		if p.request.StepID != 0 {
			attribution += fmt.Sprintf(" / step %d", p.request.StepID)
		}
		detail = attribution + ". " + detail
	}
	details := strings.Split(ansi.Wrap(detail, inner, "/-:"), "\n")
	p.offset = min(p.offset, max(0, len(details)-detailRows))
	for i := p.offset; i < len(details) && i < p.offset+detailRows; i++ {
		rows = append(rows, theme.MutedStyle().Render(details[i]))
	}
	if actionRows == 1 {
		rows = append(rows, fmt.Sprintf("> [%d/%d] %s", p.selected+1, len(p.options), p.options[p.selected].label))
	} else {
		for i, choice := range p.options {
			marker := "  "
			if i == p.selected {
				marker = "> "
			}
			rows = append(rows, marker+choice.label)
		}
	}
	hints := "↑↓/Tab choose · ↵ decide · Esc deny"
	if detailRows > 0 && len(details) > detailRows {
		hints += " · PgUp/PgDn details"
	}
	return chrome.Frame{Title: title, Hints: hints, Color: theme.Current().StatusWarning, Focused: true}.Render(strings.Join(rows, "\n"), pw+2, maxHeight, theme.Current())
}
