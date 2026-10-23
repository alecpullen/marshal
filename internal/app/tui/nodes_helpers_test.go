package tui

import (
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// thinkID is the node ID of a thinking row that started at ts.
func thinkID(ts time.Time) viewmodel.NodeID {
	return viewmodel.ThinkingID(&session.ThinkingEntry{StartedAt: ts})
}

// beatWithThinking is the beat holding the reasoning that started at ts, from
// the last refresh. Reasoning is part of its beat: expanding the beat shows it.
func beatWithThinking(m *Model, ts time.Time) viewmodel.NodeID {
	for id, n := range m.browseTree {
		if n.Kind != viewmodel.KindStep || n.Step == nil {
			continue
		}
		for _, th := range n.Step.Thinking {
			if th.StartedAt.Equal(ts) {
				return id
			}
		}
	}
	return viewmodel.NodeID{}
}

// toolIDAt is the node ID of an audit row with no call ID, logged at ts.
func toolIDAt(ts time.Time) viewmodel.NodeID {
	return viewmodel.ToolID(registry.AuditEvent{Timestamp: ts})
}

// regionOf returns the narrowest recorded region whose target is id.
func regionOf(m *Model, id viewmodel.NodeID) (nodeRegion, bool) {
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
