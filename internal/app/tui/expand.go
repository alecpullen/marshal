package tui

import (
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
)

// itemKey identifies a transcript item for per-item expand/collapse state,
// click regions and caller lookups.
//
// The identity is the item's ViewID, assigned by session.State (see
// viewid.go), NOT its timestamp. A timestamp is not an identity: two thinking
// entries logged inside one clock tick, and the several run events of a fast
// task, share one — so expanding one block expanded the other, and a click
// region could not say which of two colliding blocks it belonged to.
//
// A slice index is no better than a timestamp, because Transcript() rebuilds
// and re-sorts its slice on every call.
type itemKey struct {
	// viewID is the transcript item's presentation identity. It is empty only
	// for a key that was never resolved against a transcript, which no code
	// path produces: itemKeyFor always reads it from the item.
	viewID string
	// kind is retained for the synthetic live-thinking region and for
	// rendering decisions that need a kind without holding the item. It is
	// not part of the identity: a ViewID already encodes which collection
	// produced it.
	kind session.TranscriptKind
}

// itemKeyFor returns the identity key for a single transcript item.
func itemKeyFor(item *session.TranscriptItem) itemKey {
	return itemKey{viewID: item.ViewID, kind: item.Kind}
}

// itemKeyForGroup returns the identity key for a collapsed run of merged
// audit events, using the FIRST event's identity: a group's first member is
// the one thing that cannot change as the run grows, so the group keeps its
// identity while later calls join it.
//
// The caller passes the members' ViewIDs rather than the events themselves,
// because a registry.AuditEvent carries no stable identity of its own — see
// transcriptEntry.GroupIDs.
func itemKeyForGroup(memberIDs []string) itemKey {
	if len(memberIDs) == 0 {
		return itemKey{kind: session.KindAudit}
	}
	return itemKey{viewID: memberIDs[0], kind: session.KindAudit}
}

func notebookItemKey(scope string, id conversation.BlockID) itemKey {
	return itemKey{viewID: "notebook:" + scope + ":" + string(id), kind: session.KindMessage}
}

func (m *Model) toggleNotebookWorkOrder(key itemKey) {
	if m.notebookWorkReversed == nil {
		m.notebookWorkReversed = map[itemKey]bool{}
	}
	m.notebookWorkReversed[key] = !m.notebookWorkReversed[key]
}

// liveThinkingKey is the synthetic identity of the in-progress thinking
// region. It is not a transcript item, so it carries a reserved identity
// rather than an ordinal.
var liveThinkingKey = itemKey{viewID: session.ViewIDLiveThinking, kind: session.KindThinking}

// itemKeyLess orders keys deterministically for hashing.
//
// It compares the identity first: sorting by the kind alone would leave two
// keys of the same kind in map-iteration order, and an unstable hash makes
// refreshViewport rebuild the viewport on every call.
func itemKeyLess(a, b itemKey) bool {
	if a.viewID != b.viewID {
		return a.viewID < b.viewID
	}
	return a.kind < b.kind
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
// override, so it no longer tracks the global default until the next ctrl+g
// (see the ctrl+g case in keypress.go, which clears m.itemExpanded).
func (m *Model) toggleItemExpanded(key itemKey) {
	if m.itemExpanded == nil {
		m.itemExpanded = map[itemKey]bool{}
	}
	m.itemExpanded[key] = !m.isExpanded(key)
}
