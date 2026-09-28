package tui

import (
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

// A document built from a real transcript must carry the same identities the
// transcript produced, so a click region, an expand override and an anchor
// all name the same thing.
func TestConversationDocumentUsesTranscriptViewIDs(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(500, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "one", StartedAt: at})
	m.state.LogThinking(session.ThinkingEntry{Text: "two", StartedAt: at})

	doc := m.conversationDocument()
	items := m.state.Transcript()
	if len(items) != 2 {
		t.Fatalf("precondition: got %d items, want 2", len(items))
	}

	for _, item := range items {
		block, ok := doc.BlockForMember(item.ViewID)
		if !ok {
			t.Fatalf("no block for transcript item %q", item.ViewID)
		}
		if string(block.ID) != item.ViewID {
			t.Fatalf("block ID %q does not match ViewID %q", block.ID, item.ViewID)
		}
	}
}

// The two same-timestamp thinking entries that broke (Timestamp, Kind)
// identity must be two distinct blocks in the document too. This is the
// end-to-end version of the collision: the transcript and the document must
// agree that these are different things.
func TestConversationDocumentSeparatesIdenticalTimestamps(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(501, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "first", StartedAt: at})
	m.state.LogThinking(session.ThinkingEntry{Text: "second", StartedAt: at})

	doc := m.conversationDocument()
	items := m.state.Transcript()

	if items[0].ViewID == items[1].ViewID {
		t.Fatalf("transcript gave both items the same identity %q", items[0].ViewID)
	}

	first, ok := doc.BlockForMember(items[0].ViewID)
	if !ok {
		t.Fatalf("no block for %q", items[0].ViewID)
	}
	second, ok := doc.BlockForMember(items[1].ViewID)
	if !ok {
		t.Fatalf("no block for %q", items[1].ViewID)
	}
	if first.ID == second.ID {
		t.Fatalf("both thinking entries resolved to one block %q", first.ID)
	}
}

// A collapsed run of same-tool audit events becomes one group block, and the
// group's identity is stable as the run grows — otherwise a reader's anchor
// would move each time another read was logged.
func TestConversationDocumentGroupsStableAcrossGrowth(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(502, 0)
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(time.Second)})

	before := m.conversationDocument()
	var beforeGroup conversation.Block
	found := false
	for _, b := range before.Blocks() {
		if b.Kind == conversation.BlockToolGroup {
			beforeGroup, found = b, true
		}
	}
	if !found {
		t.Fatalf("two same-tool reads did not collapse into a group: %+v", before.Blocks())
	}

	// A third read joins the run.
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(2 * time.Second)})
	after := m.conversationDocument()

	got, ok := after.Block(beforeGroup.ID)
	if !ok {
		t.Fatalf("the group lost its identity %q when it grew", beforeGroup.ID)
	}
	if len(got.Members) != 3 {
		t.Fatalf("group members = %v, want 3 after the new read", got.Members)
	}
}

// Selecting a member of a collapsed group must resolve to that member's own
// block, not to the group: this is the "copy one line, get four tool outputs"
// bug, asserted end to end through the real grouping logic.
func TestConversationDocumentGroupMemberSelectionIsNarrow(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(503, 0)
	// A run of mergeable reads.
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(time.Second)})
	// A failure never joins a run (see mergeableAuditEvent), so it is a block
	// of its own that happens to sit between them in time.
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(2 * time.Second), Error: "boom"})

	doc := m.conversationDocument()
	items := m.state.Transcript()

	var failed string
	for _, item := range items {
		if item.Audit != nil && item.Audit.Error != "" {
			failed = item.ViewID
		}
	}
	if failed == "" {
		t.Fatal("precondition: no failed audit item found")
	}

	block, ok := doc.BlockForMember(failed)
	if !ok {
		t.Fatalf("no block for the failed call %q", failed)
	}
	if block.Kind == conversation.BlockToolGroup {
		t.Fatalf("the failed call resolved to the enclosing group %q instead of its own block", block.ID)
	}
	if len(block.Members) != 1 {
		t.Fatalf("failed call block covers %v, want exactly one member", block.Members)
	}
}

// While drilled into a subagent, the document describes the CHILD transcript,
// so anchors and selections name what is on screen rather than the parent's
// hidden items.
func TestConversationDocumentFollowsDrill(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore", child)
	child.AddMessage(session.RoleUser, "child question", session.ContentTypePlain)

	m.drillIntoSubagent(view)
	doc := m.conversationDocument()

	childItems := child.Transcript()
	if len(childItems) == 0 {
		t.Fatal("precondition: child has no transcript items")
	}
	if _, ok := doc.BlockForMember(childItems[0].ViewID); !ok {
		t.Fatalf("drilled document is missing the child's item %q", childItems[0].ViewID)
	}

	// The parent's own items must not be present while drilled in.
	if _, ok := doc.BlockForMember("msg:1"); ok {
		t.Fatal("drilled document still describes the parent transcript")
	}
}

// Every block the adapter emits must carry an identity, so nothing can be
// rendered that a reader cannot anchor to or select.
func TestConversationDocumentBlocksAllIdentified(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(504, 0)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.state.LogThinking(session.ThinkingEntry{Text: "hmm", StartedAt: at})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(time.Second)})
	m.state.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, TaskN: 1, Title: "abc", At: at.Add(2 * time.Second)})
	m.state.AddJobExit(session.JobExit{ID: "job-1", Command: "go test", At: at.Add(3 * time.Second)})
	_ = m.state.RegisterSubagent("explore", newChildState(t))

	for _, b := range m.conversationDocument().Blocks() {
		if b.ID == "" {
			t.Fatalf("adapter emitted an unidentified block: %+v", b)
		}
		if len(b.Members) == 0 {
			t.Fatalf("adapter emitted a memberless block: %+v", b)
		}
	}
}

// The document must be rebuildable at any time without the identities moving:
// a caller rebuilds it on every render.
func TestConversationDocumentStableAcrossRebuilds(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(505, 0)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.state.LogThinking(session.ThinkingEntry{Text: "hmm", StartedAt: at})

	first := conversationIDs(m.conversationDocument())
	for i := 0; i < 3; i++ {
		got := conversationIDs(m.conversationDocument())
		if len(got) != len(first) {
			t.Fatalf("rebuild %d produced %d blocks, want %d", i+1, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("rebuild %d changed block %d: %q -> %q", i+1, j, first[j], got[j])
			}
		}
	}
}

func conversationIDs(doc *conversation.Document) []conversation.BlockID {
	out := make([]conversation.BlockID, 0, doc.Len())
	for _, b := range doc.Blocks() {
		out = append(out, b.ID)
	}
	return out
}
