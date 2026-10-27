package inproc

import (
	"context"
	"fmt"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// StackNode is one transcript node's wire form with the detail the projection
// caps or leaves out (C6). subagentID 0 reads the session's transcript, and
// any other ID the transcript of the subagent with that ID anywhere in the
// session's tree. It fails for an ID that names no child, and for a node the
// transcript does not hold.
//
// The tree is built as not busy: busy decides only which beat is live and
// whether the turn's receipt exists, and neither a node's ID nor its detail
// depends on it.
func (s *Session) StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error) {
	if s.closed.Load() {
		return acpwire.StackNodeResult{}, errSessionClosed
	}
	src := s.state
	if subagentID != 0 {
		src = childState(s.state, subagentID)
		if src == nil {
			return acpwire.StackNodeResult{}, fmt.Errorf("inproc: no subagent %d", subagentID)
		}
	}
	tree := viewmodel.Build(viewmodel.SnapshotOf(src, false, time.Now()))
	res, found := viewmodel.StackNodeOf(src, tree, nodeID)
	if !found {
		return acpwire.StackNodeResult{}, fmt.Errorf("inproc: no node %q", nodeID)
	}
	return res, nil
}

// Callers lists what references the symbol a call row changed (C6). It returns
// nil and no error when the row changed no resolved symbol, when no language
// server is wired or ready, and when the node is gone.
func (s *Session) Callers(ctx context.Context, nodeID string) ([]string, error) {
	if s.closed.Load() {
		return nil, errSessionClosed
	}
	if s.refs == nil {
		return nil, nil
	}
	var (
		ref   registry.SymbolRef
		found bool
	)
	for _, item := range s.state.Transcript() {
		if item.Kind != session.KindAudit || item.Audit == nil {
			continue
		}
		if viewmodel.ToolKey(*item.Audit) != nodeID {
			continue
		}
		ref, found = viewmodel.CallersSymbol(*item.Audit)
		break
	}
	if !found {
		return nil, nil
	}
	refs, ok := s.refs.References(ctx, ref.File, ref.Line, ref.Col)
	if !ok {
		return nil, nil
	}
	return refs, nil
}
