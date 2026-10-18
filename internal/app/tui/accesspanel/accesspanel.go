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
}

var _ dock.Panel = (*Panel)(nil)

func New(request session.AccessRequest) *Panel {
	p := &Panel{request: request, options: []option{{label: "Deny"}}}
	if request.MissingEditing {
		for _, mode := range []string{"edit", "copilot", "auto"} {
			label := "Use " + strings.ToUpper(mode[:1]) + mode[1:]
			if request.MissingSystem {
				label = strings.ToUpper(mode[:1]) + mode[1:] + " + system access"
			}
			p.options = append(p.options, option{label: label, mode: mode})
		}
	} else if request.MissingSystem {
		p.options = append(p.options, option{label: "Allow system access for this session"})
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
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight <= 0 || width <= 0 {
		return ""
	}
	pw := layout.PanelWidth(width)
	inner := max(1, pw-2)
	title := "Allow system access?"
	requested := "system access; mode unchanged"
	if p.request.MissingEditing {
		title, requested = "Allow editing?", "editing capability"
		if p.request.MissingSystem {
			title, requested = "Allow editing and system access?", "editing + system access"
		}
	}
	rows := []string{
		fmt.Sprintf("Current: %s; system access: %t", clean(p.request.Current.Mode), p.request.Current.System),
		"Request: " + requested + "; this session only",
	}
	scope := "Interaction mode and tool policy still apply."
	if p.request.MissingSystem {
		scope = "Outside workspace; mode/policy/sandbox still apply; no sudo."
	}
	rows = append(rows, scope)
	actionRows := 1
	if maxHeight >= 10+len(p.options) {
		actionRows = len(p.options)
	}
	detailRows := max(0, maxHeight-1-len(rows)-actionRows-1)
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
	hints := "Arrows/Tab choose; Enter decide; Esc deny"
	if detailRows > 0 && len(details) > detailRows {
		hints += "; PgUp/PgDn details"
	}
	rows = append(rows, theme.MutedStyle().Render(hints))
	return chrome.Panel(title, strings.Join(rows, "\n"), pw, maxHeight, true, theme.Current())
}
