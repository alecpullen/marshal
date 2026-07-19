// Package picker renders a centered modal selection list with
// filter-as-you-type, used by slash commands (/model, /rewind, /branches,
// /mode). Keys are fzf-style: printable characters edit the filter, ↑/↓
// move, Enter picks, Esc cancels — j/k belong to the filter, not movement.
package picker

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/theme"
)

func groupStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary)
}
func detailStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }
func nowStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary) }
func badgeStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(theme.Current().StatusInfo) }
func cursorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Background(theme.Current().BGSelection)
}
func mutedStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }

// Item is one pickable row.
type Item struct {
	Label  string // primary text, left-aligned
	Detail string // secondary text, right-aligned, muted
	Badge  string // optional tag; "●" prefix marks the current item
	Group  string // optional group header (unfiltered view only)
	Value  string // opaque result delivered in PickedMsg
}

// PickedMsg is emitted when the user selects an item.
type PickedMsg struct{ Value string }

// CancelledMsg is emitted when the user presses Esc.
type CancelledMsg struct{}

// Model is the state for a picker modal.
type Model struct {
	title       string
	footer      string
	items       []Item
	filter      textinput.Model
	matches     []int // indices into items, rank order
	cursor      int   // index into matches
	allowCustom bool
}

// New creates a picker model. The cursor starts on the first item whose
// Badge begins with "●", or the first item if none have that badge.
func New(title, footer string, items []Item) *Model {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	m := &Model{title: title, footer: footer, items: items, filter: ti}
	m.refilter()
	for pos, idx := range m.matches {
		if strings.HasPrefix(items[idx].Badge, "●") {
			m.cursor = pos
			break
		}
	}
	return m
}

// SetFilter pre-fills the filter query (e.g. "/model qw" with no exact
// match).
func (m *Model) SetFilter(q string) {
	m.filter.SetValue(q)
	m.filter.CursorEnd()
	m.refilter()
}

// SetAllowCustom enables custom values: pressing Enter with a typed filter
// text and no selection emits PickedMsg with the filter text.
func (m *Model) SetAllowCustom(v bool) {
	m.allowCustom = v
}

func (m *Model) refilter() {
	hay := make([]string, len(m.items))
	for i, it := range m.items {
		hay[i] = it.Group + " " + it.Label + " " + it.Detail
	}
	m.matches = fuzzy.Rank(m.filter.Value(), hay)
	if m.allowCustom && strings.TrimSpace(m.filter.Value()) != "" {
		exact := false
		for _, idx := range m.matches {
			if m.items[idx].Value == m.filter.Value() {
				exact = true
				break
			}
		}
		if !exact {
			m.matches = append([]int{-1}, m.matches...)
		}
	}
	if m.cursor >= len(m.matches) {
		m.cursor = max(len(m.matches)-1, 0)
	}
}

// Update handles key and paste events for the picker.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.refilter()
		m.cursor = 0
		return cmd
	case tea.KeyPressMsg:
		k := msg
		switch k.String() {
		case "esc":
			return func() tea.Msg { return CancelledMsg{} }
		case "enter":
			if m.cursor < len(m.matches) && len(m.matches) > 0 {
				idx := m.matches[m.cursor]
				if idx == -1 {
					return func() tea.Msg { return PickedMsg{Value: m.filter.Value()} }
				}
				v := m.items[idx].Value
				return func() tea.Msg { return PickedMsg{Value: v} }
			}
			return nil
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return nil
		case "down":
			if m.cursor < len(m.matches)-1 {
				m.cursor++
			}
			return nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(k)
		m.refilter()
		m.cursor = 0
		return cmd
	}
	return nil
}

// View renders the picker as a centered panel with filter input, item list,
// and footer.
func (m *Model) View(maxW, maxH int) string {
	// chrome.Panel always emits at least its top and bottom border rows, so
	// it cannot honor a height budget under 3 (mirrors the identical guard
	// in settings.BrowserPanel.View, the other dock.Panel implementation).
	if maxH < 3 {
		return ""
	}
	pw := min(64, maxW-8)
	if pw < 30 {
		pw = max(maxW-2, 30)
	}
	inner := pw - 2

	filtering := strings.TrimSpace(m.filter.Value()) != ""
	var rows []string
	focusLine := 0
	lastGroup := ""
	for pos, idx := range m.matches {
		var it Item
		if idx == -1 {
			it = Item{Label: "Use '" + m.filter.Value() + "'", Value: m.filter.Value(), Badge: "custom"}
		} else {
			it = m.items[idx]
		}
		if !filtering && it.Group != "" && it.Group != lastGroup {
			rows = append(rows, groupStyle().Render(it.Group))
			lastGroup = it.Group
		}
		marker := "  "
		if pos == m.cursor {
			marker = "▸ "
			focusLine = len(rows)
		}
		right := detailStyle().Render(it.Detail)
		if it.Badge != "" {
			bs := badgeStyle()
			if strings.HasPrefix(it.Badge, "●") {
				bs = nowStyle()
			}
			right += " " + bs.Render(it.Badge)
		}
		gap := inner - lipgloss.Width(marker) - lipgloss.Width(it.Label) - lipgloss.Width(right)
		if gap < 1 {
			gap = 1
		}
		label := it.Label
		if pos == m.cursor {
			label = cursorStyle().Render(label)
		}
		rows = append(rows, marker+label+strings.Repeat(" ", gap)+right)
	}
	if len(m.matches) == 0 {
		rows = append(rows, mutedStyle().Render("  no matches"))
	}

	// panel = filter line + separator + windowed rows + footer
	listH := maxH - 7 // borders(2) + filter + separator + footer + margin(2)
	if listH < 3 {
		listH = 3
	}
	body := chrome.ClipLines(rows, focusLine, listH, theme.Current())
	footer := mutedStyle().Render("[↑↓] move [↵] pick [Esc] cancel")
	if m.footer != "" {
		footer += mutedStyle().Render(" · " + m.footer)
	}
	content := "/ " + m.filter.View() + "\n" +
		mutedStyle().Render(strings.Repeat("─", inner)) + "\n" +
		body + "\n" + footer
	ph := min(lipgloss.Height(content)+2, maxH)
	return chrome.Panel(m.title, content, pw, ph, true, theme.Current())
}
