package tui

import "marshal/internal/viewmodel"

// isExpanded reports whether a node reads as fully expanded, ignoring its
// ancestors' overrides. Tests use it to assert click and Enter results.
func (m *Model) isExpanded(id viewmodel.NodeID) bool {
	return m.densityOf(id, m.density) == densityFull
}
