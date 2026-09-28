package session

import (
	"strconv"
	"sync/atomic"
)

// stateScopeSeq numbers State instances so their presentation identities
// cannot collide.
//
// The per-collection counters (msg:1, audit:1, …) are per-State, and Marshal
// genuinely has several live States at once: every subagent gets a child State
// of its own, and the TUI's drill-down swaps the child's transcript into the
// same viewport the parent's was in. Without a scope, the parent's msg:1 and
// the child's msg:1 are the same identity — so an anchor taken on the parent
// resolves to an unrelated child message, and a selection made before drilling
// in silently moves on return.
//
// A process-wide counter is the simplest thing that cannot collide, and it
// costs nothing: the identity is presentation-only and recomputed per call, so
// a restart renumbering the scopes is harmless.
var stateScopeSeq atomic.Int64

// ViewID is a presentation identity for one transcript item.
//
// It exists because the TUI used to identify a transcript block by
// (Timestamp, Kind). That is not an identity: two thinking entries logged
// inside the same clock tick collapse into one, and so do the several run
// events a fast pipeline task emits with identical text. The TUI could not
// tell them apart, so expanding one expanded the other, and a reading anchor
// could not name what it was anchored to. A slice index into the *sorted*
// transcript is no better — Transcript() rebuilds and re-sorts its slice on
// every call, so positions move between renders.
//
// A ViewID is derived per source collection, from that collection's own
// stable identity, and attached before the transcript is sorted. It is
// deliberately:
//
//   - presentation-only. Nothing persists it and no schema changes: it is
//     recomputed from the same in-memory collections on every call.
//   - never a hash of content. Identical text written twice (a retry loop
//     logging the same failure) is a real, common case, and hashing mutable
//     content would also change when an entry is edited in place.
//   - namespaced per kind, so audit entry 1, thinking entry 1 and job exit 1
//     stay distinct even though they are numbered independently.
//
// Messages use Message.ID rather than an ordinal. Message.ID is a stable,
// append-only counter that survives a branch rewind, because the message
// objects outlive their place in the active-branch slice (see
// rebuildActiveBranch) — an ordinal over s.messages would not, since a rewind
// shortens that slice. Subagents likewise use SubagentView.ID.
const (
	viewIDMessage  = "msg:"
	viewIDAudit    = "audit:"
	viewIDThinking = "thinking:"
	viewIDSubagent = "subagent:"
	viewIDRunEvent = "runevent:"
	viewIDJobExit  = "jobexit:"

	// ViewIDLiveThinking is the identity of the in-progress thinking region.
	// That region is not a transcript item, so it has no ordinal and cannot
	// collide with one: real thinking items are thinking:1, thinking:2, …
	// while this one is named.
	ViewIDLiveThinking = "thinking:live"
)

// ordinalViewID renders the identity for a 1-based insertion ordinal in one
// collection's namespace.
//
// Ordinals are derived from the entry's index in its (append-only) source
// collection plus one, which IS its insertion ordinal: those collections are
// only ever appended to, so an entry's index never changes for the life of
// the State. This is why the identity is stable across renders even though
// the transcript is rebuilt every time.
//
// The assumption is load-bearing: if a collection ever gains a removal or a
// reorder — deleting one audit entry, sorting the thinking log — every
// ordinal after the change point shifts, and a reader's anchor would land on
// a neighbour. TestViewIDOrdinalsTrackAppendOnlyCollections pins the
// invariant. Collections that can be cleared or rebuilt do NOT use this: run
// events carry a collection epoch, and messages use Message.ID.
func ordinalViewID(prefix string, index int) string {
	return prefix + strconv.Itoa(index+1)
}

// scopePrefix renders this State's scope token for an identity namespace.
//
// The "@" separator is deliberate: it cannot appear in any of the namespaces
// or ordinals, so a scoped identity can never be mistaken for an unscoped one
// or for a different scope.
func (s *State) scopePrefix(kind string) string {
	return kind + "@s" + strconv.FormatInt(s.scopeID, 10) + ":"
}

// viewIDForRunEvent builds a run-event identity from the collection epoch and
// the ordinal within that epoch.
//
// The epoch is load-bearing. ClearRunEvents empties the log mid-session (the
// TUI calls it when a new user turn starts); without an epoch the next event
// would be runevent:0.1 again — the identity the reader may still be anchored
// to, which now belongs to unrelated content.
func (s *State) viewIDForRunEvent(index int) string {
	return s.scopePrefix(viewIDRunEvent) + strconv.Itoa(s.runEventEpoch) + "." + strconv.Itoa(index+1)
}
