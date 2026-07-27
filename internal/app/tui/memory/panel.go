package memory

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
	"marshal/internal/db"
)

// BrowserPanel is the docked memory browser. It presents a filterable list of
// project memories and emits ShowMsg, DeletedMsg, and ClosedMsg.
type BrowserPanel struct {
	db          *db.DB
	projectID   int64
	all         []db.Memory
	filter      textfield.Model
	matches     []int
	cursor      int
	deleteArmed bool
	loadErr     error
}

var _ dock.Panel = (*BrowserPanel)(nil)

// NewPanel creates a docked memory browser for the given project.
func NewPanel(database *db.DB, projectID int64) *BrowserPanel {
	filter := textfield.New()
	filter.SetVirtualCursor(true)
	filter.Focus()

	all, err := database.GetMemories(projectID)
	if err != nil {
		all = nil
	}

	p := &BrowserPanel{
		db:        database,
		projectID: projectID,
		all:       all,
		filter:    filter,
		loadErr:   err,
	}
	p.refilter()
	return p
}

// FilterValue returns the current filter text.
func (p *BrowserPanel) FilterValue() string { return p.filter.Value() }

// Update handles filtering, cursor movement, selection, and delete gestures.
func (p *BrowserPanel) Update(msg tea.Msg) tea.Cmd {
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		switch k.String() {
		case "esc":
			return func() tea.Msg { return ClosedMsg{} }
		case "enter":
			if p.cursor < len(p.matches) {
				id := p.all[p.matches[p.cursor]].ID
				return func() tea.Msg { return ShowMsg{ID: id} }
			}
			return nil
		case "up":
			p.moveCursor(-1)
			return nil
		case "down":
			p.moveCursor(1)
			return nil
		}

		if k.String() == "ctrl+d" {
			if p.deleteArmed {
				return p.deleteSelected()
			}
			p.deleteArmed = true
			return nil
		}

		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(k)
		p.refilter()
		p.cursor = 0
		p.deleteArmed = false
		return cmd

	case tea.PasteMsg:
		var cmd tea.Cmd
		p.filter, cmd = p.filter.Update(k)
		p.refilter()
		p.cursor = 0
		p.deleteArmed = false
		return cmd
	}

	return nil
}

func (p *BrowserPanel) moveCursor(delta int) {
	p.deleteArmed = false
	if len(p.matches) == 0 {
		return
	}
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.matches) {
		p.cursor = len(p.matches) - 1
	}
}

func (p *BrowserPanel) deleteSelected() tea.Cmd {
	p.deleteArmed = false
	if p.cursor >= len(p.matches) {
		return nil
	}
	idx := p.matches[p.cursor]
	mem := p.all[idx]
	title := memoryTitle(mem.Content)

	if p.db == nil {
		return func() tea.Msg { return DeletedMsg{Title: title, Err: fmt.Errorf("no database")} }
	}
	if err := p.db.DeleteMemory(mem.ID); err != nil {
		return func() tea.Msg { return DeletedMsg{Title: title, Err: err} }
	}

	p.all = append(p.all[:idx], p.all[idx+1:]...)
	p.refilter()
	if p.cursor >= len(p.matches) {
		p.cursor = max(len(p.matches)-1, 0)
	}
	return func() tea.Msg { return DeletedMsg{Title: title} }
}

func (p *BrowserPanel) refilter() {
	hay := make([]string, len(p.all))
	for i, m := range p.all {
		hay[i] = m.Content + " " + m.Kind
	}
	p.matches = fuzzy.Rank(p.filter.Value(), hay)
	if p.cursor >= len(p.matches) {
		p.cursor = max(len(p.matches)-1, 0)
	}
}

// View renders the browser inside the dock height budget.
func (p *BrowserPanel) View(width, maxHeight int) string {
	pw := layout.PanelWidth(width)
	inner := pw - 3

	if maxHeight < 3 {
		return mutedStyle().Render("Memory")
	}

	p.syncFilterStyles()
	p.filter.SetWidth(max(inner-2, 1))

	var rows []string
	focusLine := 0
	for pos, idx := range p.matches {
		mem := p.all[idx]
		marker := "  "
		if pos == p.cursor {
			marker = "▸ "
			focusLine = len(rows)
		}
		title := memoryTitle(mem.Content)
		right := mutedStyle().Render(formatAge(mem.CreatedAt) + " · " + mem.Kind)
		rightWidth := lipgloss.Width(right)
		titleBudget := inner - lipgloss.Width(marker) - rightWidth - 1
		if titleBudget < 1 {
			titleBudget = 1
		}
		if ansi.StringWidth(title) > titleBudget {
			title = ansi.Truncate(title, titleBudget, "…")
		}
		label := title
		if pos == p.cursor {
			label = cursorStyle().Render(label)
		}
		gap := inner - lipgloss.Width(marker) - lipgloss.Width(label) - rightWidth
		if gap < 1 {
			gap = 1
		}
		rows = append(rows, marker+label+strings.Repeat(" ", gap)+right)
	}
	if len(p.matches) == 0 && p.loadErr == nil {
		rows = append(rows, mutedStyle().Render("  no matches"))
	}

	listH := maxHeight - 3
	if listH < 1 {
		listH = 1
	}
	body := chrome.ClipLines(rows, focusLine, listH, theme.Current())

	content := "/ " + p.filter.View() + "\n" + body
	if p.deleteArmed {
		content += "\n" + mutedStyle().Render("press ctrl+d again to confirm delete · esc cancel")
	}
	if p.loadErr != nil {
		content += "\n" + mutedStyle().Render("load failed: "+p.loadErr.Error())
	}
	ph := min(lipgloss.Height(content)+1, maxHeight)
	return chrome.PanelWithHints("Memory", "⏎ show · ctrl+d delete · esc close", content, pw, ph, true, theme.Current())
}

func isMono() bool {
	_, ok := theme.Current().FGDefault.(lipgloss.NoColor)
	return ok
}

func (p *BrowserPanel) syncFilterStyles() {
	if !isMono() {
		return
	}
	empty := lipgloss.NewStyle()
	p.filter.SetVirtualCursor(false)
	p.filter.SetStyles(textinput.Styles{
		Focused: textinput.StyleState{
			Text:        empty,
			Placeholder: empty,
			Suggestion:  empty,
			Prompt:      empty,
		},
		Blurred: textinput.StyleState{
			Text:        empty,
			Placeholder: empty,
			Suggestion:  empty,
			Prompt:      empty,
		},
		Cursor: textinput.CursorStyle{
			Color: lipgloss.NoColor{},
		},
	})
}

func mutedStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}

func cursorStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Bold(true).Background(theme.Current().BGSelection)
}
