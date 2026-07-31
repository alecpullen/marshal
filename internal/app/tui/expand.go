package tui

import (
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// itemKey identifies a transcript item for per-item expand/collapse state.
// session.State.Transcript() rebuilds its []TranscriptItem slice fresh on
// every call (merging messages/audit/thinking sorted by timestamp), so a
// slice index is not a stable identity across renders — but items are
// append-only and their Timestamp never changes, so (Timestamp, Kind)
// is.
type itemKey struct {
	ts   time.Time
	kind session.TranscriptKind
}

// itemKeyFor returns the identity key for a single transcript item.
func itemKeyFor(item *session.TranscriptItem) itemKey {
	return itemKey{ts: item.Timestamp, kind: item.Kind}
}

// itemKeyForGroup returns the identity key for a collapsed run of merged
// audit events (see groupTranscript), using the first event in the run —
// the group's own rendering never changes which event is first once merged.
func itemKeyForGroup(events []registry.AuditEvent) itemKey {
	return itemKey{ts: events[0].Timestamp, kind: session.KindAudit}
}

// isExpanded reports the effective expanded state for key: the per-item
// override if one has been clicked, otherwise the global ctrl+g default.
func (m *Model) isExpanded(key itemKey) bool {
	if v, ok := m.itemExpanded[key]; ok {
		return v
	}
	return m.detailExpanded
}

// toggleItemExpanded flips key's effective state and records it as an
// override, so it no longer tracks the global default until the next
// ctrl+g (see the ctrl+g case in keypress.go, which clears m.itemExpanded).
func (m *Model) toggleItemExpanded(key itemKey) {
	if m.itemExpanded == nil {
		m.itemExpanded = map[itemKey]bool{}
	}
	m.itemExpanded[key] = !m.isExpanded(key)
}
