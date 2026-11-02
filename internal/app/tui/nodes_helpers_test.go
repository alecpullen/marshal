package tui

import (
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// thinkID is the identity of a thinking row that started at ts.
func thinkID(ts time.Time) nodeID {
	return nodeID{Kind: acpwire.KindThinking, Key: viewmodel.ThinkingKey(&session.ThinkingEntry{StartedAt: ts})}
}

// beatWithThinking is the beat holding the reasoning that started at ts, from
// the last refresh. Reasoning is part of its beat: expanding the beat shows it.
func beatWithThinking(m *Model, st *session.State, ts time.Time) nodeID {
	turns := viewmodel.Build(viewmodel.SnapshotOf(st, m.busy, m.now()))
	for _, turn := range turns {
		for _, n := range turn.Children {
			if n.Kind != viewmodel.KindStep || n.Step == nil {
				continue
			}
			for _, th := range n.Step.Thinking {
				if th.StartedAt.Equal(ts) {
					return nodeID{Kind: acpwire.KindStep, Key: n.ID.Key}
				}
			}
		}
	}
	return nodeID{}
}

// toolNodeID is the identity of the tool row ev settles into.
func toolNodeID(ev registry.AuditEvent) nodeID {
	return nodeID{Kind: acpwire.KindTool, Key: viewmodel.ToolKey(ev)}
}

// statusView is the status view Status carries for that subagent, which is
// what the drill stack holds.
func statusView(t *testing.T, m *Model, id int64) acpwire.Subagent {
	t.Helper()
	for _, v := range m.sess.Status().Subagents {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("Status carries no subagent %d", id)
	return acpwire.Subagent{}
}

// activeNodeID is the identity of a running call's row.
func activeNodeID(a session.ActiveToolCall) nodeID {
	return nodeID{Kind: acpwire.KindTool, Key: viewmodel.ActiveToolID(a).Key}
}

// toolIDAt is the node ID of an audit row with no call ID, logged at ts.
func toolIDAt(ts time.Time) nodeID {
	return nodeID{Kind: acpwire.KindTool, Key: viewmodel.ToolKey(registry.AuditEvent{Timestamp: ts})}
}

// regionOf returns the narrowest recorded region whose target is id.
func regionOf(m *Model, id nodeID) (nodeRegion, bool) {
	var best nodeRegion
	found := false
	for _, r := range m.nodeRegions {
		if r.target.node != id {
			continue
		}
		if !found || r.endLine-r.startLine < best.endLine-best.startLine {
			best, found = r, true
		}
	}
	return best, found
}
