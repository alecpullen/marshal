package sessionsheet

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/theme"
)

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
// closes the sheet.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
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
			if cmd := commandFor(live[p.sel].ID()); cmd != "" {
				return func() tea.Msg { return RunCommandMsg{Command: cmd} }
			}
		}
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
	d := p.data()
	live := p.relevant(d)
	if len(live) == 0 {
		return ""
	}
	p.sel = min(max(p.sel, 0), len(live)-1)

	pw := layout.PanelWidth(width)
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
		lines = append(lines, chrome.Header(title, "", inner))
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
	return chrome.PanelWithHints("Session", "↑↓ section · ↵ open · esc close", body, pw, len(lines)+1, true, theme.Current())
}
