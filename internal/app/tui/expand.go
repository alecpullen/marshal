package tui

import (
	"fmt"

	"marshal/internal/app/tui/stack"
)

// expandKey scopes a node ID to the transcript it is shown in. Every session
// numbers its own steps from 1, so without the scope, expanding step 3 of the
// main transcript would also expand step 3 of a subagent's, and toggling it
// there would flip the main one.
func (m *Model) expandKey(id stack.NodeID) stack.NodeID {
	if v, ok := m.drilledInto(); ok {
		return stack.NodeID{Kind: id.Kind, Key: fmt.Sprintf("sub%d/%s", v.ID, id.Key)}
	}
	return id
}

// isExpanded reports the effective expanded state for a node: the per-node
// override if one has been clicked, otherwise the global ctrl+g default.
func (m *Model) isExpanded(id stack.NodeID) bool {
	if v, ok := m.expanded[m.expandKey(id)]; ok {
		return v
	}
	return m.detailExpanded
}

// toggleExpanded flips a node's effective state and records it as an
// override, so it no longer tracks the global default until the next ctrl+g
// (see the ctrl+g case in keypress.go, which clears m.expanded).
func (m *Model) toggleExpanded(id stack.NodeID) {
	if m.expanded == nil {
		m.expanded = map[stack.NodeID]bool{}
	}
	m.expanded[m.expandKey(id)] = !m.effectiveExpanded(id)
}

// effectiveExpanded is what the row on screen shows: an in-flight call
// ignores the global default, so toggling it must flip from there, or ctrl+g
// would make the first click a no-op.
func (m *Model) effectiveExpanded(id stack.NodeID) bool {
	if id.Kind == stack.KindTool {
		if st, _ := m.transcriptSource(); st != nil {
			for _, a := range st.ActiveToolCalls() {
				if stack.ActiveToolID(a) == id {
					return m.isToolExpanded(id, true)
				}
			}
		}
	}
	return m.isExpanded(id)
}

// isToolExpanded is isExpanded for an in-flight tool row. A running call
// stays collapsed unless clicked, whatever the global default says: its
// output is a live tail, not history. The override carries over when the call
// settles, because a call's node ID is the same before and after.
func (m *Model) isToolExpanded(id stack.NodeID, live bool) bool {
	if live {
		return m.expanded[m.expandKey(id)]
	}
	return m.isExpanded(id)
}
