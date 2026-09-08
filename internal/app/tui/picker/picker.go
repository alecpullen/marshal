// Package picker renders a centered modal selection list with
// filter-as-you-type, used by slash commands (/rewind, /branches,
// /mode). Keys are fzf-style: printable characters edit the filter, ↑/↓
// move, Enter picks, Esc cancels — j/k belong to the filter, not movement.
package picker

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/fuzzy"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
)

func groupStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary)
}
func detailStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }
func nowStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary) }
func badgeStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(theme.Current().StatusInfo) }
func mutedStyle() lipgloss.Style  { return theme.MutedStyle() }
func errStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(theme.Current().StatusError) }

// Item is one pickable row.
type Item struct {
	Label  string // primary text, left-aligned
	Detail string // secondary text, right-aligned, muted
	Badge  string // optional tag; "●" prefix marks the current item
	Group  string // optional group header (unfiltered view only)
	Value  string // opaque result delivered in PickedMsg
	// Pinned keeps the item at the top of the list regardless of filter
	// score (group headers are suppressed for pinned rows). Use for
	// "new X" affordances that must stay discoverable while filtering.
	Pinned bool
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
	filter      textfield.Model
	matches     []int // indices into items, rank order
	cursor      int   // index into matches
	allowCustom bool
	errMsg      string // transient error displayed below the footer
}

// New creates a picker model. The cursor starts on the first item whose
// Badge begins with "●", or the first item if none have that badge.
func New(title, footer string, items []Item) *Model {
	ti := textfield.New()
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

// SetFilter pre-fills the filter query (e.g. "/rewind fix" with no exact
// match).
func (m *Model) SetFilter(q string) {
	m.filter.SetValue(q)
	m.filter.CursorEnd()
	m.refilter()
}

// FilterValue returns the current filter text.
func (m *Model) FilterValue() string { return m.filter.Value() }

// Items returns a copy of the picker's items, in the order they were
// supplied. It exists so callers (and tests) can inspect the offered rows
// without reaching into the unexported field.
func (m *Model) Items() []Item {
	out := make([]Item, len(m.items))
	copy(out, m.items)
	return out
}

// SetAllowCustom enables custom values: pressing Enter with a typed filter
// text and no selection emits PickedMsg with the filter text.
func (m *Model) SetAllowCustom(v bool) {
	m.allowCustom = v
}

// SetError displays a transient error message below the footer (e.g. when
// a picker's commit handler rejects the value). Empty string clears it.
// Any user keypress also clears the error — the user is fixing their input.
func (m *Model) SetError(msg string) {
	m.errMsg = msg
}

// ErrMsg returns the current error message, or "" if none.
func (m *Model) ErrMsg() string {
	return m.errMsg
}

func (m *Model) refilter() {
	pinnedSet := make(map[int]bool)
	var pinnedOrder []int
	var rest []int
	for i, it := range m.items {
		if it.Pinned {
			pinnedSet[i] = true
			pinnedOrder = append(pinnedOrder, i)
		} else {
			rest = append(rest, i)
		}
	}
	hay := make([]string, len(rest))
	for i, idx := range rest {
		it := m.items[idx]
		hay[i] = it.Group + " " + it.Label + " " + it.Detail
	}
	rankedRest := fuzzy.Rank(m.filter.Value(), hay)
	m.matches = make([]int, 0, len(pinnedOrder)+len(rankedRest)+1)
	m.matches = append(m.matches, pinnedOrder...)
	for _, r := range rankedRest {
		m.matches = append(m.matches, rest[r])
	}
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
		m.errMsg = ""
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
			m.errMsg = "No matches to select"
			return nil
		case "up":
			m.errMsg = ""
			if m.cursor > 0 {
				m.cursor--
			}
			return nil
		case "down":
			m.errMsg = ""
			if m.cursor < len(m.matches)-1 {
				m.cursor++
			}
			return nil
		}
		// Any other key edits the filter — clear the error as the user
		// corrects their input.
		m.errMsg = ""
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(k)
		m.refilter()
		m.cursor = 0
		return cmd
	}
	return nil
}

// Sizing keeps the picker docked under the default height cap.
func (m *Model) Sizing() dock.Sizing { return dock.Docked }

// View renders the picker as a centered panel with filter input, item list,
// and footer.
func (m *Model) View(maxW, maxH int) string {
	if maxH < 2 {
		return ""
	}
	pw := layout.PanelWidth(maxW)
	inner := pw - 3

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
		if !filtering && !it.Pinned && it.Group != "" && it.Group != lastGroup {
			rows = append(rows, groupStyle().Render(it.Group))
			lastGroup = it.Group
		}
		marker := chrome.BlankMarker
		if pos == m.cursor {
			marker = chrome.SelectionMarker
			focusLine = len(rows)
		}
		if pos == m.cursor {
			label := it.Label
			if it.Pinned {
				label = groupStyle().Render(it.Label)
			}
			line := chrome.Row(marker, label, it.Detail, it.Badge, inner)
			rows = append(rows, chrome.SelectionStyle().Width(inner).Render(line))
			continue
		}
		detail := ""
		if it.Detail != "" {
			detail = detailStyle().Render(it.Detail)
		}
		badge := ""
		if it.Badge != "" {
			bs := badgeStyle()
			if strings.HasPrefix(it.Badge, "●") {
				bs = nowStyle()
			}
			badge = bs.Render(it.Badge)
		}
		label := it.Label
		if it.Pinned {
			label = groupStyle().Render(it.Label)
		}
		rows = append(rows, chrome.Row(marker, label, detail, badge, inner))
	}
	if len(m.matches) == 0 {
		rows = append(rows, mutedStyle().Render("  no matches"))
	}

	listH := maxH - 3 // header + filter + margin
	if listH < 3 {
		listH = 3
	}
	body := chrome.ClipLines(rows, focusLine, listH, theme.Current())
	content := "/ " + m.filter.View() + "\n" + body
	if m.footer != "" {
		content += "\n" + mutedStyle().Render(m.footer)
	}
	if m.errMsg != "" {
		content += "\n" + errStyle().Render(m.errMsg)
	}
	ph := min(lipgloss.Height(content)+1, maxH)
	return chrome.PanelWithHints(m.title, "↵ pick · Esc cancel", content, pw, ph, true, theme.Current())
}
