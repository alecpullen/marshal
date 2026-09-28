package session

import (
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

// viewIDs collects the presentation identities of every item, in transcript
// order.
func viewIDs(items []TranscriptItem) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].ViewID)
	}
	return out
}

// TestViewIDIsNonEmptyForEveryItem pins the basic contract: every transcript
// item carries a presentation identity, so a reader can name what they are
// looking at. An empty identity is the collision case — it would make every
// item indistinguishable from every other one in the same kind.
func TestViewIDIsNonEmptyForEveryItem(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "hi", ContentTypePlain)
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read"})
	s.LogThinking(ThinkingEntry{Text: "hmm"})
	s.AddRunEvent(RunEvent{Kind: RunEventCommit, TaskN: 1, Title: "abc123"})
	s.AddJobExit(JobExit{ID: "job-1", Command: "go test", ExitCode: 0})
	_ = s.RegisterSubagent("explore", newTestState())

	for i, item := range s.Transcript() {
		if item.ViewID == "" {
			t.Fatalf("item %d (kind %v) has an empty ViewID", i, item.Kind)
		}
	}
}

// Two same-kind items written at the same instant must still be
// distinguishable. The transcript used to be keyed by (Timestamp, Kind), so
// identical timestamps collapsed two different blocks into one identity and
// expanding one expanded both. A slice index is no better: Transcript()
// re-sorts on every call.
func TestViewIDDistinguishesSameKindAtIdenticalTimestamp(t *testing.T) {
	s := newTestState()
	at := time.Unix(100, 0)
	s.LogThinking(ThinkingEntry{Text: "first", StartedAt: at})
	s.LogThinking(ThinkingEntry{Text: "second", StartedAt: at})

	items := s.Transcript()
	if len(items) != 2 {
		t.Fatalf("got %d thinking items, want 2", len(items))
	}
	if items[0].Timestamp != items[1].Timestamp {
		t.Fatalf("precondition: the two items share a timestamp")
	}
	if items[0].Kind != items[1].Kind {
		t.Fatalf("precondition: the two items share a kind")
	}
	if items[0].ViewID == items[1].ViewID {
		t.Fatalf("two same-kind items at one timestamp share ViewID %q", items[0].ViewID)
	}
}

// Identical timestamps for run events are the normal case, not an edge case:
// the pipeline logs several events inside one fast task. Identical *content*
// happens too (a retry loop logging the same failure), so identity must not
// be a hash of the text either.
func TestViewIDDistinguishesIdenticalRunEvents(t *testing.T) {
	s := newTestState()
	at := time.Unix(200, 0)
	for i := 0; i < 3; i++ {
		s.AddRunEvent(RunEvent{Kind: RunEventRetry, TaskN: 1, Title: "go test ./...", Body: "FAIL", At: at})
	}

	items := s.Transcript()
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.ViewID] {
			t.Fatalf("duplicate ViewID %q across identical run events", item.ViewID)
		}
		seen[item.ViewID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("got %d distinct ViewIDs, want 3", len(seen))
	}
}

// ClearRunEvents empties the collection mid-session. The next event must not
// reuse the identity of a cleared one, or a reader scrolled to the old event
// silently lands on an unrelated new one.
func TestViewIDSurvivesRunEventCollectionReset(t *testing.T) {
	s := newTestState()
	s.AddRunEvent(RunEvent{Kind: RunEventCommit, TaskN: 1, Title: "aaa111"})
	before := viewIDs(s.Transcript())

	s.ClearRunEvents()
	if n := len(s.Transcript()); n != 0 {
		t.Fatalf("precondition: ClearRunEvents left %d items", n)
	}

	s.AddRunEvent(RunEvent{Kind: RunEventCommit, TaskN: 2, Title: "bbb222"})
	after := viewIDs(s.Transcript())
	if len(after) != 1 {
		t.Fatalf("got %d items after the reset, want 1", len(after))
	}
	if after[0] == before[0] {
		t.Fatalf("an event added after ClearRunEvents reused the cleared ViewID %q", after[0])
	}
}

// Identities must be stable across repeated Transcript() calls: the method
// rebuilds and re-sorts its slice every time, so anything derived from the
// current ordering would drift between renders.
func TestViewIDStableAcrossRepeatedTranscriptCalls(t *testing.T) {
	s := newTestState()
	at := time.Unix(300, 0)
	s.AddMessage(RoleUser, "hi", ContentTypePlain)
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at})
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at})
	s.LogThinking(ThinkingEntry{Text: "hmm", StartedAt: at})

	first := viewIDs(s.Transcript())
	for i := 0; i < 5; i++ {
		if got := viewIDs(s.Transcript()); !equalStrings(got, first) {
			t.Fatalf("call %d changed identities:\n got %v\nwant %v", i+1, got, first)
		}
	}
}

// A rewind moves the active leaf, which rebuilds s.messages as a shorter
// path. A message that survives the rewind must keep the identity a reader
// anchored to; only the removed tail may disappear. Deriving identity from
// the current position in the slice fails exactly here.
func TestViewIDSurvivesRewind(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "one", ContentTypePlain)
	s.AddMessage(RoleAssistant, "two", ContentTypePlain)
	s.AddMessage(RoleUser, "three", ContentTypePlain)

	before := s.Transcript()
	if len(before) != 3 {
		t.Fatalf("precondition: got %d messages, want 3", len(before))
	}
	keep := viewIDs(before)[:2]
	third := before[2].Message.ID

	s.Rewind(third)
	after := viewIDs(s.Transcript())
	if !equalStrings(after, keep) {
		t.Fatalf("rewind changed surviving identities:\n got %v\nwant %v", after, keep)
	}
}

// A message's identity is its message ID, not its position: the same ID must
// yield the same ViewID no matter where the message currently sits.
func TestViewIDForMessageTracksMessageID(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "one", ContentTypePlain)
	s.AddMessage(RoleUser, "two", ContentTypePlain)

	items := s.Transcript()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ViewID == items[1].ViewID {
		t.Fatalf("both messages share ViewID %q", items[0].ViewID)
	}
	// Identity is the message ID, so the same state re-reading its transcript
	// must produce the same identity even though the slice is rebuilt.
	again := s.Transcript()
	if again[0].ViewID != items[0].ViewID {
		t.Fatalf("message ViewID moved between reads: %q -> %q", items[0].ViewID, again[0].ViewID)
	}
}

// Message IDs are per-State, so a parent and its child subagent both have a
// msg:1. A presentation identity must not collide across those scopes, or an
// anchor taken on the parent resolves to an unrelated child message — the
// cross-session collision that makes a drill-down silently move the reader.
func TestViewIDScopedPerStateInstance(t *testing.T) {
	parent := newTestState()
	child := newTestState()
	parent.AddMessage(RoleUser, "parent message", ContentTypePlain)
	child.AddMessage(RoleUser, "child message", ContentTypePlain)

	parentItems := parent.Transcript()
	childItems := child.Transcript()
	if len(parentItems) != 1 || len(childItems) != 1 {
		t.Fatalf("precondition: got %d parent and %d child items, want 1 each",
			len(parentItems), len(childItems))
	}
	if parentItems[0].Message.ID != childItems[0].Message.ID {
		t.Skip("message IDs no longer collide across states; scope token may be unnecessary")
	}
	if parentItems[0].ViewID == childItems[0].ViewID {
		t.Fatalf("parent and child messages share ViewID %q", parentItems[0].ViewID)
	}
}

// The same applies to the other collections: two States each produce
// audit:1, and those are different tool calls.
func TestViewIDScopedPerStateForCollections(t *testing.T) {
	a := newTestState()
	b := newTestState()
	a.LogThinking(ThinkingEntry{Text: "a"})
	b.LogThinking(ThinkingEntry{Text: "b"})

	if a.Transcript()[0].ViewID == b.Transcript()[0].ViewID {
		t.Fatalf("two states produced the same thinking ViewID %q", a.Transcript()[0].ViewID)
	}
}

// Kind-scoped ordinals mean an audit event and a thinking entry at the same
// timestamp cannot collide even though they are numbered independently.
func TestViewIDIsKindScoped(t *testing.T) {
	s := newTestState()
	at := time.Unix(400, 0)
	// Interleave so each collection's first entry is not the transcript's
	// first item.
	s.LogThinking(ThinkingEntry{Text: "t", StartedAt: at})
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at})

	items := s.Transcript()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for _, item := range items {
		if item.Kind == KindThinking && item.ViewID == "audit:1" {
			t.Fatalf("thinking item reused the audit namespace: %q", item.ViewID)
		}
	}
	if items[0].ViewID == items[1].ViewID {
		t.Fatalf("cross-kind items share ViewID %q", items[0].ViewID)
	}
}

// Ordinals are derived from an entry's index in its source collection, which
// is only valid while those collections are append-only. This pins the
// invariant that makes the identity stable: adding to a collection must never
// renumber an entry already in it.
func TestViewIDOrdinalsTrackAppendOnlyCollections(t *testing.T) {
	s := newTestState()
	s.LogThinking(ThinkingEntry{Text: "one"})
	s.LogToolCall(registry.AuditEvent{ToolName: "file.read"})

	before := viewIDs(s.Transcript())

	// Append to both collections and re-read. Everything already present must
	// keep its identity; only new entries may introduce new ones.
	s.LogThinking(ThinkingEntry{Text: "two"})
	s.LogToolCall(registry.AuditEvent{ToolName: "file.write"})
	after := viewIDs(s.Transcript())

	if len(after) != len(before)+2 {
		t.Fatalf("got %d identities, want %d", len(after), len(before)+2)
	}
	for _, id := range before {
		found := false
		for _, got := range after {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("appending renumbered an existing entry: %q disappeared from %v", id, after)
		}
	}
}

// Clearing messages starts a new branch but must not let a later message
// reuse a cleared message's identity: nextMsgID keeps advancing.
func TestViewIDNotReusedAfterClearMessages(t *testing.T) {
	s := newTestState()
	s.AddMessage(RoleUser, "gone", ContentTypePlain)
	before := viewIDs(s.Transcript())

	if n := s.ClearMessages(); n != 1 {
		t.Fatalf("ClearMessages = %d, want 1", n)
	}
	s.AddMessage(RoleUser, "new", ContentTypePlain)
	after := viewIDs(s.Transcript())

	if len(after) != 1 {
		t.Fatalf("got %d identities after clear, want 1", len(after))
	}
	if after[0] == before[0] {
		t.Fatalf("a message added after ClearMessages reused %q", after[0])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
