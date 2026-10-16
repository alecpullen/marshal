package activeworkpanel

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/theme"
)

type Item struct {
	ID    string
	Label string
}

type DoneMsg struct {
	Stop bool
	IDs  []string
}

type Panel struct {
	replacing bool
	items     []Item
	cursor    int
}

var _ dock.Panel = (*Panel)(nil)

func New(replacing bool, items []Item) *Panel {
	p := &Panel{replacing: replacing}
	p.Replace(items)
	return p
}

func (p *Panel) Replace(items []Item) {
	p.items = append([]Item(nil), items...)
	p.cursor = 0
}

func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "esc", "n", "N":
		return p.done(false)
	case "up", "left", "k", "h":
		p.cursor = 0
	case "down", "right", "j", "l":
		p.cursor = 1
	case "enter":
		return p.done(p.cursor == 1)
	}
	return nil
}

func (p *Panel) done(stop bool) tea.Cmd {
	ids := make([]string, len(p.items))
	for i, item := range p.items {
		ids[i] = item.ID
	}
	result := DoneMsg{Stop: stop, IDs: ids}
	return func() tea.Msg { return result }
}

func (p *Panel) View(width, maxHeight int) string {
	if width < 4 || maxHeight < 4 {
		return ""
	}
	keep, stop := "Keep Marshal open", "Stop work and continue exiting"
	if p.replacing {
		keep, stop = "Keep current conversation", "Stop work and replace conversation"
	}
	rows := []string{fmt.Sprintf("Active work (%d) will be stopped:", len(p.items))}
	available := max(0, maxHeight-5)
	shown := min(len(p.items), available)
	if shown < len(p.items) && available > 0 {
		shown = max(0, available-1)
	}
	line := lipgloss.NewStyle().MaxWidth(max(1, width-6))
	for _, item := range p.items[:shown] {
		rows = append(rows, line.Render("  "+item.Label))
	}
	if shown < len(p.items) && available > 0 {
		rows = append(rows, fmt.Sprintf("  +%d more active items", len(p.items)-shown))
	}
	if maxHeight >= 5 {
		rows = append(rows, "")
	}
	for i, label := range []string{keep, stop} {
		marker := "  "
		if i == p.cursor {
			marker = "> "
		}
		rows = append(rows, line.Render(marker+label))
	}
	content := strings.Join(rows, "\n")
	return chrome.PanelWithHints("Active work", "arrows choose · enter confirm · esc keep",
		content, width-2, min(lipgloss.Height(content)+1, maxHeight), true, theme.Current())
}
