package tui

import (
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

// readerAtThinkingBlock builds a model whose transcript is long enough to
// scroll, logs one thinking block containing needle, and returns the model
// with the viewport parked so that block is on screen but the viewport is NOT
// at the bottom — the state a reader is in when their place matters.
func readerAtThinkingBlock(t *testing.T, needle string) (Model, itemKey) {
	t.Helper()
	m := newTestModel(t)
	m.resize(80, 24)
	for i := 0; i < 40; i++ {
		m.state.AddMessage(session.RoleUser, "filler line", session.ContentTypePlain)
	}
	at := time.Unix(700, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: needle, Duration: time.Second, StartedAt: at})
	for i := 0; i < 40; i++ {
		m.state.AddMessage(session.RoleUser, "trailing line", session.ContentTypePlain)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	key := testItemKey(t, m, session.KindThinking, 0)
	region, ok := regionForKey(m, key)
	if !ok {
		t.Fatal("precondition: no click region for the thinking block")
	}
	// Scroll so the block is on screen but the viewport is NOT at the bottom:
	// that is the state a reader is in when they care about their place.
	m.viewportFollow = false
	m.viewport.SetYOffset(max(0, region.startLine-2))
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return m, key
}

// regionForKey returns the click region for a block key.
func regionForKey(m Model, key itemKey) (clickRegion, bool) {
	for _, r := range m.clickRegions {
		if r.target.key == key {
			return r, true
		}
	}
	return clickRegion{}, false
}

// A block is on screen (its rendered rows overlap the viewport window).
func blockVisible(m Model, key itemKey) bool {
	region, ok := regionForKey(m, key)
	if !ok {
		return false
	}
	top := m.viewport.YOffset()
	bottom := top + m.viewport.Height()
	return region.endLine > top && region.startLine < bottom
}

// THE reflow requirement for the real app: narrowing/widening the terminal
// re-lays-out every block, which moves every row after the change. A reader
// who is not following must still be looking at the same block afterwards.
func TestScrolledReaderKeepsItsBlockAcrossWidthChange(t *testing.T) {
	m, key := readerAtThinkingBlock(t, "the block I was reading")

	if !blockVisible(m, key) {
		t.Fatal("precondition: the block should be visible before the resize")
	}

	// A width change reflows every line; rows before the block change count.
	m.resize(120, 30)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if m.viewportFollow {
		t.Fatal("a width change must not silently resume following")
	}
	if !blockVisible(m, key) {
		t.Fatalf("the reader lost their block across a width change: %q no longer on screen",
			"the block I was reading")
	}
}

// New output arriving below a scrolled reader must not drag them to the
// bottom, and must not move the block they are reading off screen.
func TestScrolledReaderKeepsItsBlockWhenOutputArrives(t *testing.T) {
	m, key := readerAtThinkingBlock(t, "the block I was reading")
	beforeTop := m.viewport.YOffset()

	for i := 0; i < 20; i++ {
		m.state.AddMessage(session.RoleUser, "new output arrived", session.ContentTypePlain)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if m.viewportFollow {
		t.Fatal("new output must not resume following for a scrolled reader")
	}
	if !blockVisible(m, key) {
		t.Fatal("new output pushed the reader's block off screen")
	}
	// The block's own position is unchanged, so the viewport must not have
	// jumped to the bottom.
	if got := m.viewport.YOffset(); got == beforeTop && !blockVisible(m, key) {
		t.Fatalf("viewport offset %d unchanged but the block is not visible", got)
	}
	if m.viewport.AtBottom() {
		t.Fatal("a scrolled reader was dragged to the bottom by new output")
	}
}

// Drilling into a child and coming back must restore the parent's reading
// position, not dump the reader at the top or the bottom of the parent.
func TestDrillAndReturnRestoresParentReadingPosition(t *testing.T) {
	m, key := readerAtThinkingBlock(t, "parent block")
	if !blockVisible(m, key) {
		t.Fatal("precondition: the parent block should be visible")
	}

	child := newChildState(t)
	for i := 0; i < 30; i++ {
		child.AddMessage(session.RoleUser, "child line", session.ContentTypePlain)
	}
	view := m.state.RegisterSubagent("explore", child)

	m.drillIntoSubagent(view)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	// While drilled in, the parent's block is gone from the document.
	doc := m.conversationDocument()
	if _, ok := doc.BlockForMember(key.viewID); ok {
		t.Fatal("the parent's block should not be in the drilled-in document")
	}

	if !m.popDrill() {
		t.Fatal("popDrill reported nothing to pop")
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if !blockVisible(m, key) {
		t.Fatal("returning from a drill lost the parent's reading position")
	}
	if m.viewportFollow {
		t.Fatal("returning from a drill must restore the parent's follow state (it was off)")
	}
}

// Returning from a drill into a child restores the PARENT's follow state,
// including the case where the parent was following: the reader should be
// back at the bottom, not stranded mid-scroll.
func TestDrillAndReturnRestoresFollowingParent(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	for i := 0; i < 40; i++ {
		m.state.AddMessage(session.RoleUser, "filler", session.ContentTypePlain)
	}
	m.viewportFollow = true
	m.lastTranscriptHash = 0
	m.refreshViewport()

	child := newChildState(t)
	child.AddMessage(session.RoleUser, "child", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore", child)

	m.drillIntoSubagent(view)
	m.popDrill()
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if !m.viewportFollow {
		t.Fatal("a following parent must still be following after a drill round trip")
	}
	if !m.viewport.AtBottom() {
		t.Fatal("a following parent must be back at the bottom after a drill round trip")
	}
}

// An anchor whose block genuinely vanishes must land the reader somewhere real
// and must not claim to have found the block. The real vanishing case here is
// ClearRunEvents: the TUI drops the collapsed run log when a new user turn
// starts, so a reader anchored to a run event loses it mid-session.
func TestVanishedAnchorIsReportedNotInvented(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	for i := 0; i < 30; i++ {
		m.state.AddMessage(session.RoleUser, "filler", session.ContentTypePlain)
	}
	at := time.Unix(800, 0)
	m.state.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, TaskN: 1, Title: "abc123", At: at})
	for i := 0; i < 30; i++ {
		m.state.AddMessage(session.RoleUser, "trailing", session.ContentTypePlain)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	key := testItemKey(t, m, session.KindRunEvent, 0)
	// A run event is not clickable, so it has no click region — the anchor
	// must still be able to name it, which is why the rendered spans are
	// recorded separately.
	if _, ok := regionForKey(m, key); ok {
		t.Fatal("precondition: a run event should not have a click region")
	}
	start, ok := m.blockStartRow(conversation.BlockID(key.viewID))
	if !ok {
		t.Fatal("precondition: the run event should have a rendered block span")
	}
	m.viewportFollow = false
	m.viewport.SetYOffset(max(0, start-1))
	m.lastTranscriptHash = 0
	m.refreshViewport()

	anchored := m.readingAnchor
	if anchored.Block == "" {
		t.Fatal("precondition: a scrolled reader should have an anchor")
	}
	if string(anchored.Block) != key.viewID {
		t.Fatalf("anchor names %q, want the visible block %q", anchored.Block, key.viewID)
	}
	if _, present := m.conversationDocument().Block(anchored.Block); !present {
		t.Fatal("precondition: the anchored block should be in the document")
	}

	// The run log is dropped. The anchored block is gone; the reader must
	// still be somewhere real, and the move must be reported.
	m.state.ClearRunEvents()
	m.lastTranscriptHash = 0
	m.refreshViewport()

	doc := m.conversationDocument()
	if _, still := doc.Block(anchored.Block); still {
		t.Fatal("precondition: the anchored block should be gone")
	}
	res := anchored.Resolve(doc)
	if res.Found {
		t.Fatal("a vanished block must not resolve as found")
	}
	if !res.Approximate && !res.Empty {
		t.Fatalf("a vanished block must be reported as approximate or empty, got %+v", res)
	}
	if res.Anchor.Block != "" {
		if _, ok := doc.Block(res.Anchor.Block); !ok {
			t.Fatalf("the fallback names a block that is not in the document: %q", res.Anchor.Block)
		}
	}
}

// The reader's own anchor must be re-resolved in place: after the block
// vanishes, the model's anchor names a real block rather than the dead one, or
// every later rebuild would keep resolving a ghost.
func TestModelAnchorIsReresolvedAfterVanish(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	for i := 0; i < 30; i++ {
		m.state.AddMessage(session.RoleUser, "filler", session.ContentTypePlain)
	}
	at := time.Unix(810, 0)
	m.state.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, TaskN: 1, Title: "abc123", At: at})
	for i := 0; i < 30; i++ {
		m.state.AddMessage(session.RoleUser, "trailing", session.ContentTypePlain)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	key := testItemKey(t, m, session.KindRunEvent, 0)
	start, ok := m.blockStartRow(conversation.BlockID(key.viewID))
	if !ok {
		t.Fatal("precondition: the run event should have a rendered block span")
	}
	m.viewportFollow = false
	m.viewport.SetYOffset(max(0, start-1))
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if string(m.readingAnchor.Block) != key.viewID {
		t.Fatalf("precondition: anchor = %q, want %q", m.readingAnchor.Block, key.viewID)
	}

	m.state.ClearRunEvents()
	m.lastTranscriptHash = 0
	m.refreshViewport()

	doc := m.conversationDocument()
	if m.readingAnchor.Block != "" {
		if _, ok := doc.Block(m.readingAnchor.Block); !ok {
			t.Fatalf("model anchor still names the vanished block %q", m.readingAnchor.Block)
		}
	}
}

// A reader anchored on a COLLAPSED GROUP must stay there across a reflow.
//
// This was a real defect, found while wiring find onto the same table. A group
// is recorded in the rendered-span table under its FIRST MEMBER's identity,
// while the document names it "group:<first member>" — so the lookup missed,
// blockIndex reported 0, and Resolve's index-clamped fallback sent the reader to
// the TOP OF THE TRANSCRIPT on the next reflow. The group's members are also the
// ids of the group's CHILDREN, which is what made the collision silent: the
// member exists in the document, just not as a top-level block.
func TestAnchorOnACollapsedGroupSurvivesAReflow(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(900, 0)
	// Two same-tool reads collapse into one group. They must be CONSECUTIVE in
	// the transcript, which is ordered by timestamp.
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at, ResultContent: "one"})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(time.Second), ResultContent: "two"})
	m.refreshViewport()

	doc := m.conversationDocument()
	var group conversation.Block
	for _, b := range doc.Blocks() {
		if b.Kind == conversation.BlockToolGroup {
			group = b
		}
	}
	if group.ID == "" {
		t.Fatal("precondition: the reads did not collapse into a group")
	}

	// The group must be reachable by its DOCUMENT identity, which is what every
	// consumer of blockSpans is asking about.
	row, ok := m.blockStartRow(group.ID)
	if !ok {
		t.Fatalf("the group %q is not in the rendered-span table, so nothing can scroll to it", group.ID)
	}

	// And an anchor on the group must RESOLVE to the group rather than falling
	// through to the nearest-position guess. This is the consequence that
	// matters: the fallback is what moved the reader to the top of the
	// transcript, and it is reached only when the block cannot be found by
	// identity.
	anchor := conversation.Anchor{Block: group.ID, Offset: 0, Index: m.blockIndex(group.ID)}
	res := anchor.Resolve(doc)
	if !res.Found {
		t.Fatalf("an anchor on the group did not resolve to it; it landed on %q (approximate=%v)",
			res.Anchor.Block, res.Approximate)
	}
	if res.Anchor.Block != group.ID {
		t.Fatalf("the anchor resolved to %q, want the group %q", res.Anchor.Block, group.ID)
	}

	// The index the model reports for the group must be the group's document
	// position, because that index is what the fallback uses when the group
	// genuinely vanishes. Reporting 0 sends the reader to the first block.
	if want, _ := doc.IndexOf(group.ID); m.blockIndex(group.ID) != want {
		t.Fatalf("blockIndex(%q) = %d, want its document position %d",
			group.ID, m.blockIndex(group.ID), want)
	}
	_ = row
}

// The rendered-span table and the document must agree on EVERY block's
// identity, not just on the easy ones.
//
// This is the general form of the group defect: the transcript builder derived
// a block's identity from its CLICK KEY, and a collapsed group's key is its
// first member's identity while the document calls the group
// "group:<member>". The group's members are also its children, so the member
// identity resolves to something in the document — which is what let the
// mismatch hide. Asserting agreement over the whole transcript catches the
// class, not just the instance.
func TestEveryRenderedBlockIsAKnownDocumentBlock(t *testing.T) {
	m := newTestModel(t)
	// The transcript is ordered by TIMESTAMP, so the items here are spaced a
	// minute apart. Two calls only group when nothing sorts between them: with
	// the thinking entry at the same instant as the first read it landed in the
	// middle of the run, and no group formed at all.
	base := time.Unix(902, 0)
	m.state.AddMessage(session.RoleUser, "a prompt", session.ContentTypePlain)
	m.state.LogThinking(session.ThinkingEntry{Text: "hmm", StartedAt: base})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: base.Add(time.Minute), ResultContent: "one"})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: base.Add(2 * time.Minute), ResultContent: "two"})
	m.state.AddMessage(session.RoleAssistant, "an answer", session.ContentTypeMarkdown)
	m.refreshViewport()

	doc := m.conversationDocument()
	if len(m.blockSpans) == 0 {
		t.Fatal("precondition: nothing was rendered")
	}
	var sawGroup bool
	for _, span := range m.blockSpans {
		if _, ok := doc.Block(span.id); !ok {
			t.Fatalf("the rendered-span table names %q, which is not a document block", span.id)
		}
		if len(span.id) > 6 && span.id[:6] == "group:" {
			sawGroup = true
		}
	}
	if !sawGroup {
		t.Fatal("precondition: no collapsed group was rendered, so the case this test exists for was not exercised")
	}
}

// While following, there is no anchor to preserve and the viewport stays
// pinned to the bottom: anchoring must not fight follow mode.
func TestFollowingReaderStaysAtBottom(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	for i := 0; i < 60; i++ {
		m.state.AddMessage(session.RoleUser, "line", session.ContentTypePlain)
	}
	m.viewportFollow = true
	m.lastTranscriptHash = 0
	m.refreshViewport()

	m.state.AddMessage(session.RoleUser, "more", session.ContentTypePlain)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if !m.viewport.AtBottom() {
		t.Fatal("a following reader must stay at the bottom")
	}
}

// The anchor must survive an unchanged rebuild: rebuilding the viewport with
// nothing changed must not move the reader.
func TestAnchorStableAcrossUnchangedRebuild(t *testing.T) {
	m, _ := readerAtThinkingBlock(t, "the block I was reading")
	before := m.viewport.YOffset()

	m.lastTranscriptHash = 0
	m.refreshViewport()

	if got := m.viewport.YOffset(); got != before {
		t.Fatalf("an unchanged rebuild moved the reader: %d -> %d", before, got)
	}
}
