// Package inspector is the full-screen panel behind `i` in browse mode: every
// recorded fact about one step or tool call, in sections that scroll
// independently, plus a view-only metadata tab when the detail carries fields.
// It renders a Detail value built by the tui package and imports nothing from
// session, so the view model stays the only place that knows how a step or an
// audit event is shaped.
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

// metadataTabName labels the view-only tab that holds Detail.Fields.
const metadataTabName = "metadata"

// tabRef is one entry in the displayed tab sequence: the metadata tab when the
// detail carries fields, then every section in order. meta is the metadata
// tab's identity, so a section that happens to be called "metadata" stays
// distinguishable from it without matching the label.
type tabRef struct {
	name string
	meta bool
	idx  int // index into Detail.Sections; -1 for the metadata tab
}

// Panel is the inspector dock panel.
type Panel struct {
	detail Detail
	sel    int   // index into the displayed tab sequence
	offset []int // scroll offset per displayed tab
	// bodyRows is the section body height of the last render; page keys use
	// it, and it bounds the scroll.
	bodyRows int
	lines    [][]string // wrapped lines per displayed tab, from the last render
}

var _ dock.Panel = (*Panel)(nil)

// New builds an inspector over d.
func New(d Detail) *Panel {
	p := &Panel{}
	p.SetDetail(d)
	return p
}

// tabs is the displayed tab sequence: the metadata tab first when there are
// fields, then every section in order. Detail.Sections is never changed.
func (p *Panel) tabs() []tabRef {
	out := make([]tabRef, 0, len(p.detail.Sections)+1)
	if len(p.detail.Fields) > 0 {
		out = append(out, tabRef{name: metadataTabName, meta: true, idx: -1})
	}
	for i, s := range p.detail.Sections {
		out = append(out, tabRef{name: s.Name, idx: i})
	}
	return out
}

// SetDetail swaps the detail, keeping the selected tab by identity — metadata
// stays selected when the new detail still has fields, and an ordinary section
// is kept when the new detail has one by the same name — so stepping with n/p
// stays put. A fresh selection lands on the metadata tab when fields exist.
func (p *Panel) SetDetail(d Detail) {
	var prev tabRef
	havePrev := false
	if tabs := p.tabs(); p.sel >= 0 && p.sel < len(tabs) {
		prev, havePrev = tabs[p.sel], true
	}
	p.detail = d
	tabs := p.tabs()
	p.sel = 0 // the metadata tab, when there is one, is always first
	if havePrev {
		for i, t := range tabs {
			if t.meta != prev.meta || (!t.meta && t.name != prev.name) {
				continue
			}
			p.sel = i
			break
		}
	}
	p.offset = make([]int, len(tabs))
	p.lines = nil
}

// Detail returns the detail being shown.
func (p *Panel) Detail() Detail { return p.detail }

// Section reports the selected section's index in Detail.Sections, or -1 when
// the metadata tab is selected (and when there is nothing to select).
func (p *Panel) Section() int {
	tabs := p.tabs()
	if p.sel < 0 || p.sel >= len(tabs) || tabs[p.sel].meta {
		return -1
	}
	return tabs[p.sel].idx
}

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
		tabs := p.tabs()
		if p.sel >= 0 && p.sel < len(tabs) {
			text := copyText(p.detail, tabs[p.sel])
			return func() tea.Msg { return CopyMsg{Text: text} }
		}
	case "o":
		return func() tea.Msg { return OpenMsg{} }
	}
	return nil
}

// copyText is what y puts on the clipboard: the metadata tab's complete
// key/value text, or an ordinary section's own body.
func copyText(d Detail, t tabRef) string {
	if !t.meta && t.idx >= 0 && t.idx < len(d.Sections) {
		return d.Sections[t.idx].Body
	}
	lines := make([]string, 0, len(d.Fields))
	for _, f := range d.Fields {
		lines = append(lines, f.Key+": "+f.Value)
	}
	return strings.Join(lines, "\n")
}

func (p *Panel) move(d int) {
	n := len(p.tabs())
	if n == 0 {
		return
	}
	p.sel = (p.sel + d + n) % n
}

func (p *Panel) scroll(d int) {
	if p.sel < 0 || p.sel >= len(p.offset) {
		return
	}
	total := 0
	if p.sel < len(p.lines) {
		total = len(p.lines[p.sel])
	}
	p.offset[p.sel] = max(0, min(p.offset[p.sel]+d, p.maxOffset(total)))
}

// maxOffset is the furthest a tab can scroll while its last line is still on
// screen, marker rows included. It is the same clamp window applies, so a
// resize cannot leave the view past the end.
func (p *Panel) maxOffset(total int) int {
	room := max(p.bodyRows, 1)
	if total <= room {
		return 0
	}
	return max(total-max(room-2, 1), 0)
}

// window is one tab's visible rows: its scrolled lines plus muted ↑/↓ more
// markers, all inside the bodyRows budget.
func (p *Panel) window(lines []string, off int, muted lipgloss.Style) []string {
	room := max(p.bodyRows, 1)
	if len(lines) <= room {
		return lines
	}
	n := max(room-2, 1) // content rows with both markers showing
	start := min(max(off, 0), max(len(lines)-n, 0))
	out := make([]string, 0, room)
	if start > 0 {
		out = append(out, muted.Render("  ↑ more"))
	} else {
		n++ // no top marker, so the row is content
	}
	end := min(start+n, len(lines))
	out = append(out, lines[start:end]...)
	if end < len(lines) {
		out = append(out, muted.Render("  ↓ more"))
	}
	if len(out) > room {
		out = out[:room]
	}
	return out
}

// metadataLines lays out every field: the key in a column as wide as the
// widest key, two spaces, then the value wrapped to what is left, with
// continuation lines aligned under the value.
func metadataLines(fields []Field, inner int, muted lipgloss.Style) []string {
	keyW := 0
	for _, f := range fields {
		keyW = max(keyW, ansi.StringWidth(f.Key))
	}
	indent := strings.Repeat(" ", keyW+2)
	width := max(inner-keyW-2, 8)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		pad := strings.Repeat(" ", max(keyW-ansi.StringWidth(f.Key), 0))
		for i, l := range strings.Split(ansi.Wrap(f.Value, width, ""), "\n") {
			if i == 0 {
				out = append(out, muted.Render(f.Key+pad)+"  "+l)
				continue
			}
			out = append(out, indent+l)
		}
	}
	return out
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

	tabs := p.tabs()
	// Body rows: what is left after the panel header, head, tab row and rule.
	p.bodyRows = max(maxHeight-1-len(head)-2, 1)
	p.lines = make([][]string, len(tabs))
	for i, t := range tabs {
		if t.meta {
			p.lines[i] = metadataLines(p.detail.Fields, inner, muted)
			continue
		}
		p.lines[i] = strings.Split(ansi.Wrap(p.detail.Sections[t.idx].Body, inner, ""), "\n")
	}
	p.scroll(0)

	var body []string
	body = append(body, head...)
	if len(tabs) > 0 {
		row := make([]string, len(tabs))
		for i, t := range tabs {
			if i == p.sel {
				row[i] = chrome.SelectionStyle().Render(" " + t.name + " ")
				continue
			}
			row[i] = muted.Render(" " + t.name + " ")
		}
		body = append(body, strings.Join(row, " "))
		body = append(body, lipgloss.NewStyle().Foreground(th.BorderMuted).Render(strings.Repeat("─", inner)))
		if p.sel >= 0 && p.sel < len(p.lines) {
			off := 0
			if p.sel < len(p.offset) {
				off = p.offset[p.sel]
			}
			body = append(body, p.window(p.lines[p.sel], off, muted)...)
		}
	}
	hints := "tab section · ↑↓ scroll · n/p step · y copy · o open · esc back"
	return chrome.PanelWithHints(p.detail.Title, hints, strings.Join(body, "\n"), pw, maxHeight, true, th)
}
