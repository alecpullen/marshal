package service

import (
	"context"

	"marshal/internal/acpwire"
	"marshal/internal/viewmodel"
)

// Stack is the transcript the TUI renders (C6). It reads like StackNode: the
// session's own transcript for 0, a child's anywhere in the tree for any other
// ID. The tree is viewmodel.ProjectState's, built with busy at the source
// state's clock. A closed session still answers, as Status and Sheet do.
//
// Each transcript is projected through a memo, so an unchanged node is handed
// back as it was: reused nodes share their slices and pointers with earlier
// reads, and a caller must not change what it is given.
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

	s.stackMu.Lock()
	memo := &s.mainMemo
	if subagentID != 0 {
		if s.subMemoID != subagentID {
			// The TUI reads one drilled subagent at a time: the memo serves
			// whoever was read last, so another child starts it over.
			s.subMemo = viewmodel.WireMemo{}
			s.subMemoID = subagentID
		}
		memo = &s.subMemo
	}
	tree := memo.ProjectState(src, busy, src.Now()).Tree
	s.stackMu.Unlock()

	return acpwire.StackSnapshot{
		SessionID:  s.state.SessionID(),
		SubagentID: subagentID,
		Rev:        0,
		Roots:      tree.Roots,
		Nodes:      tree.Nodes,
	}
}

// ReleaseSubagent drops the subagent's projection memo, if it is the one
// the session keeps. The next read of the subagent starts the memo afresh.
// Releasing 0, the session's own transcript, does nothing.
func (s *Session) ReleaseSubagent(ctx context.Context, subagentID int64) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if subagentID == 0 {
		return nil
	}
	s.stackMu.Lock()
	defer s.stackMu.Unlock()
	if s.subMemoID == subagentID {
		s.subMemo = viewmodel.WireMemo{}
		s.subMemoID = 0
	}
	return nil
}
