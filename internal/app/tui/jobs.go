package tui

// workLaneActive reports whether the Work list has anything to show. It is
// true once the session serves work or a non-empty snapshot has arrived. The
// list reads only the immutable snapshot while it is true.
func (m Model) workLaneActive() bool {
	return m.workAvailable() || len(m.workSnapshot.Items) > 0
}
