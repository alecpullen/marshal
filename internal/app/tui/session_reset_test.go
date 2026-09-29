// internal/app/tui/session_reset_test.go — per-session caches must not cross a switch
package tui

import (
	"testing"
	"time"

	"marshal/internal/app/session"
)

// Two caches in the TUI are keyed by BLOCK IDENTITY, and a block identity is
// scoped per session. Carrying entries across a session switch would serve one
// conversation's rendering — and one conversation's readable text — for another
// session's block that happens to share an id, which the numbering makes likely
// rather than rare: both conversations start at msg:...:1.
//
// Both caches documented "reset on session switch" while nothing called their
// reset. These tests are the reason that wiring exists.

// sessionSwitchModel returns a model with both caches populated and a search
// open, ready for a session switch.
func sessionSwitchModel(t *testing.T) Model {
	t.Helper()
	m := findScrollableModel(t)
	m.openFind("tokens")
	if len(m.find.matches) == 0 {
		t.Fatalf("precondition: the search matched nothing")
	}
	// Render, so the render cache is populated too.
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if m.findIndex == nil || m.findIndex.Len() == 0 {
		t.Fatal("precondition: the search index is empty")
	}
	return m
}

// resetSessionState performs the session-scoped reset the /new effect performs.
//
// It calls the effect's own body rather than reimplementing it, so a test cannot
// pass against a reset the production path does not do. The effect needs a
// sessionSwapper this build does not have, so the reset is factored out and
// called from both — which is what makes this test about the real thing.
func resetSessionState(t *testing.T, m *Model) {
	t.Helper()
	m.resetSessionScopedUIState()
}

// Switching sessions must drop the search index. A stale entry would let a
// query in the new conversation match text from the old one, because a block id
// that exists in both resolves to a projection only one of them ever had.
func TestSessionSwitchResetsTheSearchIndex(t *testing.T) {
	m := sessionSwitchModel(t)
	if got := m.findIndex.Len(); got == 0 {
		t.Fatal("precondition: nothing cached to reset")
	}

	resetSessionState(t, &m)

	if got := m.findIndex.Len(); got != 0 {
		t.Fatalf("the search index still holds %d entries after a session switch", got)
	}
}

// Switching sessions must drop the render cache, for the same reason — and the
// reset is the right thing to do even though the cache is currently never
// consulted.
//
// The precondition is conditional because of a REAL GAP this test found:
// renderConversationBlock has no callers, so the transcript never populates the
// cache. The reset is written for the cache being wired; this test proves the
// reset works the moment it is, and TestRenderCacheIsNotOnTheRenderPath records
// the gap so it cannot stay latent.
func TestSessionSwitchResetsTheRenderCache(t *testing.T) {
	m := sessionSwitchModel(t)
	if m.convRender == nil {
		// Nothing has initialized it, which is the unwired case: prime it so
		// the reset under test has something to clear.
		m.convRender = newConversationRenderCache(0)
	}
	m.convRender.put(blockRenderKey{block: "probe", width: 80}, "rendered")
	if m.convRender.Len() == 0 {
		t.Fatal("precondition: could not populate the cache")
	}

	resetSessionState(t, &m)

	if got := m.convRender.Len(); got != 0 {
		t.Fatalf("the render cache still holds %d entries after a session switch", got)
	}
}

// The render cache is built by Task 11 and never read: renderConversationBlock is
// the only entry point that consults it, and nothing calls it.
//
// This test exists to keep that visible. The consequence is real — the plan's
// requirement that "unchanged-block refresh does not reparse all history" is not
// met today, because every refresh reparses every block — and a gap recorded only
// in a commit message is a gap that gets forgotten.
//
// It asserts the CURRENT state, and it will need replacing when the cache is
// wired: the correct failure here is "the cache now has a caller, delete this
// test", which is loud rather than silent.
func TestRenderCacheIsNotOnTheRenderPath(t *testing.T) {
	m := findScrollableModel(t)
	m.convRender = newConversationRenderCache(0)
	// Render the transcript repeatedly through the real path.
	for i := 0; i < 3; i++ {
		m.lastTranscriptHash = 0
		m.refreshViewport()
	}
	if got := m.convRender.Len(); got != 0 {
		t.Fatalf(
			"the render cache now HAS a caller (%d entries after three refreshes) — "+
				"delete TestRenderCacheIsNotOnTheRenderPath and assert caching instead", got)
	}
}

// An open search must be closed. Its matches name blocks that are gone, and its
// entry anchor names a position in a transcript that no longer exists — so
// leaving it open would paint highlights for one conversation on another, and
// then "restore" the reader to a line that is not there.
func TestSessionSwitchClosesAnOpenSearch(t *testing.T) {
	m := sessionSwitchModel(t)
	if !m.find.open {
		t.Fatal("precondition: find is not open")
	}

	resetSessionState(t, &m)

	if m.find.open {
		t.Fatal("find is still open after a session switch")
	}
	if len(m.find.matches) != 0 {
		t.Fatalf("%d matches survived a session switch", len(m.find.matches))
	}
	if m.find.query != "" {
		t.Fatalf("the query %q survived a session switch", m.find.query)
	}
	if m.findStatus() != "" {
		t.Fatalf("the status line still reports a search after a session switch: %q", m.findStatus())
	}
}

// With no search open and no cache built, the reset must be a no-op rather than
// a panic: it runs on every /new, including the first one.
func TestSessionSwitchResetIsSafeWhenNothingIsCached(t *testing.T) {
	m := newTestModel(t)
	if m.convRender != nil && m.convRender.Len() != 0 {
		t.Fatal("precondition: the render cache is not empty")
	}
	if m.findIndex != nil && m.findIndex.Len() != 0 {
		t.Fatal("precondition: the search index is not empty")
	}

	resetSessionState(t, &m)

	if m.find.open {
		t.Fatal("find was opened by the reset")
	}
}

// A new session starts with a clean reading state: no anchor into the previous
// transcript, no selection. A reader who had scrolled deep into one conversation
// must not have that position applied to the next.
func TestSessionSwitchLeavesNoStaleReadingState(t *testing.T) {
	m := sessionSwitchModel(t)
	// A selection in the old conversation, made on a block that HAS a cell
	// mapping — a keyboard selection begins at the block under the top of the
	// viewport, and the first mapped block is the one a selection can exist in.
	span, ok := m.mappedBlockSpanForTest()
	if !ok {
		t.Fatal("precondition: no mapped block to select in")
	}
	m.viewportFollow = false
	m.viewport.SetYOffset(span.blockRow)
	m.captureReadingAnchor()
	m.beginSelectionAtReadingAnchor()
	for i := 0; i < 3; i++ {
		m.extendSelectionByKey(selectionKeyRight)
	}
	if !m.hasSelection() {
		t.Fatal("precondition: no selection was made")
	}
	selected := m.selection.sel.Block

	resetSessionState(t, &m)

	if m.hasSelection() {
		t.Fatalf("a selection on block %q survived a session switch", selected)
	}
	if m.selectionActive() {
		t.Fatal("a selection gesture survived a session switch")
	}
	if m.selection.sel.Block != "" {
		t.Fatalf("the selection still names block %q", m.selection.sel.Block)
	}
	if m.selection.frozen != nil {
		t.Fatalf("%d frozen blocks survived a session switch", len(m.selection.frozen))
	}
}

// The reading anchor must not survive either: it names a block from the old
// transcript, and the next reflow would resolve it against unrelated content.
//
// The fixture deliberately has NO search open. With one open, closeFind already
// restores the anchor and flips the follow flag, so the anchor reset could be
// deleted and this test would still pass — which it did, before this comment was
// written. The case that isolates the anchor is a reader who scrolled and then
// started a new conversation.
func TestSessionSwitchClearsTheReadingAnchor(t *testing.T) {
	m := findScrollableModel(t)
	if m.find.open {
		t.Fatal("precondition: a search is open, which would mask the anchor reset")
	}
	if len(m.blockSpans) == 0 {
		t.Fatal("precondition: nothing rendered to anchor on")
	}
	m.viewportFollow = false
	m.viewport.SetYOffset(m.blockSpans[1].startLine)
	m.captureReadingAnchor()
	if m.readingAnchor.Block == "" {
		t.Fatal("precondition: no anchor was captured")
	}

	resetSessionState(t, &m)

	if m.readingAnchor.Block != "" {
		t.Fatalf("the reading anchor still names block %q after a session switch", m.readingAnchor.Block)
	}
	// A new conversation follows the live end, which is where a reader who has
	// just started one expects to be.
	if !m.viewportFollow {
		t.Fatal("a new session is not following the live end")
	}
}

var _ = time.Now
var _ = session.RoleUser
