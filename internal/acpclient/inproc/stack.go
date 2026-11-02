package inproc

import (
	"marshal/internal/acpwire"
	"marshal/internal/viewmodel"
)

// Stack is the transcript the TUI renders (C6). It reads like StackNode: the
// session's own transcript for 0, a child's anywhere in the tree for any other
// ID. The tree is viewmodel.ProjectState's, built with busy at the source
// state's clock. A closed session still answers, as Status and Sheet do.
func (s *Session) Stack(subagentID int64, busy bool) acpwire.StackSnapshot {
	src := s.state
	if subagentID != 0 {
		src = childState(s.state, subagentID)
		if src == nil {
			return acpwire.StackSnapshot{
				SessionID:  s.state.SessionID(),
				SubagentID: subagentID,
			}
		}
	}
	tree := viewmodel.ProjectState(src, busy, src.Now()).Tree
	return acpwire.StackSnapshot{
		SessionID:  s.state.SessionID(),
		SubagentID: subagentID,
		Rev:        0,
		Roots:      tree.Roots,
		Nodes:      tree.Nodes,
	}
}
