package tui

import "marshal/internal/app/tui/stack"

// isExpanded reports the effective expanded state for a node: the per-node
// override if one has been clicked, otherwise the global ctrl+g default.
func (m *Model) isExpanded(id stack.NodeID) bool {
	if v, ok := m.expanded[id]; ok {
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
	m.expanded[id] = !m.effectiveExpanded(id)
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
		return m.expanded[id]
	}
	return m.isExpanded(id)
}
