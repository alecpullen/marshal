// Package inspector is the full-screen panel behind `i` in browse mode: every
// recorded fact about one step or tool call, in sections that scroll
// independently. It renders a Detail value built by the tui package and
// imports nothing from session, so the view model stays the only place that
// knows how a step or an audit event is shaped.
package inspector

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

// Detail is everything the inspector shows.
type Detail struct {
	Title    string // "step 3.2 · tool 1/1" or "step 14"
	Subject  string // command / tool subject / step headline
	Fields   []Field
	Sections []Section // "args", "output", "narration", "thinking", "diff"
}

// Field is one ordered key/value row.
type Field struct{ Key, Value string }

// Section is a named block of text that scrolls on its own.
type Section struct{ Name, Body string }

// ClosedMsg asks the host to close the inspector and return to browse mode.
type ClosedMsg struct{}

// NavigateMsg asks the host to move the browse cursor by Delta and call
// SetDetail with the new node's detail.
type NavigateMsg struct{ Delta int }

// CopyMsg carries the current section's text for the host to put on the
// clipboard.
type CopyMsg struct{ Text string }

// OpenMsg asks the host to open the inspected node's file in $EDITOR.
type OpenMsg struct{}

// Panel is the inspector dock panel.
type Panel struct {
	detail Detail
	sel    int   // selected section
	offset []int // scroll offset per section
	// bodyRows is the section body height of the last render; page keys use
	// it, and it bounds the scroll.
	bodyRows int
	lines    [][]string // wrapped lines per section, from the last render
}

var _ dock.Panel = (*Panel)(nil)

// New builds an inspector over d.
func New(d Detail) *Panel {
	p := &Panel{}
	p.SetDetail(d)
	return p
}

// SetDetail swaps the detail, keeping the selected section when the new
// detail has one by the same name so stepping with n/p stays put.
func (p *Panel) SetDetail(d Detail) {
	name := ""
	if p.sel < len(p.detail.Sections) {
		name = p.detail.Sections[p.sel].Name
	}
	p.detail = d
	p.sel = 0
	for i, s := range d.Sections {
		if s.Name == name {
			p.sel = i
		}
	}
	p.offset = make([]int, len(d.Sections))
	p.lines = nil
}

// Detail returns the detail being shown.
func (p *Panel) Detail() Detail { return p.detail }

// Section reports the selected section index.
func (p *Panel) Section() int { return p.sel }

// Sizing takes the whole frame; the transcript is hidden while it is open.
func (p *Panel) Sizing() dock.Sizing { return dock.FullFrame }

// Update handles the inspector's keys.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc", "q":
		return func() tea.Msg { return ClosedMsg{} }
	case "tab":
		p.move(1)
	case "shift+tab":
		p.move(-1)
	case "down", "j":
		p.scroll(1)
	case "up", "k":
		p.scroll(-1)
	case "pgdown", "ctrl+d", "space":
		p.scroll(max(p.bodyRows-1, 1))
	case "pgup", "ctrl+u":
		p.scroll(-max(p.bodyRows-1, 1))
	case "g", "home":
		p.scroll(-1 << 30)
	case "G", "end":
		p.scroll(1 << 30)
	case "n":
		return func() tea.Msg { return NavigateMsg{Delta: 1} }
	case "p":
		return func() tea.Msg { return NavigateMsg{Delta: -1} }
	case "y":
		if p.sel < len(p.detail.Sections) {
			text := p.detail.Sections[p.sel].Body
			return func() tea.Msg { return CopyMsg{Text: text} }
		}
	case "o":
		return func() tea.Msg { return OpenMsg{} }
	}
	return nil
}

func (p *Panel) move(d int) {
	n := len(p.detail.Sections)
	if n == 0 {
		return
	}
	p.sel = (p.sel + d + n) % n
}

func (p *Panel) scroll(d int) {
	if p.sel >= len(p.offset) {
		return
	}
	total := 0
	if p.sel < len(p.lines) {
		total = len(p.lines[p.sel])
	}
	maxOff := max(total-max(p.bodyRows, 1), 0)
	p.offset[p.sel] = max(0, min(p.offset[p.sel]+d, maxOff))
}

// View renders the panel within the frame the dock gives it.
func (p *Panel) View(width, maxHeight int) string {
	pw := layout.PanelWidth(width)
	inner := max(pw-3, 10)
	th := theme.Current()
	muted := lipgloss.NewStyle().Foreground(th.FGMuted)
	emph := lipgloss.NewStyle().Bold(true).Foreground(th.FGEmphasis)

	var head []string
	if p.detail.Subject != "" {
		head = append(head, emph.Render(ansi.Truncate(strings.ReplaceAll(p.detail.Subject, "\n", " "), inner, "…")))
	}
	keyW := 0
	for _, f := range p.detail.Fields {
		keyW = max(keyW, ansi.StringWidth(f.Key))
	}
	// Fields never take more than half the frame; the sections need room.
	fieldRows := 0
	for _, f := range p.detail.Fields {
		wrapped := strings.Split(ansi.Wrap(f.Value, max(inner-keyW-2, 8), ""), "\n")
		for i, l := range wrapped {
			if fieldRows >= max(maxHeight/2-2, 2) {
				break
			}
			label := strings.Repeat(" ", keyW)
			if i == 0 {
				label = f.Key + strings.Repeat(" ", keyW-ansi.StringWidth(f.Key))
			}
			head = append(head, muted.Render(label)+"  "+l)
			fieldRows++
		}
	}

	var tabs []string
	for i, s := range p.detail.Sections {
		if i == p.sel {
			tabs = append(tabs, chrome.SelectionStyle().Render(" "+s.Name+" "))
		} else {
			tabs = append(tabs, muted.Render(" "+s.Name+" "))
		}
	}
	tabRow := strings.Join(tabs, " ")

	// Body rows: what is left after the panel header, head, tab row and rule.
	p.bodyRows = max(maxHeight-1-len(head)-2, 1)
	p.lines = make([][]string, len(p.detail.Sections))
	for i, s := range p.detail.Sections {
		p.lines[i] = strings.Split(ansi.Wrap(s.Body, inner, ""), "\n")
	}
	p.scroll(0)

	var body []string
	body = append(body, head...)
	if len(p.detail.Sections) > 0 {
		body = append(body, tabRow)
		rule := lipgloss.NewStyle().Foreground(th.BorderMuted).Render(strings.Repeat("─", inner))
		body = append(body, rule)
		lines := p.lines[p.sel]
		off := p.offset[p.sel]
		end := min(off+p.bodyRows, len(lines))
		if off < end {
			body = append(body, lines[off:end]...)
		}
	}
	hints := "tab section · ↑↓ scroll · n/p step · y copy · o open · esc back"
	return chrome.PanelWithHints(p.detail.Title, hints, strings.Join(body, "\n"), pw, maxHeight, true, th)
}
