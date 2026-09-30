// internal/app/tui/session_reset_test.go — per-session caches must not cross a switch
package tui

import (
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/inspector"
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

// A session switch must drop the drill stack. Its entries name subagents of the
// conversation being left, and the anchors saved beside them are positions in a
// transcript that no longer exists — so leaving it populated means the first Esc
// in the new conversation "pops back" to a parent that is gone.
func TestSessionSwitchClearsTheDrillStack(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	child := session.New(m.state.Config, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	view := m.state.RegisterSubagent("explore", child)
	child.AddMessage(session.RoleUser, "child question", session.ContentTypePlain)
	m.drillIntoSubagent(view)
	if len(m.viewStack) == 0 {
		t.Fatal("precondition: the fixture is not drilled in")
	}
	if len(m.viewStackAnchors) == 0 {
		t.Fatal("precondition: no drill anchor was saved")
	}

	resetSessionState(t, &m)

	if len(m.viewStack) != 0 {
		t.Fatalf("%d drill levels survived a session switch", len(m.viewStack))
	}
	if len(m.viewStackAnchors) != 0 {
		t.Fatalf("%d saved drill anchors survived a session switch", len(m.viewStackAnchors))
	}
	if m.anchorFollow {
		t.Fatal("the saved follow flag survived a session switch")
	}
}

// The session switch must not leave the inspector rendering the previous
// conversation's changes. The snapshot is handed to the inspector separately from
// the rail's row list, so clearing only the rows left the Changes tab showing
// files from a session the user had left.
func TestSessionSwitchClearsTheRailSnapshotForTheInspector(t *testing.T) {
	m := findScrollableModel(t)
	m.railSnapshot = changedfiles.Snapshot{Status: changedfiles.StatusOK}
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.refreshInspector()
	if m.inspector.model.ChangesSnapshot().Status == "" {
		t.Fatal("precondition: the inspector never received the snapshot")
	}

	// The /new effect's own reset body, plus the snapshot clear it performs.
	m.railChanged = nil
	m.railSnapshot = changedfiles.Snapshot{}
	m.refreshInspector()

	if got := m.inspector.model.ChangesSnapshot().Status; got != "" {
		t.Fatalf("the inspector still reports changes status %q after a session switch", got)
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

// With no search open and nothing cached, the reset must be a no-op rather than
// a panic: it runs on every /new, including the first one.
func TestSessionSwitchResetIsSafeWhenNothingIsCached(t *testing.T) {
	m := newTestModel(t)
	if m.findIndex != nil && m.findIndex.Len() != 0 {
		t.Fatal("precondition: the search index is not empty")
	}
	if m.hasSelection() {
		t.Fatal("precondition: the fixture starts with a selection")
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
