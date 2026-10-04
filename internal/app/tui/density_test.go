package tui

import "marshal/internal/app/tui/stack"

// isExpanded reports whether a node reads as fully expanded, ignoring its
// ancestors' overrides. Tests use it to assert click and Enter results.
func (m *Model) isExpanded(id stack.NodeID) bool {
	return m.densityOf(id, m.density) == densityFull
}
