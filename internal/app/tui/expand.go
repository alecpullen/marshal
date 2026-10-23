package tui

import (
	"fmt"

	"marshal/internal/viewmodel"
)

// The transcript has one level of detail. Every row draws collapsed until it
// is expanded: one node at a time with Enter or a click, or every node with
// Ctrl+G. A node without a choice of its own follows its beat's, and a beat
// without one follows Ctrl+G, so expanding a beat opens everything in it.

// expandKey scopes a node ID to the transcript it is shown in. Every session
// numbers its own steps from 1, so without the scope, expanding step 3 of the
// main transcript would also expand step 3 of a subagent's, and toggling it
// there would flip the main one.
func (m *Model) expandKey(id viewmodel.NodeID) viewmodel.NodeID {
	if v, ok := m.drilledInto(); ok {
		return viewmodel.NodeID{Kind: id.Kind, Key: fmt.Sprintf("sub%d/%s", v.ID, id.Key)}
	}
	return id
}

// ownExpanded returns the node's own choice, if the user made one.
func (m *Model) ownExpanded(id viewmodel.NodeID) (expanded, ok bool) {
	expanded, ok = m.expanded[m.expandKey(id)]
	return expanded, ok
}

// expandedUnder resolves whether a node draws expanded: its own choice, else
// what it inherits from its beat or from Ctrl+G.
func (m *Model) expandedUnder(id viewmodel.NodeID, inherited bool) bool {
	if e, ok := m.ownExpanded(id); ok {
		return e
	}
	return inherited
}

// isExpanded reports whether a node draws expanded: its own choice, else its
// beat's, else Ctrl+G's.
func (m *Model) isExpanded(id viewmodel.NodeID) bool {
	inherited := m.expandAll
	if beat, ok := m.rowBeat[id]; ok {
		inherited = m.expandedUnder(beat, inherited)
	}
	return m.expandedUnder(id, inherited)
}

// isToolExpanded reports whether a tool row is shown expanded. A running call
// stays collapsed unless it was opened itself, whatever its beat or Ctrl+G
// says: its output is a live tail, not history. The choice carries over when
// the call settles, because a call's node ID is the same before and after.
func (m *Model) isToolExpanded(id viewmodel.NodeID, live bool) bool {
	if live {
		e, ok := m.ownExpanded(id)
		return ok && e
	}
	return m.isExpanded(id)
}

func (m *Model) setExpanded(id viewmodel.NodeID, e bool) {
	if m.expanded == nil {
		m.expanded = map[viewmodel.NodeID]bool{}
	}
	m.expanded[m.expandKey(id)] = e
}

// toggleExpanded is the click and Enter action: it flips what the node shows
// now.
func (m *Model) toggleExpanded(id viewmodel.NodeID) {
	if m.isLiveToolNode(id) {
		m.setExpanded(id, !m.isToolExpanded(id, true))
		return
	}
	m.setExpanded(id, !m.isExpanded(id))
}

// toggleExpandAll is Ctrl+G. It drops every per-node choice, so the whole
// transcript reads one way.
func (m *Model) toggleExpandAll() {
	m.expandAll = !m.expandAll
	m.expanded = map[viewmodel.NodeID]bool{}
}

// resetExpansion collapses everything and forgets every choice, for a new
// session.
func (m *Model) resetExpansion() {
	m.expandAll = false
	m.expanded = map[viewmodel.NodeID]bool{}
	m.invalidateTranscript()
}

func (m *Model) isLiveToolNode(id viewmodel.NodeID) bool {
	if id.Kind != viewmodel.KindTool {
		return false
	}
	st, _ := m.transcriptSource()
	if st == nil {
		return false
	}
	for _, a := range st.ActiveToolCalls() {
		if viewmodel.ActiveToolID(a) == id {
			return true
		}
	}
	return false
}
