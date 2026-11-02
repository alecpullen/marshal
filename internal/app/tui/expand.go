package tui

import (
	"fmt"

	"marshal/internal/acpwire"
)

// The transcript has one level of detail. Every row draws collapsed until it
// is expanded: one node at a time with Enter or a click, or every node with
// Ctrl+G. A node without a choice of its own follows its beat's, and a beat
// without one follows Ctrl+G, so expanding a beat opens everything in it.

// expandKey scopes a node ID to the transcript it is shown in. Every session
// numbers its own steps from 1, so without the scope, expanding step 3 of the
// main transcript would also expand step 3 of a subagent's, and toggling it
// there would flip the main one.
func (m *Model) expandKey(id nodeID) nodeID {
	if v, ok := m.drilledInto(); ok {
		return nodeID{Kind: id.Kind, Key: fmt.Sprintf("sub%d/%s", v.ID, id.Key)}
	}
	return id
}

// ownExpanded returns the node's own choice, if the user made one.
func (m *Model) ownExpanded(id nodeID) (expanded, ok bool) {
	expanded, ok = m.expanded[m.expandKey(id)]
	return expanded, ok
}

// expandedUnder resolves whether a node draws expanded: its own choice, else
// what it inherits from its beat or from Ctrl+G.
func (m *Model) expandedUnder(id nodeID, inherited bool) bool {
	if e, ok := m.ownExpanded(id); ok {
		return e
	}
	return inherited
}

// isExpanded reports whether a node draws expanded: its own choice, else its
// beat's, else Ctrl+G's.
func (m *Model) isExpanded(id nodeID) bool {
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
func (m *Model) isToolExpanded(id nodeID, live bool) bool {
	if live {
		e, ok := m.ownExpanded(id)
		return ok && e
	}
	return m.isExpanded(id)
}

func (m *Model) setExpanded(id nodeID, e bool) {
	if m.expanded == nil {
		m.expanded = map[nodeID]bool{}
	}
	m.expanded[m.expandKey(id)] = e
}

// toggleExpanded is the click and Enter action: it flips what the node shows
// now.
func (m *Model) toggleExpanded(id nodeID) {
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
	m.expanded = map[nodeID]bool{}
}

// resetExpansion collapses everything and forgets every choice, for a new
// session.
func (m *Model) resetExpansion() {
	m.expandAll = false
	m.expanded = map[nodeID]bool{}
	m.invalidateTranscript()
}

func (m *Model) isLiveToolNode(id nodeID) bool {
	if id.Kind != acpwire.KindTool {
		return false
	}
	w := m.wire[id.Key]
	return w != nil && w.Tool != nil && w.Tool.Running != nil
}
