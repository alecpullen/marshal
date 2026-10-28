package sessionsheet

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

// rulesDetailHints are the keys the rules detail view advertises. It is
// read-only, so scrolling and leaving are all it offers.
const rulesDetailHints = "↑↓ scroll · esc back · ctrl+b close"

// ClosedMsg asks the Model to close the sheet.
type ClosedMsg struct{}

// RunCommandMsg asks the Model to close the sheet and dispatch a slash
// command (Command has no leading slash).
type RunCommandMsg struct{ Command string }

// DefaultSections is the sheet's section list, in render order.
func DefaultSections() []Section {
	return []Section{
		SwarmSection{},
		SDDSection{},
		ContextSection{},
		ChangedSection{},
		WorktreesSection{},
		WorkingSetSection{},
		ToolsSection{},
		RulesSection{},
		RepoSection{},
		SkillsSection{},
		SessionSection{},
	}
}

// commandFor maps a section to the slash command that shows it in full.
// Sections without one return "".
func commandFor(id string) string {
	switch id {
	case "context":
		return "context"
	case "changed":
		return "diff"
	case "worktrees":
		return "worktrees"
	case "skills":
		return "skills"
	case "tools":
		return "log"
	case "sdd":
		return "run"
	case "swarm":
		return "agents"
	}
	return ""
}

// Panel is the Ctrl+B session sheet: every relevant section stacked in a
// docked panel, one line each, with the selected section expanded. It is
// read-only; Enter hands off to the section's slash command.
type Panel struct {
	sections []Section
	data     func() Data
	sel      int // index into the relevant sections

	// rulesDetail is the read-only full-rules view Enter opens on the rules
	// section. rulesOffset is its scroll position in physical lines and
	// rulesRows the body height it last rendered at, which PgUp/PgDn size
	// their stride from. Both are derived state: the content itself is
	// rebuilt from data on every render.
	rulesDetail bool
	rulesOffset int
	rulesRows   int
}

// NewPanel builds the sheet. data is called on every render so the sheet
// shows live values; anything expensive in it must already be cached.
func NewPanel(sections []Section, data func() Data) *Panel {
	return &Panel{sections: sections, data: data}
}

// SetData swaps the data source. The Model calls it each frame so the
// closure sees the current model value rather than the one the sheet was
// opened from.
func (p *Panel) SetData(data func() Data) { p.data = data }

// HasContent reports whether any section currently has something to show.
func (p *Panel) HasContent() bool { return len(p.relevant(p.data())) > 0 }

// Sizing keeps the transcript visible above the sheet.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

func (p *Panel) relevant(d Data) []Section {
	live := make([]Section, 0, len(p.sections))
	for _, s := range p.sections {
		if s.Relevant(d) {
			live = append(live, s)
		}
	}
	return live
}

// Update moves the selection, opens the selected section's command, or
// closes the sheet. On the rules section Enter opens the read-only detail
// view instead, which owns the keys until Escape returns to the sheet.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if p.rulesDetail {
		return p.updateRulesDetail(k)
	}
	live := p.relevant(p.data())
	switch k.String() {
	case "esc", "ctrl+b":
		return func() tea.Msg { return ClosedMsg{} }
	case "up":
		p.sel = max(p.sel-1, 0)
	case "down":
		p.sel = min(p.sel+1, max(len(live)-1, 0))
	case "enter":
		if p.sel < len(live) {
			if live[p.sel].ID() == rulesSectionID {
				p.rulesDetail = true
				p.rulesOffset = 0
				return nil
			}
			if cmd := commandFor(live[p.sel].ID()); cmd != "" {
				return func() tea.Msg { return RunCommandMsg{Command: cmd} }
			}
		}
	}
	return nil
}

// updateRulesDetail scrolls the detail view or leaves it. Escape returns to
// the sheet without closing it; Ctrl+B still closes outright. Scrolling only
// moves the offset — it never touches the data it renders.
func (p *Panel) updateRulesDetail(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "ctrl+b":
		return func() tea.Msg { return ClosedMsg{} }
	case "esc":
		p.rulesDetail = false
		p.rulesOffset = 0
	case "up", "k":
		p.rulesOffset = max(p.rulesOffset-1, 0)
	case "down", "j":
		// Clamped against the freshly derived line count at render time.
		p.rulesOffset++
	case "pgup":
		p.rulesOffset = max(p.rulesOffset-max(p.rulesRows-1, 1), 0)
	case "pgdown":
		p.rulesOffset += max(p.rulesRows-1, 1)
	case "home", "g":
		p.rulesOffset = 0
	case "end", "G":
		// Clamped to the last page at render time, where the line count and
		// the body height are both known.
		p.rulesOffset = 1 << 30
	}
	return nil
}

// View renders the stacked sections inside the dock's budget. Every section
// costs a header row and a one-line summary; the selected one gets whatever
// rows remain. When even the collapsed stack does not fit, the window
// slides to keep the selected section's header visible.
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 3 {
		return ""
	}
	if p.rulesDetail {
		return p.viewRulesDetail(width, maxHeight)
	}
	d := p.data()
	live := p.relevant(d)
	pw := layout.PanelWidth(width)
	if len(live) == 0 {
		// Sections can all go quiet while the sheet is open. Say so rather
		// than rendering nothing, so a panel that still owns the keys is
		// never invisible.
		return chrome.PanelWithHints("Session", "esc close", theme.MutedStyle().Render("Nothing to show."), pw, 2, true, theme.Current())
	}
	p.sel = min(max(p.sel, 0), len(live)-1)

	inner := max(pw-3, 1)
	budget := maxHeight - 1 // the panel's own title row

	// Rows left for the selected body after every other section takes two
	// (header + summary) and the selected one takes its header.
	bodyRows := max(budget-2*(len(live)-1)-1, 1)

	var lines []string
	selStart, selEnd := 0, 0
	for i, s := range live {
		title := s.Title()
		if i == p.sel {
			title = glyph.Running + " " + title
			selStart = len(lines)
		}
		lines = append(lines, chrome.Divider(title))
		if i == p.sel {
			body := s.Render(d, inner, bodyRows)
			if len(body) > bodyRows {
				body = body[:bodyRows]
			}
			lines = append(lines, body...)
			selEnd = len(lines)
		} else {
			lines = append(lines, s.OneLine(d, inner))
		}
	}
	if len(lines) > budget {
		start := 0
		if selEnd > budget {
			start = min(selEnd-budget, selStart)
		}
		lines = lines[start : start+budget]
	}

	body := strings.Join(lines, "\n")
	hints := "↑↓ section · esc close"
	if p.sel < len(live) {
		if id := live[p.sel].ID(); commandFor(id) != "" || id == rulesSectionID {
			hints = "↑↓ section · ↵ open · esc close"
		}
	}
	return chrome.PanelWithHints("Session", hints, body, pw, len(lines)+1, true, theme.Current())
}

// viewRulesDetail renders every standing rule in full, one rule per " allow "
// line, each wrapped to the width the sheet body uses. The content is derived
// from live data on every render, so a rule that disappears while the view is
// open leaves an empty message instead of a stale snapshot.
func (p *Panel) viewRulesDetail(width, maxHeight int) string {
	pw := layout.PanelWidth(width)
	inner := max(pw-3, 1)
	budget := maxHeight - 1 // the panel's own title row

	lines := p.rulesDetailLines(inner)
	if len(lines) == 0 {
		// The rules can go while the view is open. Say so, and reset the
		// scroll position: there is nothing left to be scrolled to.
		p.rulesOffset, p.rulesRows = 0, 0
		return chrome.PanelWithHints("Session", "esc back · ctrl+b close",
			theme.MutedStyle().Render("Nothing to show."), pw, 2, true, theme.Current())
	}

	// One budget row carries the position indicator, so the window and its
	// ↑/↓ markers share what remains and the frame never exceeds maxHeight.
	const posRows = 1
	avail := max(budget-posRows, 1)

	// Markers are only worth a row while a content row survives beside them.
	shown := avail
	top, bottom := false, false
	if avail >= 2 && p.rulesOffset > 0 {
		top = true
		shown--
	}
	off := min(max(p.rulesOffset, 0), max(len(lines)-shown, 0))
	if shown > 1 && off+shown < len(lines) && (avail-shown) < 2 {
		bottom = true
		shown--
	}
	shown = max(shown, 1)

	// Clamp against the line count derived this frame: that covers both a
	// resize and rules disappearing under the view. It only ever lowers the
	// offset, so scrolling cannot drift past the last page.
	p.rulesOffset = min(max(p.rulesOffset, 0), max(len(lines)-shown, 0))
	top = p.rulesOffset > 0
	bottom = p.rulesOffset+shown < len(lines)
	p.rulesRows = shown

	muted := theme.MutedStyle()
	rows := make([]string, 0, budget)
	if top {
		rows = append(rows, muted.Render("  ↑ more"))
	}
	rows = append(rows, lines[p.rulesOffset:p.rulesOffset+shown]...)
	if bottom {
		rows = append(rows, muted.Render("  ↓ more"))
	}
	end := p.rulesOffset + shown
	rows = append(rows, muted.Render(ansi.Truncate(
		fmt.Sprintf("  %d–%d/%d", p.rulesOffset+1, end, len(lines)), inner, "…")))

	return chrome.PanelWithHints("Session", rulesDetailHints,
		strings.Join(rows, "\n"), pw, len(rows)+1, true, theme.Current())
}

// rulesDetailLines is the detail view's complete content: every rule prefixed
// the way the sheet prefixes it, wrapped to width, one physical line each.
func (p *Panel) rulesDetailLines(width int) []string {
	d := p.data()
	lines := make([]string, 0, len(d.Rules))
	for _, r := range d.Rules {
		lines = append(lines, strings.Split(ansi.Wrap(" allow "+r, width, ""), "\n")...)
	}
	return lines
}
