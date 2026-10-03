package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/stack"
	"marshal/internal/app/tui/theme"
)

// browseItem is one navigable node and the content lines it occupies. The
// list is rebuilt on every full refresh from the rendered tree, so it always
// matches what is on screen.
type browseItem struct {
	id         stack.NodeID
	kind       stack.Kind
	start, end int  // content lines, end exclusive
	jump       bool // a stop for J/K: a task header or a turn's first node
}

// navigable reports whether browse mode can put the cursor on a node.
func navigable(k stack.Kind) bool {
	switch k {
	case stack.KindTask, stack.KindStep, stack.KindTool, stack.KindSubagent, stack.KindMessage,
		stack.KindFinal, stack.KindRunEvent, stack.KindJobExit, stack.KindPassthrough, stack.KindReceipt:
		return true
	}
	return false
}

// collectBrowse appends the navigable nodes of one rendered top-level block.
// A task contributes its header line only; its steps and their rows come from
// the sub-regions the renderer recorded.
func collectBrowse(items []browseItem, n *stack.Node, out string, subs []subRegion, base int, turnFirst bool) []browseItem {
	lines := strings.Count(out, "\n")
	if navigable(n.Kind) {
		end := base + lines
		if n.Kind == stack.KindTask {
			end = base + 1
		}
		items = append(items, browseItem{id: n.ID, kind: n.Kind, start: base, end: end, jump: n.Kind == stack.KindTask || turnFirst})
	}
	for _, sr := range subs {
		if !navigable(sr.id.Kind) {
			continue
		}
		items = append(items, browseItem{id: sr.id, kind: sr.id.Kind, start: base + sr.start, end: base + sr.end})
	}
	return items
}

// indexTree records every node of a block by ID, for copy/open/inspect.
func indexTree(n *stack.Node, into map[stack.NodeID]*stack.Node) {
	into[n.ID] = n
	for _, c := range n.Children {
		indexTree(c, into)
	}
}

func (m *Model) setBrowseItems(items []browseItem, tree map[stack.NodeID]*stack.Node) {
	m.browseItems, m.browseTree = items, tree
	m.browseNodes = make([]stack.NodeID, len(items))
	for i, it := range items {
		m.browseNodes[i] = it.id
	}
	if m.browsing && m.cursorIndex() < 0 {
		if len(items) == 0 {
			m.browsing = false
			m.input.Focus()
			return
		}
		m.cursor = items[len(items)-1].id
	}
}

func (m *Model) cursorIndex() int {
	for i, it := range m.browseItems {
		if it.id == m.cursor {
			return i
		}
	}
	return -1
}

// enterBrowse puts the cursor on the newest step (or the newest node when
// there is no step), blurs the input without touching its text, and stops
// following the bottom. It reports false when there is nothing to browse.
func (m *Model) enterBrowse() bool {
	if len(m.browseItems) == 0 {
		return false
	}
	pick := m.browseItems[len(m.browseItems)-1].id
	for i := len(m.browseItems) - 1; i >= 0; i-- {
		if m.browseItems[i].kind == stack.KindStep {
			pick = m.browseItems[i].id
			break
		}
	}
	m.browsing = true
	m.cursor = pick
	m.input.Blur()
	m.viewportFollow = false
	m.invalidateTranscript()
	m.refreshViewport()
	m.scrollToCursor()
	return true
}

// leaveBrowse refocuses the input and keeps the viewport where it is. Follow
// comes back only if the viewport is already at the bottom.
func (m *Model) leaveBrowse() {
	if !m.browsing {
		return
	}
	m.browsing = false
	m.input.Focus()
	m.viewportFollow = m.viewport.AtBottom()
	m.invalidateTranscript()
	m.refreshViewport()
}

// scrollToCursor brings the cursor node into view: its first line at least,
// and the whole node when it fits.
func (m *Model) scrollToCursor() {
	i := m.cursorIndex()
	if i < 0 {
		return
	}
	it := m.browseItems[i]
	top, h := m.viewport.YOffset(), m.viewport.Height()
	switch {
	case it.start < top:
		m.viewport.SetYOffset(it.start)
	case it.end > top+h:
		if it.end-it.start <= h {
			m.viewport.SetYOffset(it.end - h)
		} else {
			m.viewport.SetYOffset(it.start)
		}
	}
}

func (m *Model) moveCursor(to int) {
	if len(m.browseItems) == 0 {
		return
	}
	to = max(0, min(to, len(m.browseItems)-1))
	m.cursor = m.browseItems[to].id
	m.invalidateTranscript()
	m.refreshViewport()
	m.scrollToCursor()
}

// jumpCursor moves to the next or previous stop (a task header, or the first
// node of a turn).
func (m *Model) jumpCursor(dir int) {
	i := m.cursorIndex()
	if i < 0 {
		return
	}
	for j := i + dir; j >= 0 && j < len(m.browseItems); j += dir {
		if m.browseItems[j].jump {
			m.moveCursor(j)
			return
		}
	}
	if dir > 0 {
		m.moveCursor(len(m.browseItems) - 1)
	} else {
		m.moveCursor(0)
	}
}

// cursorToVisible moves the cursor to the first node that is fully on screen,
// after a page scroll.
func (m *Model) cursorToVisible() {
	top, h := m.viewport.YOffset(), m.viewport.Height()
	for i, it := range m.browseItems {
		if it.start >= top && it.end <= top+h {
			m.moveCursorNoScroll(i)
			return
		}
	}
}

func (m *Model) moveCursorNoScroll(i int) {
	m.cursor = m.browseItems[i].id
	m.invalidateTranscript()
	m.refreshViewport()
}

// handleBrowseKey owns the keys of browse mode. It returns handled=false for
// keys it leaves to the rest of the app (Ctrl chords, function keys), and for
// an unbound printable key after leaving browse mode, so that key is typed.
func (m *Model) handleBrowseKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	key := msg.String()
	switch key {
	case "esc":
		m.leaveBrowse()
		return *m, nil, true
	case "j", "down":
		m.moveCursor(m.cursorIndex() + 1)
	case "k", "up":
		m.moveCursor(m.cursorIndex() - 1)
	case "J", "]":
		m.jumpCursor(1)
	case "K", "[":
		m.jumpCursor(-1)
	case "g", "home":
		m.moveCursor(0)
	case "G", "end":
		m.moveCursor(len(m.browseItems) - 1)
		if last := m.currentNode(); last != nil && last.AnyLive() {
			m.viewportFollow = true
			m.viewport.GotoBottom()
		}
	case "pgup", "ctrl+u":
		m.viewport.PageUp()
		m.cursorToVisible()
	case "pgdown", "ctrl+d":
		m.viewport.PageDown()
		m.cursorToVisible()
	case "enter":
		if len(m.browseItems) == 0 {
			m.leaveBrowse()
			return *m, nil, true
		}
		if v, ok := m.subagentAtCursor(true); ok {
			m.drillIntoSubagent(v)
			m.leaveBrowse()
			return *m, nil, true
		}
		m.cycleDensity(m.cursor)
		m.invalidateTranscript()
		m.refreshViewport()
		m.scrollToCursor()
	case "i":
		return m.openInspector()
	case "y":
		cmd := m.copyCursorNode()
		return *m, cmd, true
	case "o":
		cmd := m.openCursorFile()
		return *m, cmd, true
	case "f":
		if v, ok := m.subagentAtCursor(false); ok {
			m.drillIntoSubagent(v)
			m.leaveBrowse()
		}
		return *m, nil, true
	case "z":
		m.foldTasks = !m.foldTasks
		word := "off"
		if m.foldTasks {
			word = "on"
		}
		cmd := m.setFlash("Fold finished tasks: " + word)
		m.invalidateTranscript()
		m.refreshViewport()
		m.scrollToCursor()
		return *m, cmd, true
	case "?":
		return m.openBrowseHelp()
	default:
		if isPrintable(msg) {
			// Unbound: leave browse mode and let the key be typed.
			m.leaveBrowse()
			return *m, nil, false
		}
		return *m, nil, false
	}
	return *m, nil, true
}

// isPrintable reports whether a key press would insert text.
func isPrintable(msg tea.KeyPressMsg) bool {
	if msg.Text == "" {
		return false
	}
	return msg.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModMeta|tea.ModSuper) == 0
}

func (m *Model) currentNode() *stack.Node { return m.browseTree[m.cursor] }

// subagentAtCursor finds the subagent card under the cursor, or (when
// owning is set, or the cursor is on a step) the first card of the cursor's
// step.
func (m *Model) subagentAtCursor(onlyCard bool) (session.SubagentView, bool) {
	n := m.currentNode()
	if n == nil {
		return session.SubagentView{}, false
	}
	if n.Kind == stack.KindSubagent && n.Item != nil && n.Item.Subagent != nil && n.Item.Subagent.Child != nil {
		return *n.Item.Subagent, true
	}
	if onlyCard {
		return session.SubagentView{}, false
	}
	for _, c := range n.Children {
		if c.Kind == stack.KindSubagent && c.Item != nil && c.Item.Subagent != nil && c.Item.Subagent.Child != nil {
			return *c.Item.Subagent, true
		}
	}
	return session.SubagentView{}, false
}

// paintCursor highlights the cursor node's lines in the joined transcript.
// It runs after the cached blocks are joined, so moving the cursor never
// invalidates the render cache. With no colour to paint, a marker in the
// gutter column carries the cursor.
func (m *Model) paintCursor(content string) string {
	i := m.cursorIndex()
	if i < 0 {
		return content
	}
	cur := m.browseItems[i]
	lines := strings.Split(content, "\n")
	th := theme.Current()
	w := max(m.viewport.Width(), 1)
	marker := lipgloss.NewStyle().Foreground(th.AccentSecondary).Bold(true).Render("▸")
	sel := func(l string) string { return chrome.PaintBand(l, w, th.BGSelection) }
	over := func(l string) string { return chrome.PaintBand(l, w, th.BGOverlay) }

	// A step's own rows get the lighter overlay.
	lighter := map[int]bool{}
	if cur.kind == stack.KindStep || cur.kind == stack.KindTask {
		for _, it := range m.browseItems {
			if (it.kind == stack.KindTool || it.kind == stack.KindSubagent) && it.start >= cur.start && it.end <= cur.end {
				for l := it.start; l < it.end; l++ {
					lighter[l] = true
				}
			}
		}
	}
	for l := cur.start; l < cur.end && l < len(lines); l++ {
		if l < 0 {
			continue
		}
		if lighter[l] {
			lines[l] = over(lines[l])
			continue
		}
		lines[l] = sel(lines[l])
	}
	if cur.start >= 0 && cur.start < len(lines) {
		line := lines[cur.start]
		lines[cur.start] = marker + ansi.Cut(line, 1, max(ansi.StringWidth(line), 1))
	}
	return strings.Join(lines, "\n")
}

// plainText strips ANSI from copied text.
func plainText(s string) string { return strings.TrimRight(stripANSI(s), "\n") }
