package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// browseItem is one navigable node and the content lines it occupies. The
// list is rebuilt on every full refresh from the rendered tree, so it always
// matches what is on screen.
type browseItem struct {
	id         viewmodel.NodeID
	kind       viewmodel.Kind
	start, end int  // content lines, end exclusive
	jump       bool // a stop for J/K: a task header or a turn's first node
}

// navigable reports whether browse mode can put the cursor on a node.
func navigable(k viewmodel.Kind) bool {
	switch k {
	case viewmodel.KindStep, viewmodel.KindTool, viewmodel.KindSubagent, viewmodel.KindMessage,
		viewmodel.KindFinal, viewmodel.KindRunEvent, viewmodel.KindCompletion, viewmodel.KindPassthrough, viewmodel.KindReceipt:
		return true
	}
	return false
}

// collectBrowse appends the navigable nodes of one rendered top-level block.
// A beat that opens a todo is a J/K stop; its steps and their rows come from
// the sub-regions the renderer recorded.
func collectBrowse(items []browseItem, n *viewmodel.Node, out string, subs []subRegion, base int, turnFirst bool) []browseItem {
	lines := strings.Count(out, "\n")
	if navigable(n.Kind) {
		labelled := n.Kind == viewmodel.KindStep && n.Step != nil && n.Step.Todo != nil
		items = append(items, browseItem{id: n.ID, kind: n.Kind, start: base, end: base + lines, jump: turnFirst || labelled})
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
func indexTree(n *viewmodel.Node, into map[viewmodel.NodeID]*viewmodel.Node) {
	into[n.ID] = n
	for _, c := range n.Children {
		indexTree(c, into)
	}
}

func (m *Model) setBrowseItems(items []browseItem, tree map[viewmodel.NodeID]*viewmodel.Node) {
	prev := m.cursorIndex()
	// Browse continues past the last history row into the box's stops. They
	// own no content lines (the box paints its own cursor), so start and end
	// are -1 and they are never J/K stops.
	for _, t := range m.boxLayout().Stops {
		items = append(items, browseItem{id: boxStopID(t), kind: viewmodel.KindPassthrough, start: -1, end: -1})
	}
	m.browseItems, m.browseTree = items, tree
	if !m.browsing {
		return
	}
	if len(items) == 0 {
		m.browsing = false
		m.input.Focus()
		return
	}
	switch {
	case m.viewportFollow:
		// Following new output: the cursor goes with it, or it would scroll
		// out of view while the view moves on.
		m.cursor = items[len(items)-1].id
	case m.cursorIndex() < 0:
		// The node is gone (a rewind): stay near where it was
		// rather than jumping to the end of the transcript.
		m.cursor = items[max(0, min(prev, len(items)-1))].id
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
		if m.browseItems[i].kind == viewmodel.KindStep {
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
	if it.start < 0 {
		// A box stop has no content lines; the box keeps its own cursor in
		// view and the viewport stays where it is.
		return
	}
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
	// Reading earlier output must not be undone by a refresh snapping the
	// viewport back to the bottom; only G on a live node re-enables follow.
	m.viewportFollow = false
	m.invalidateTranscript()
	m.refreshViewport()
	m.scrollToCursor()
}

// jumpCursor moves to the next or previous stop (a labelled beat, or the
// first node of a turn).
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
	m.viewportFollow = false
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
		// The live part is no longer a history node, so nothing here can
		// re-arm following: G always does.
		m.viewportFollow = true
		m.viewport.GotoBottom()
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
		if t, ok := boxTargetOf(m.cursor); ok {
			return *m, m.enterBoxStop(t), true
		}
		m.toggleExpanded(m.cursor)
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
		// A box stop names its child by SourceID, so it is resolved before
		// subagentAtCursor — which finds no node for a stop.
		if v, ok := m.boxAgentAtCursor(); ok {
			m.drillIntoSubagent(v)
			m.leaveBrowse()
			return *m, nil, true
		}
		if v, ok := m.subagentAtCursor(); ok {
			m.drillIntoSubagent(v)
			m.leaveBrowse()
		}
		return *m, nil, true
	case "?":
		return m.openBrowseHelp()
	case "s":
		// Stop is offered only on a running box stop whose item allows it.
		// Anywhere else the key stays unbound, as it was before.
		if cmd, ok := m.boxStopAtCursor(); ok {
			return *m, cmd, true
		}
		fallthrough
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

// wireNode is id's wire form from the last refresh, or nil.
func (m *Model) wireNode(id viewmodel.NodeID) *acpwire.WireNode { return m.wire[id.Key] }

// nodeDetail asks the session for id's detail, in the drilled-in subagent's
// transcript while the view is drilled in. ok is false when the call fails.
func (m *Model) nodeDetail(id viewmodel.NodeID) (acpwire.StackNodeResult, bool) {
	res, err := m.sess.StackNode(m.ctx, id.Key, m.drilledSheetID())
	if err != nil {
		return acpwire.StackNodeResult{}, false
	}
	return res, true
}

// enterBoxStop acts on a box stop the way a click does: a running card opens
// or closes, a held row scrolls to its completion event, the more row opens the
// Work list, and a plan item scrolls to the first beat bound to it.
func (m *Model) enterBoxStop(t livebox.Target) tea.Cmd {
	switch t.Kind {
	case livebox.TargetWork:
		return m.activateWorkRow(t.Key)
	case livebox.TargetMore:
		m.openWork()
	case livebox.TargetPlan:
		m.scrollToPlanItem(t.Key)
	}
	return nil
}

// boxStopAtCursor returns the stop command for a box stop whose item allows a
// stop, and reports false for every other cursor.
func (m *Model) boxStopAtCursor() (tea.Cmd, bool) {
	t, ok := boxTargetOf(m.cursor)
	if !ok || t.Kind != livebox.TargetWork {
		return nil, false
	}
	it, found := m.workItemByKey(t.Key)
	if !found || !it.Capabilities.Stop {
		return nil, false
	}
	return m.boxStopCmd(t.Key), true
}

// boxAgentAtCursor resolves the running subagent a box stop names: the item's
// SourceID is the child session's subagent ID.
func (m *Model) boxAgentAtCursor() (session.SubagentView, bool) {
	t, ok := boxTargetOf(m.cursor)
	if !ok || t.Kind != livebox.TargetWork {
		return session.SubagentView{}, false
	}
	it, found := m.workItemByKey(t.Key)
	if !found || it.Kind != "agent" {
		return session.SubagentView{}, false
	}
	id, err := strconv.ParseInt(it.SourceID, 10, 64)
	if err != nil {
		return session.SubagentView{}, false
	}
	v, ok := m.state.Subagent(id)
	return v, ok && v.Child != nil
}

// subagentAtCursor resolves the subagent the cursor points at: the completion
// event of a finished child, or the agent.run row that dispatched it.
func (m *Model) subagentAtCursor() (session.SubagentView, bool) {
	w := m.wireNode(m.cursor)
	if w == nil {
		return session.SubagentView{}, false
	}
	id := subagentIDOf(w)
	if id == 0 && w.Kind == acpwire.KindStep {
		for _, key := range w.Children {
			if id = subagentIDOf(m.wire[key]); id > 0 {
				break
			}
		}
	}
	if id == 0 {
		return session.SubagentView{}, false
	}
	src, _ := m.transcriptSource()
	v, ok := src.Subagent(id)
	return v, ok && v.Child != nil
}

// subagentIDOf is the subagent a wire node names: a completion event for a
// finished child, or the single agent.run call that dispatched one.
func subagentIDOf(w *acpwire.WireNode) int64 {
	if w == nil {
		return 0
	}
	if w.Completion != nil {
		return w.Completion.SubagentID
	}
	if w.Kind == acpwire.KindTool && w.Tool != nil && len(w.Tool.Calls) == 1 {
		return w.Tool.SubagentID
	}
	return 0
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
	if cur.start < 0 {
		// A box stop has no lines in the transcript: the box paints its own
		// cursor inside its frame.
		return content
	}
	lines := strings.Split(content, "\n")
	th := theme.Current()
	w := max(m.viewport.Width(), 1)
	marker := lipgloss.NewStyle().Foreground(th.AccentSecondary).Bold(true).Render("▸")
	sel := func(l string) string { return chrome.PaintBand(l, w, th.BGSelection) }
	over := func(l string) string { return chrome.PaintBand(l, w, th.BGOverlay) }

	// A step's own rows get the lighter overlay.
	lighter := map[int]bool{}
	if cur.kind == viewmodel.KindStep {
		for _, it := range m.browseItems {
			if (it.kind == viewmodel.KindTool || it.kind == viewmodel.KindSubagent) && it.start >= cur.start && it.end <= cur.end {
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
