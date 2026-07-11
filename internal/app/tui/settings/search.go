package settings

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/fuzzy"
)

type searchHit struct {
	sectionIdx   int
	fieldID      string
	sectionTitle string
	fieldTitle   string
	keywords     []string
}

// buildRegistry walks every section's ROOT frame rows. Nested drill frames
// are intentionally not indexed — the row that leads to them is. Sections
// whose root is an empty collection (e.g. Providers/Hooks with a default
// config) register a section-level hit so they stay findable; its fieldID
// is empty and jumpTo just opens the section.
func buildRegistry(specs []sectionSpec, panes []*paneStack) []searchHit {
	var out []searchHit
	for i, sp := range specs {
		rows := panes[i].stack[0].list.Rows()
		if len(rows) == 0 {
			out = append(out, searchHit{sectionIdx: i, sectionTitle: sp.title, fieldTitle: sp.title})
			continue
		}
		for _, f := range rows {
			out = append(out, searchHit{
				sectionIdx: i, fieldID: f.id,
				sectionTitle: sp.title, fieldTitle: f.title,
				keywords: f.keywords,
			})
		}
	}
	return out
}

// fuzzyFilter matches query against "section field keywords" via the shared
// fuzzy.Rank matcher.
func fuzzyFilter(hits []searchHit, query string) []searchHit {
	hay := make([]string, len(hits))
	for i, h := range hits {
		hay[i] = h.sectionTitle + " " + h.fieldTitle + " " + strings.Join(h.keywords, " ")
	}
	idx := fuzzy.Rank(query, hay)
	out := make([]searchHit, 0, len(idx))
	for _, i := range idx {
		out = append(out, hits[i])
	}
	return out
}

type searchState struct {
	input    textinput.Model
	registry []searchHit
	results  []searchHit
	cursor   int
}

func (m *Model) openSearch() {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ti.Focus()
	reg := buildRegistry(m.specs, m.panes)
	m.search = searchState{input: ti, registry: reg, results: reg}
	m.overlay = overlaySearch
}

func (m *Model) updateSearch(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.overlay = overlayNone
		return nil
	case "up", "ctrl+k":
		if m.search.cursor > 0 {
			m.search.cursor--
		}
		return nil
	case "down", "ctrl+j", "tab":
		if m.search.cursor < len(m.search.results)-1 {
			m.search.cursor++
		}
		return nil
	case "enter":
		if m.search.cursor < len(m.search.results) {
			m.jumpTo(m.search.results[m.search.cursor])
		}
		m.overlay = overlayNone
		return nil
	}
	var cmd tea.Cmd
	m.search.input, cmd = m.search.input.Update(k)
	m.search.results = fuzzyFilter(m.search.registry, m.search.input.Value())
	m.search.cursor = 0
	return cmd
}

func (m *Model) jumpTo(h searchHit) {
	m.cursor = h.sectionIdx
	pane := m.activePane()
	for pane.pop() {
	} // reset to root
	m.paneFocused = true
	if h.fieldID == "" {
		return // section-level hit: opening the section is the jump
	}
	for i, f := range pane.top().list.Rows() {
		if f.id == h.fieldID {
			pane.top().list.SetCursor(i)
			return
		}
	}
}

func (m Model) searchOverlay(fw, fh int) string {
	var b strings.Builder
	b.WriteString("/ " + m.search.input.View() + "\n")
	b.WriteString(footerTextStyle.Render(strings.Repeat("─", max(fw/2-2, 10))) + "\n")
	maxRows := min(len(m.search.results), fh-6)
	for i := 0; i < maxRows; i++ {
		h := m.search.results[i]
		marker := "  "
		line := h.sectionTitle + " › " + h.fieldTitle
		if i == m.search.cursor {
			marker = "▸ "
			line = sidebarActiveStyle.Render(line)
		}
		b.WriteString(marker + line + "\n")
	}
	if len(m.search.results) == 0 {
		b.WriteString(footerTextStyle.Render("  no matches") + "\n")
	}
	panel := renderPanel("Jump to setting", strings.TrimRight(b.String(), "\n"),
		max(fw/2, 40), min(fh, maxRows+5), true)
	return lipgloss.NewStyle().Width(fw).Height(fh).Align(lipgloss.Center, lipgloss.Center).Render(panel)
}
