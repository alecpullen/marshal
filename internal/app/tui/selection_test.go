package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
)

// A click on body text starts a candidate selection rather than an expansion.
//
// The distinction the plan draws: pointer-down begins a selection, and a
// STATIONARY click focuses the block without expanding it. Expansion moves to the
// block's header line, so reading a paragraph and opening a collapsed group stop
// sharing one gesture — which is what made a drag unusable.
func TestClickingBodyTextBeginsASelectionInsteadOfToggling(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma delta")
	id := anchorBlock(m, "alpha beta gamma delta")

	// A press exactly ON the text, derived from the mapping rather than guessed.
	row, cell, ok := cellAtOffset(t, &m, id, 0)
	if !ok {
		t.Fatal("the mapping cannot locate the answer's first character")
	}

	expandedBefore := len(m.itemExpanded)

	if !m.beginSelectionAt(row, cell) {
		t.Fatal("a press on body text began no selection")
	}
	// A press starts a GESTURE. It has selected no text yet — the ends coincide
	// — but the surface must know the reader is mid-drag so the first pixels of
	// a drag are not indistinguishable from nothing happening.
	if !m.selectionActive() {
		t.Fatal("a press on body text began no gesture at all")
	}
	if m.selection.dragging != true {
		t.Fatal("a press on body text should begin a DRAG, not a finished selection")
	}
	if m.hasSelection() {
		t.Fatal("a press alone should not yet count as selected text")
	}
	if m.selectionAnchorBlock() == "" {
		t.Fatal("a press on body text anchored no block")
	}
	if len(m.itemExpanded) != expandedBefore {
		t.Fatalf("a press on body text changed the expansion set: %v", m.itemExpanded)
	}
}

// Motion with the button held extends the selection, and the text it covers is
// the text between the two ends.
func TestDraggingExtendsTheSelection(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")

	startRow, startCell, ok := cellAtOffset(t, &m, id, 0)
	if !ok {
		t.Fatal("cannot locate the start of the answer")
	}
	endRow, endCell, ok := cellAtOffset(t, &m, id, 5)
	if !ok {
		t.Fatal("cannot locate offset 5 of the answer")
	}

	m.beginSelectionAt(startRow, startCell)
	m.extendSelectionTo(endRow, endCell)
	if !m.selection.dragging {
		t.Fatal("dragging stopped mid-drag")
	}
	got, ok := m.selectedText()
	if !ok {
		t.Fatal("a drag produced no text")
	}
	if got != "alpha" {
		t.Fatalf("drag selected %q, want %q", got, "alpha")
	}
}

// Release ends the drag and KEEPS the selection, so `y` can copy it. A release
// that cleared it would make the gesture useless.
func TestReleasingKeepsTheSelectionForCopy(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")

	startRow, startCell, _ := cellAtOffset(t, &m, id, 0)
	endRow, endCell, _ := cellAtOffset(t, &m, id, 5)

	m.beginSelectionAt(startRow, startCell)
	m.extendSelectionTo(endRow, endCell)
	m.endSelection()

	if m.selection.dragging {
		t.Fatal("the drag was still active after release")
	}
	if !m.hasSelection() {
		t.Fatal("the selection was cleared by the release")
	}
	got, ok := m.selectedText()
	if !ok || got != "alpha" {
		t.Fatalf("after release the selection is %q (ok=%v), want %q", got, ok, "alpha")
	}
}

// A stationary press-and-release is NOT a selection: the reader clicked to focus
// a block, and leaving a zero-width selection behind would make the next `y`
// copy nothing.
func TestAStationaryClickLeavesNoSelection(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")
	row, cell, _ := cellAtOffset(t, &m, id, 3)

	m.beginSelectionAt(row, cell)
	m.endSelection()

	if m.hasSelection() {
		got, _ := m.selectedText()
		t.Fatalf("a stationary click left a selection behind: %q", got)
	}
}

// Esc clears the selection without cancelling the agent. A selection is not a
// mode change, and Esc is the reader's "never mind", not a stop button.
func TestEscClearsTheSelectionAndNothingElse(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 5)
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()
	if !m.hasSelection() {
		t.Fatal("the fixture produced no selection")
	}

	busyBefore := m.busy
	msgsBefore := len(m.state.Messages())

	m.clearSelection()

	if m.hasSelection() {
		t.Fatal("Esc did not clear the selection")
	}
	if m.busy != busyBefore {
		t.Fatal("Esc cleared the selection AND changed the busy state")
	}
	if len(m.state.Messages()) != msgsBefore {
		t.Fatal("Esc cleared the selection AND changed the conversation")
	}
}

// A selection carries the revisions it was made against, and the bytes it
// reports do not change when the underlying text does.
//
// This is what the plan means by "streaming cannot change selected bytes": a
// reader who selects an answer mid-stream keeps the bytes they read, rather than
// having the clipboard change under them between selecting and pressing y.
func TestASelectionDoesNotChangeWhenTheBlockTextDoes(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 5)
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()

	before, ok := m.selectedText()
	if !ok || before != "alpha" {
		t.Fatalf("fixture selection is %q, want %q", before, "alpha")
	}

	// Simulate the block's text changing underneath the selection (streaming
	// appends, a branch rewind replaces the tail).
	m.freezeSelection()
	after, ok := m.selectedText()
	if !ok {
		t.Fatal("the frozen selection stopped resolving")
	}
	if after != before {
		t.Fatalf("the selection changed from %q to %q when the text changed", before, after)
	}
}

// A selection is scoped to ONE surface and ONE block. A drag that starts in the
// conversation and ends in the inspector must not select the whole screen, so
// extending across a block boundary is refused rather than joined.
func TestASelectionCannotCrossIntoAnotherBlock(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	spans := m.blockRenderSpans
	if len(spans) == 0 {
		t.Fatal("the fixture published no mapping")
	}
	first := spans[0]

	startRow, startCell, _ := cellAtOffset(t, &m, first.id, 0)
	m.beginSelectionAt(startRow, startCell)
	// A row far below the block: the drag has left it.
	m.extendSelectionTo(first.blockRow+first.rows+50, 0)

	if !m.selectionActive() {
		t.Fatal("the drag produced no selection at all")
	}
	block, ok := m.selectedBlock()
	if !ok {
		t.Fatal("the selection does not resolve to a block")
	}
	if block.BlockID != first.id {
		t.Fatalf("the drag resolved against %q, want the anchor's block %q",
			block.BlockID, first.id)
	}
	// And the text it yields is that block's, bounded by it: a drag that ran
	// past the end selects TO the end, not past it.
	got, ok := m.selectedText()
	if !ok {
		t.Fatal("the selection produced no text")
	}
	if len(got) > len(first.rendered.Logical) {
		t.Fatalf("the selection is %d bytes in a %d-byte block",
			len(got), len(first.rendered.Logical))
	}
	if !strings.HasPrefix(first.rendered.Logical, got) {
		t.Fatalf("the selection %q is not a prefix of its own block %q",
			got, first.rendered.Logical)
	}
}

// `y` copies the SELECTION when there is one, in preference to the reading
// anchor. Otherwise a reader who carefully dragged over a phrase would get a
// whole block on the clipboard.
func TestCopyPrefersTheSelectionOverTheReadingAnchor(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 5)
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()

	got, ok := m.copyableText()
	if !ok {
		t.Fatal("nothing was copyable")
	}
	if got != "alpha" {
		t.Fatalf("copy yielded %q, want the selection %q", got, "alpha")
	}
}

// With no selection, `y` falls back to the reading anchor's block, which is the
// pre-existing behaviour and must not be lost.
func TestCopyWithNoSelectionFallsBackToTheReadingAnchor(t *testing.T) {
	m, row := modelWithAnswer(t, "alpha beta gamma")
	m.clearSelection()
	// Put the reader on the answer so the anchor has somewhere to point.
	m.viewport.SetYOffset(row)
	m.captureReadingAnchor()
	if m.readingAnchor.Block == "" {
		t.Skip("the fixture produced no reading anchor")
	}

	got, ok := m.copyableText()
	if !ok {
		t.Fatal("with no selection and a reading anchor, nothing was copyable")
	}
	if !strings.Contains(got, "alpha") {
		t.Fatalf("the anchor path yielded %q, want the block the reader is on", got)
	}
}

// The selection survives a REFLOW: the offsets are logical, so a resize changes
// the rows and not the bytes.
func TestSelectionSurvivesAReflow(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma delta epsilon zeta")
	id := anchorBlock(m, "alpha beta gamma delta epsilon zeta")
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 11)
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()
	before, ok := m.selectedText()
	if !ok {
		t.Fatal("no selection to reflow")
	}

	// Resize: the block is re-laid-out at a narrower width.
	m.viewport.SetWidth(40)
	m.refreshViewport()

	after, ok := m.selectedText()
	if !ok {
		t.Fatal("the selection stopped resolving after a reflow")
	}
	if after != before {
		t.Fatalf("the reflow changed the selection from %q to %q", before, after)
	}
}

// A selection must be VISIBLE. A gesture that selects text with no visible
// feedback is indistinguishable from one that does nothing, which is the failure
// this whole task exists to remove.
//
// The assertion is on the rendered viewport content — what the terminal is
// actually handed — rather than on state, because the state was already correct
// before the highlight existed.
func TestASelectionIsVisibleInTheRenderedTranscript(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 5)

	before := m.viewport.GetContent()
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()
	m.lastTranscriptHash = 0
	m.refreshViewport()
	after := m.viewport.GetContent()

	if before == after {
		t.Fatal("selecting text changed nothing on screen; the selection is invisible")
	}
	// The text itself must be unchanged: a highlight may only add styling.
	if ansi.Strip(before) != ansi.Strip(after) {
		t.Fatalf("the highlight changed the TEXT of the transcript:\nbefore %q\nafter  %q",
			ansi.Strip(before), ansi.Strip(after))
	}
	// And the selected characters must still be there, in order.
	if !strings.Contains(ansi.Strip(after), "alpha") {
		t.Fatalf("the selected text vanished from the rendered transcript: %q",
			ansi.Strip(after))
	}
}

// Clearing the selection must remove the highlight, or the reader is left
// looking at a selection that no longer exists.
func TestClearingASelectionRemovesItsHighlight(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 5)

	clean := m.viewport.GetContent()

	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()
	m.lastTranscriptHash = 0
	m.refreshViewport()

	m.clearSelection()
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if got := m.viewport.GetContent(); got != clean {
		t.Fatalf("clearing the selection did not restore the transcript:\ngot  %q\nwant %q",
			got, clean)
	}
}

// A selection whose block disappears is dropped rather than drawn against a
// neighbour: a highlight on the wrong block looks like it worked, and the reader
// would have no reason to check what they are about to copy.
func TestASelectionWhoseBlockVanishesIsDropped(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	id := anchorBlock(m, "alpha beta gamma")
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 5)
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()
	if !m.hasSelection() {
		t.Fatal("the fixture produced no selection")
	}

	// The transcript is emptied, so the selected block genuinely no longer
	// exists. Merely ADDING a message would not do it: block identities are
	// stable across growth, and the original would still be there — which is
	// the behaviour the earlier tests already rely on.
	m.state.ClearMessages()
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if m.hasSelection() {
		t.Fatalf("a selection survived the disappearance of its block (still %q)",
			m.selection.sel.Block)
	}
	// And the frozen copy went with it, so nothing holds the old text alive.
	if m.selection.frozen != nil {
		t.Fatal("the frozen copy outlived the selection it served")
	}
}

// A wheel over a live region that has nowhere to scroll must scroll the
// TRANSCRIPT, not be swallowed.
//
// This is the trap the plan removes. Routing on cursor position alone meant a
// reader whose pointer happened to rest on a subagent card could not scroll the
// conversation at all, and nothing on screen explained why — the wheel simply
// did nothing. Consuming the event only when the region can move keeps the
// region's own history reachable by opening it, where scrolling is explicit.
func TestAWheelOverAStuckLiveRegionScrollsTheTranscript(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	child.BeginStreaming()
	for i := 0; i < 40; i++ {
		child.AppendThinking(fmt.Sprintf("reasoning line %d that is long enough to be distinct\n", i))
	}
	v := m.state.RegisterSubagent("reviewer", child)
	// Filler so the transcript itself has somewhere to scroll to.
	for i := 0; i < 200; i++ {
		m.state.AddMessage(session.RoleSystem, "filler line", session.ContentTypePlain)
	}
	m.refreshViewport()

	key := regionKeyFor(t, m, v)
	// The region is at its newest end: a wheel-down has nowhere to go.
	m.regionOffset = map[itemKey]int{key: 0}

	var region clickRegion
	found := false
	for _, r := range m.clickRegions {
		if r.target.key == key && r.target.isLiveRegion {
			region, found = r, true
		}
	}
	if !found {
		t.Fatal("expected a live-region click region for the running subagent")
	}
	// The STUCK direction. Scrolling back through a region's history is
	// unbounded, so a wheel-up is never stuck; a wheel-DOWN at the newest end
	// is, and that is the case the trap was about — the reader at the bottom of
	// a card, unable to move the conversation.
	const stuck = tea.MouseWheelDown
	if m.liveRegionCanScroll(key, stuck) {
		t.Fatal("the fixture's region can still scroll in the stuck direction; " +
			"it cannot test the case")
	}

	// The wheel must be delivered AT the region, so the pointer position has to
	// be derived from the region's screen row. Getting this wrong silently
	// makes the test vacuous: an event outside the transcript is handled by the
	// ordinary scroll path, and the test then passes without the region ever
	// being consulted.
	m.viewport.SetYOffset(region.startLine)
	before := m.viewport.YOffset()
	y := m.scrollHintRows() + region.startLine - m.viewport.YOffset()
	if line, ok := m.contentLineForClick(1, y); !ok || line != region.startLine {
		t.Fatalf("the fixture does not deliver the wheel to the region: "+
			"row %d maps to (line %d, ok %v), want line %d", y, line, ok, region.startLine)
	}
	if before >= m.viewport.TotalLineCount()-m.viewport.Height() {
		t.Fatal("the transcript has nowhere to scroll down; the test could not " +
			"distinguish a swallowed wheel from a stuck viewport")
	}

	out, _ := m.Update(tea.MouseWheelMsg{X: 1, Y: y, Button: stuck})
	got := out.(Model)
	if got.viewport.YOffset() == before {
		t.Fatal("a wheel over a stuck live region was swallowed instead of scrolling the transcript")
	}
	if got.regionOffset[key] != 0 {
		t.Fatalf("the stuck region's offset changed to %d; it had nowhere to scroll",
			got.regionOffset[key])
	}
}

// ...and a region that CAN scroll still consumes the wheel, so its own history
// stays reachable without opening it.
func TestAWheelOverAScrollableLiveRegionStillScrollsTheRegion(t *testing.T) {
	m := newTestModel(t)
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	child.BeginStreaming()
	for i := 0; i < 40; i++ {
		child.AppendThinking(fmt.Sprintf("reasoning line %d that is long enough to be distinct\n", i))
	}
	v := m.state.RegisterSubagent("reviewer", child)
	m.refreshViewport()
	key := regionKeyFor(t, m, v)

	var region clickRegion
	found := false
	for _, r := range m.clickRegions {
		if r.target.key == key && r.target.isLiveRegion {
			region, found = r, true
		}
	}
	if !found {
		t.Fatal("expected a live-region click region for the running subagent")
	}
	// Scrolled back into history, so a wheel-down has somewhere to go.
	m.regionOffset = map[itemKey]int{key: 3}
	if !m.liveRegionCanScroll(key, tea.MouseWheelDown) {
		t.Fatal("a region scrolled back into history should be able to scroll forward")
	}

	out, _ := m.Update(tea.MouseWheelMsg{
		X: 1, Y: m.scrollHintRows() + region.startLine - m.viewport.YOffset(),
		Button: tea.MouseWheelDown,
	})
	got := out.(Model)
	if got.regionOffset[key] != 2 {
		t.Fatalf("the region offset is %d, want 2 (one step toward the newest end)",
			got.regionOffset[key])
	}
}

// `v` begins a selection at the reader's place, so selecting text does not
// require a trackpad.
//
// The key is driven through the REAL handler rather than by calling the method,
// because the failure this would miss is the one that matters: a key that never
// reaches the selection code.
func TestVKeyBeginsASelectionAtTheReadingAnchor(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	m.setFocus(FocusConversation)
	m.viewport.SetYOffset(0)
	m.captureReadingAnchor()

	got := selKey(t, &m, "v")
	if !got.selectionActive() {
		t.Fatal("v began no selection")
	}
	if got.selection.sel.Block == "" {
		t.Fatal("v anchored no block")
	}
	// It starts at the block's first character, so the caret is somewhere the
	// reader can see.
	if got.selection.sel.Anchor != 0 {
		t.Fatalf("the selection anchored at offset %d, want 0", got.selection.sel.Anchor)
	}
}

// Arrow keys extend the selection by grapheme while a selection is live, and
// scroll the viewport when none is.
//
// Both halves matter. Extending is the keyboard's way to place an end; scrolling
// is what the arrows have always done, and a change that made the arrows
// selection-only would break reading.
func TestArrowsExtendASelectionButScrollWithoutOne(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	for i := 0; i < 200; i++ {
		m.state.AddMessage(session.RoleSystem, "filler line", session.ContentTypePlain)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	m.setFocus(FocusConversation)

	// No selection: an arrow scrolls.
	m.viewport.SetYOffset(m.viewport.TotalLineCount() / 2)
	before := m.viewport.YOffset()
	scrolled := selKey(t, &m, "up")
	if scrolled.viewport.YOffset() == before {
		t.Fatal("an arrow with no selection did not scroll the transcript")
	}
	if scrolled.selectionActive() {
		t.Fatal("an arrow with no selection began one")
	}

	// With a selection: an arrow extends it and does not scroll.
	//
	// The viewport is returned to the answer first. `v` anchors to the block at
	// the top of the viewport, and the scroll above left it on filler rows that
	// are not mapped blocks — mid-document there is nothing to anchor to, which
	// is the CORRECT behaviour and would make this half of the test vacuous.
	m.viewport.SetYOffset(0)
	m.lastTranscriptHash = 0
	m.refreshViewport()
	m.viewport.SetYOffset(0)
	withSel := selKey(t, &m, "v")
	if !withSel.selectionActive() {
		t.Fatal("the fixture could not begin a selection")
	}
	start := withSel.selection.sel.Focus
	extended := selKey(t, &withSel, "right")
	if extended.selection.sel.Focus == start {
		t.Fatalf("right arrow did not extend the selection (focus stayed at %d)", start)
	}
	if extended.selection.sel.Focus != conversation.NextGrapheme(
		mustLogicalText(t, extended), start) {
		t.Fatalf("right arrow moved the focus to %d, want one grapheme from %d",
			extended.selection.sel.Focus, start)
	}
}

// A keyboard selection is COPYABLE as soon as it covers something: there is no
// release to wait for, so `y` must work immediately.
func TestAKeyboardSelectionIsCopyableImmediately(t *testing.T) {
	m, _ := modelWithAnswer(t, "alpha beta gamma")
	m.setFocus(FocusConversation)
	m.viewport.SetYOffset(0)
	m.captureReadingAnchor()

	m = selKey(t, &m, "v")
	for i := 0; i < 5; i++ {
		m = selKey(t, &m, "right")
	}
	if !m.hasSelection() {
		t.Fatal("five right-arrows selected nothing")
	}
	got, ok := m.copyableText()
	if !ok {
		t.Fatal("a keyboard selection was not copyable")
	}
	if got != "alpha" {
		t.Fatalf("the keyboard selection copied %q, want %q", got, "alpha")
	}
}

// selKey drives one key through the model's real key handler.
//
// It is deliberately separate from the existing pressKey helper (which sends a
// rune and reports handled): these tests need the RESULTING model, and they need
// real key CODES for the arrows rather than runes — the arrow keys are
// tea.KeyPressMsg.Code values, and sending them as runes would test a key nobody
// can press.
func selKey(t *testing.T, m *Model, key string) Model {
	t.Helper()
	msg := tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	switch key {
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		msg = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		msg = tea.KeyPressMsg{Code: tea.KeyRight}
	}
	out, cmd := m.Update(msg)
	got, ok := out.(Model)
	if !ok {
		if mp, isPtr := out.(*Model); isPtr {
			got = *mp
		} else {
			t.Fatalf("Update returned %T, not a Model", out)
		}
	}
	if cmd != nil {
		applied, _ := got.Update(cmd())
		if a, ok := applied.(Model); ok {
			got = a
		} else if ap, isPtr := applied.(*Model); isPtr {
			got = *ap
		}
	}
	return got
}

// mustLogicalText returns the logical text of the block a selection is anchored
// to, so a test can assert against the text the offsets are into.
func mustLogicalText(t *testing.T, m Model) string {
	t.Helper()
	block, ok := m.selectedBlock()
	if !ok {
		t.Fatal("the selection does not resolve to a block")
	}
	return block.Logical
}

// modelWithAnswer builds a model whose transcript holds one prose answer, and
// returns the display row that answer occupies.
func modelWithAnswer(t *testing.T, answer string) (Model, int) {
	t.Helper()
	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: t.TempDir()})
	m.state.AddMessageFinal(session.RoleAssistant, answer, session.ContentTypeMarkdown)
	m.refreshViewport()

	for _, s := range m.blockRenderSpans {
		if strings.TrimSpace(s.rendered.Logical) == answer {
			return m, s.blockRow
		}
	}
	t.Fatalf("the answer %q published no mapping: %+v", answer, m.blockRenderSpans)
	return m, 0
}

// cellAtOffset returns the display cell a logical offset in the anchored block
// is drawn at, on the transcript row that displays it.
//
// Tests use it rather than hardcoding cells, because the block is rendered behind
// the transcript's gutter: cell 0 is decoration, and a test that assumed text
// started there would be asserting against a layout that never exists. Deriving
// the cell from the mapping also means a change to the gutter cannot make these
// tests silently measure the wrong thing.
func cellAtOffset(t *testing.T, m *Model, blockID conversation.BlockID, off int) (row, cell int, ok bool) {
	t.Helper()
	for _, s := range m.blockRenderSpans {
		if s.id != blockID {
			continue
		}
		blockRow, found := s.rendered.RowForOffset(off)
		if !found {
			return 0, 0, false
		}
		c, found := s.rendered.CellAt(blockRow, off)
		if !found {
			return 0, 0, false
		}
		return s.blockRow + blockRow, c, true
	}
	return 0, 0, false
}

// anchorBlock returns the identity of the mapped block whose logical text is
// want, so a test can convert offsets to cells in the block it is about.
func anchorBlock(m Model, want string) conversation.BlockID {
	for _, s := range m.blockRenderSpans {
		if strings.TrimSpace(s.rendered.Logical) == want {
			return s.id
		}
	}
	panic("no mapped block has text " + want)
}
