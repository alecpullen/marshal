package viewmodel

import (
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// SnapshotOf is what Build reads from st at now. busy says a turn is running.
// A running call or streaming reasoning makes the snapshot busy too.
func SnapshotOf(st *session.State, busy bool, now time.Time) Snapshot {
	active := st.ActiveToolCalls()
	inProgress := st.InProgress()
	return Snapshot{
		Items: st.Transcript(), Steps: st.Steps(), Todos: st.Todos(),
		ActiveTools: active, InProgress: inProgress,
		Busy: busy || len(active) > 0 || len(inProgress.Reasoning) > 0,
		Now:  now,
	}
}

// Projection is one transcript, built and projected for the wire.
type Projection struct {
	Turns []*Node          // the built tree
	Band  BandInfo         // the band, from the same snapshot
	Tree  acpwire.WireTree // Turns projected, then the band node, which is not in Roots
}

// ProjectState builds st's transcript at now and projects it: the sequence
// session/stack serves, and the TUI renders from plan 3c on.
func ProjectState(st *session.State, busy bool, now time.Time) Projection {
	snap := SnapshotOf(st, busy, now)
	turns := Build(snap)
	tree := Project(turns, st.Workspace().ActiveRoot)
	band := BandOf(snap, turns)
	tree.Nodes = append(tree.Nodes, ProjectBand(band))
	return Projection{Turns: turns, Band: band, Tree: tree}
}
