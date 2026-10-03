package tui

import (
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/stack"
	"marshal/internal/tools/registry"
)

// thinkID is the node ID of a thinking row that started at ts.
func thinkID(ts time.Time) stack.NodeID {
	return stack.ThinkingID(&session.ThinkingEntry{StartedAt: ts})
}

// toolIDAt is the node ID of an audit row with no call ID, logged at ts.
func toolIDAt(ts time.Time) stack.NodeID {
	return stack.ToolID(registry.AuditEvent{Timestamp: ts})
}

// regionOf returns the narrowest recorded region whose target is id.
func regionOf(m *Model, id stack.NodeID) (nodeRegion, bool) {
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
