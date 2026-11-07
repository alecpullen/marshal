package acp

import (
	"context"
	"encoding/json"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/viewmodel"
)

// StackNodeParams is the session/stack_node request body.
type StackNodeParams = acpwire.StackNodeParams

// treeFor builds the full view-model tree of a session or subagent.
func (m *TurnManager) treeFor(sessionID string, subagentID int64) (*TurnRuntime, *session.State, []*viewmodel.Node, error) {
	rt, ok := m.readLookup()(sessionID)
	if !ok || rt.State == nil {
		return nil, nil, nil, serverErrorf("unknown session: %s", sessionID)
	}
	src, drilled, err := m.stackSource(rt, subagentID)
	if err != nil {
		return nil, nil, nil, err
	}
	busy := m.stackTurnBusy(sessionID)
	if drilled {
		if v, ok := rt.State.Subagent(subagentID); ok {
			busy = v.Status == session.SubagentRunning
		}
	}
	return rt, src, viewmodel.Build(viewmodel.SnapshotOf(src, busy, time.Now())), nil
}

// StackNode handles session/stack_node: one node's wire form plus the detail
// the snapshot caps or omits.
func (m *TurnManager) StackNode(ctx context.Context, params json.RawMessage) (any, error) {
	var p StackNodeParams
	if err := decodeParams(params, &p, "session/stack_node"); err != nil {
		return nil, err
	}
	if p.SessionID == "" || p.NodeID == "" {
		return nil, invalidParamsError("session/stack_node requires sessionId and nodeId")
	}
	_, state, tree, err := m.treeFor(p.SessionID, p.SubagentID)
	if err != nil {
		return nil, err
	}
	res, found := viewmodel.StackNodeOf(state, tree, p.NodeID)
	if !found {
		return nil, invalidParamsError("unknown node: %s", p.NodeID)
	}
	return res, nil
}
