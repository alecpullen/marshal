// internal/app/tui/find_test.go — the find UI over the live conversation
package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

// These tests hold the find UI to the promises that make it usable rather than
// merely present. The four that matter most, because getting any of them wrong
// produces a search that looks like it works:
//
//   - closing find restores the reader to where they were when they opened it,
//     independently of anything they typed into the composer meanwhile;
//   - the current match survives a rebuild when it still exists, so streaming
//     output cannot yank the reader off the hit they are looking at;
//   - new output does not move the current match, even when it adds matches;
//   - the search never submits anything to the agent.

// findTestModel returns a model with a transcript the tests can search.
func findTestModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "first prompt about parsers", session.ContentTypePlain)
	m.state.AddMessage(session.RoleAssistant, "an answer mentioning parsers and tokens", session.ContentTypeMarkdown)
	m.state.AddMessage(session.RoleUser, "a second prompt about tokens", session.ContentTypePlain)
	m.refreshViewport()
	return m
}

func TestNotebookFindSearchesOnlyVisibleRevisionOrder(t *testing.T) {
	doc := conversation.NewDocument([]conversation.Block{{
		ID: "narration:n", Kind: conversation.BlockNarration, Members: []string{"narration:n"}, Text: "Current headline",
		EventOrderAlternatives: []conversation.Block{{ID: "revision:old", Kind: conversation.BlockMessage, Members: []string{"revision:old"}, Text: "Earlier private parser detail", SourceRevision: 1}},
	}})
	query := conversation.NewFindQuery("Earlier private parser detail")
	sections := notebookFindDocument(doc, "scope", nil)
	if got := conversation.FindInDocument(sections, query, nil); len(got) != 0 {
		t.Fatalf("section-mode search exposed an unrendered revision: %+v", got)
	}
	eventOrder := notebookFindDocument(doc, "scope", map[itemKey]bool{notebookItemKey("scope", "narration:n"): true})
	got := conversation.FindInDocument(eventOrder, query, nil)
	if len(got) != 1 || got[0].Block != "revision:old" || got[0].Scroll != "narration:n" {
		t.Fatalf("event-order search did not locate the visible revision: %+v", got)
	}
}

// findScrollableModel returns a model whose transcript is TALLER than the
// viewport.
//
// The distinction is load-bearing for anything asserting that the reader MOVED.
// newTestModel's transcript fits inside 24 rows, so every scroll clamps to
// offset 0, the block at the top of the viewport never changes, and a jump is
// indistinguishable from a no-op. A test written against the short fixture can
// therefore pass while the jump does nothing at all — which is exactly what
// happened here.
func findScrollableModel(t *testing.T) Model {
	t.Helper()
	m := findTestModel(t)
	// The filler is a run of FINAL answers, not ordinary messages. Only a final
	// answer is rendered through the mapped path, so filler that was not final
	// would leave this fixture with no cell mapping at all — and every test that
	// selects or highlights in it would be asserting against something that
	// cannot exist.
	for i := 0; i < 20; i++ {
		m.state.AddMessageFinal(session.RoleAssistant,
			"filler answer to make the transcript taller than the viewport", session.ContentTypeMarkdown)
	}
	m.state.AddMessage(session.RoleUser, "the final prompt mentions tokens", session.ContentTypePlain)
	m.refreshViewport()
	if m.viewport.TotalLineCount() <= m.viewport.Height() {
		t.Fatalf("fixture is stale: %d lines fit in a %d-row viewport, so nothing can scroll",
			m.viewport.TotalLineCount(), m.viewport.Height())
	}
	if len(m.blockRenderSpans) == 0 {
		t.Fatal("fixture is stale: no block carries a cell mapping, so nothing can be selected or highlighted")
	}
	return m
}

func TestFindOpensEmptyAndClosed(t *testing.T) {
	m := findTestModel(t)
	if m.find.open {
		t.Fatal("find starts open")
	}
	if m.findStatus() != "" {
		t.Fatalf("a closed find shows status %q", m.findStatus())
	}
}

func TestFindQueryProducesResultsAndACount(t *testing.T) {
	m := findTestModel(t)
	m.openFind("parsers")

	if got := len(m.find.matches); got != 2 {
		t.Fatalf("got %d matches for \"parsers\", want 2: %+v", got, m.find.matches)
	}
	if got := m.findStatus(); !strings.Contains(got, "1/2") {
		t.Fatalf("status = %q, want it to report 1/2", got)
	}
}

func TestFindNoMatchSaysSoRatherThanShowingZero(t *testing.T) {
	m := findTestModel(t)
	m.openFind("zebra")
	if len(m.find.matches) != 0 {
		t.Fatalf("found %d matches for an absent query", len(m.find.matches))
	}
	got := m.findStatus()
	if !strings.Contains(got, "no match") {
		t.Fatalf("status = %q, want it to say there is no match", got)
	}
	if strings.Contains(got, "0/0") {
		t.Fatalf("status = %q tells the reader a count instead of an answer", got)
	}
}

func TestFindEmptyQueryReportsNoResultsAndNoMatch(t *testing.T) {
	m := findTestModel(t)
	m.openFind("")
	if len(m.find.matches) != 0 {
		t.Fatalf("an empty query produced %d matches", len(m.find.matches))
	}
}

// Navigation wraps, because a reader at the last hit pressing "next" means "go
// round again", not "do nothing".
func TestFindNextAndPreviousWrap(t *testing.T) {
	m := findTestModel(t)
	m.openFind("parsers")
	if m.find.current != 0 {
		t.Fatalf("find opened on match %d, want the first", m.find.current)
	}

	if got, want := m.nextFindMatch(), 1; got != want {
		t.Fatalf("next moved to %d, want %d", got, want)
	}
	// Past the last, back to the first.
	if got, want := m.nextFindMatch(), 0; got != want {
		t.Fatalf("next past the end gave %d, want a wrap to %d", got, want)
	}
	if got, want := m.prevFindMatch(), 1; got != want {
		t.Fatalf("previous from the first gave %d, want a wrap to %d", got, want)
	}
}

func TestFindNavigationWithOneMatchStaysPut(t *testing.T) {
	m := findTestModel(t)
	m.openFind("an answer")
	if len(m.find.matches) != 1 {
		t.Fatalf("precondition: got %d matches, want 1", len(m.find.matches))
	}
	if got := m.nextFindMatch(); got != 0 {
		t.Fatalf("next moved to %d with a single match", got)
	}
	if got := m.prevFindMatch(); got != 0 {
		t.Fatalf("previous moved to %d with a single match", got)
	}
}

func TestFindNavigationWithNoMatchesIsHarmless(t *testing.T) {
	m := findTestModel(t)
	m.openFind("zebra")
	if got := m.nextFindMatch(); got != 0 {
		t.Fatalf("next with no matches gave %d", got)
	}
	if got := m.prevFindMatch(); got != 0 {
		t.Fatalf("previous with no matches gave %d", got)
	}
}

// The entry anchor is the whole reason closing find is safe: the reader returns
// to the line they were reading when they opened it, not to wherever the last
// jump left them and not to the top.
func TestFindEscapeRestoresTheEntryAnchor(t *testing.T) {
	m := findScrollableModel(t)
	if len(m.blockSpans) < 3 {
		t.Fatalf("precondition: only %d blocks rendered", len(m.blockSpans))
	}
	// Put the reader deliberately near the top, on a block that is not the one
	// the search will jump to.
	first := m.blockSpans[0]
	m.viewportFollow = false
	m.viewport.SetYOffset(first.startLine)
	m.captureReadingAnchor()
	entry := m.readingAnchor
	if entry.Block == "" {
		t.Fatal("precondition: no reading anchor was captured")
	}

	m.openFind("tokens")
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want at least 2 to jump between", len(m.find.matches))
	}
	// Jump somewhere else entirely. The FIRST match may be the block we are
	// already on, so the jump steps forward before asserting it moved — an
	// assertion that the jump moved us, made without ensuring it could, is how
	// this test passed while the restore was a no-op.
	m.nextFindMatch()
	if !m.gotoFindMatch() {
		t.Fatal("the jump reported that it could not scroll to the match")
	}
	if m.readingAnchor.Block == entry.Block && m.readingAnchor.Offset == entry.Offset {
		t.Fatal("the jump did not move the reader, so the restore below proves nothing")
	}

	m.closeFind()

	if m.find.open {
		t.Fatal("find is still open after Esc")
	}
	if m.readingAnchor.Block != entry.Block || m.readingAnchor.Offset != entry.Offset {
		t.Fatalf("closing find left the reader at %q+%d, want the entry anchor %q+%d",
			m.readingAnchor.Block, m.readingAnchor.Offset, entry.Block, entry.Offset)
	}
	// And the viewport must actually be showing it, not merely record it.
	if row, ok := m.blockStartRow(entry.Block); ok {
		if got := m.viewport.YOffset(); got != row+entry.Offset {
			t.Fatalf("viewport is at row %d, want the entry block's row %d", got, row+entry.Offset)
		}
	}
}

// Opening find must capture the reader's position ITSELF rather than trust the
// reading anchor already on the model.
//
// In production the two differ: a reader scrolls, and the reading anchor is not
// updated until the next transcript rebuild. So the position find must restore
// is "where the viewport is NOW", and a version that only re-read the stored
// anchor would restore a position from before the scroll.
//
// The test is written the way production behaves — scroll WITHOUT capturing —
// because capturing first makes the model's own capture redundant and the test
// blind to its removal.
func TestFindEntryAnchorIsCapturedFromTheCurrentViewport(t *testing.T) {
	m := findScrollableModel(t)
	// Deliberately NOT captured: this is the state a reader is in after
	// scrolling, before anything rebuilds the transcript.
	m.viewportFollow = false
	m.viewport.SetYOffset(m.blockSpans[0].startLine)

	m.openFind("tokens")
	entry := m.find.entryAnchor
	if entry.Block == "" {
		t.Fatal("find captured no entry anchor at all")
	}
	// It must name the block actually under the top of the viewport, not
	// whatever the model happened to be holding.
	if want, _ := m.blockAtViewportTop(); entry.Block != want {
		t.Fatalf("find captured %q as the entry anchor, want the block at the top of the viewport (%q)",
			entry.Block, want)
	}
}

// A reader who was following the bottom when they opened find must be following
// again when they close it.
//
// Restoring the position but not the follow flag would leave them parked on a
// line in a conversation that is still streaming — and the next output would
// arrive below them with no way to notice. The two are captured and restored
// together for exactly this reason.
func TestFindClosingRestoresFollowing(t *testing.T) {
	m := findScrollableModel(t)
	// Following: the reader is pinned to the live end.
	m.viewportFollow = true
	m.viewport.GotoBottom()

	m.openFind("tokens")
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want at least 2", len(m.find.matches))
	}
	// A jump takes the reader off follow, which is what makes the restore
	// observable.
	m.nextFindMatch()
	if !m.gotoFindMatch() {
		t.Fatal("the jump reported that it could not scroll to the match")
	}
	if m.viewportFollow {
		t.Fatal("precondition: the jump left the reader following, so the restore proves nothing")
	}

	m.closeFind()

	if !m.viewportFollow {
		t.Fatal("closing a search opened from the live end left the reader parked instead of following")
	}
	if !m.viewport.AtBottom() {
		t.Fatal("the reader is flagged as following but the viewport is not at the bottom")
	}
}

// The entry anchor must be the reader's position when they opened find, and it
// must not be overwritten by the jumps find performs. A single reused anchor
// field would drift: each jump would capture its own destination, and closing
// would "restore" the reader to the last hit.
func TestFindEntryAnchorSurvivesRepeatedJumps(t *testing.T) {
	m := findScrollableModel(t)
	if len(m.blockSpans) < 2 {
		t.Fatalf("precondition: only %d blocks rendered", len(m.blockSpans))
	}
	first := m.blockSpans[0]
	m.viewportFollow = false
	m.viewport.SetYOffset(first.startLine)
	m.captureReadingAnchor()
	entry := m.readingAnchor

	m.openFind("tokens")
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want at least 2", len(m.find.matches))
	}
	for i := 0; i < 3; i++ {
		m.nextFindMatch()
		m.gotoFindMatch()
	}
	m.closeFind()

	if m.readingAnchor.Block != entry.Block || m.readingAnchor.Offset != entry.Offset {
		t.Fatalf("after three jumps, closing gave %q+%d, want the entry anchor %q+%d",
			m.readingAnchor.Block, m.readingAnchor.Offset, entry.Block, entry.Offset)
	}
}

// Enter accepts: it keeps the position and closes the input. The distinction
// from Esc is the point — Esc is "put me back", Enter is "I will stay here".
func TestFindEnterAcceptsAndKeepsThePosition(t *testing.T) {
	m := findScrollableModel(t)
	m.openFind("tokens")
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want at least 2", len(m.find.matches))
	}
	m.nextFindMatch()
	if !m.gotoFindMatch() {
		t.Fatal("the jump reported that it could not scroll to the match")
	}
	accepted := m.readingAnchor
	entry := m.find.entryAnchor
	if accepted.Block == entry.Block && accepted.Offset == entry.Offset {
		t.Fatal("precondition: accepting would prove nothing, the match is where we started")
	}

	m.acceptFind()

	if m.find.open {
		t.Fatal("find is still open after Enter")
	}
	if m.readingAnchor.Block != accepted.Block || m.readingAnchor.Offset != accepted.Offset {
		t.Fatalf("accepting moved the reader to %q+%d, want it left at the match %q+%d",
			m.readingAnchor.Block, m.readingAnchor.Offset, accepted.Block, accepted.Offset)
	}
}

// Rebuilding the document — which happens on every refresh, and therefore
// continuously while an answer streams — must not move the reader off the match
// they are on when that match is still there.
func TestFindKeepsTheCurrentMatchAcrossARebuild(t *testing.T) {
	m := findTestModel(t)
	m.openFind("tokens")
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: got %d matches, want at least 2", len(m.find.matches))
	}
	m.nextFindMatch()
	m.gotoFindMatch()
	current := m.find.current
	if current == 0 {
		t.Fatal("precondition: the current match did not move off the first")
	}
	want := m.find.matches[current]

	m.refreshFind()

	if len(m.find.matches) != 2 {
		t.Fatalf("the rebuild changed the result count to %d", len(m.find.matches))
	}
	if m.find.current != current {
		t.Fatalf("the rebuild moved the current match to %d, want %d", m.find.current, current)
	}
	if got := m.find.matches[m.find.current]; got.Block != want.Block || got.Range != want.Range {
		t.Fatalf("the current match is now %+v, want %+v", got, want)
	}
}

// New output that also matches must NOT move the reader. This is the streaming
// case: an answer arrives while a search is open, and the newest match is
// constantly changing under the reader's cursor.
func TestFindDoesNotJumpOnNewMatchingOutput(t *testing.T) {
	m := findTestModel(t)
	m.openFind("tokens")
	m.nextFindMatch()
	m.gotoFindMatch()
	current := m.currentFindMatchOrFail(t)
	before := len(m.find.matches)

	// A new turn arrives containing the same word.
	m.state.AddMessage(session.RoleAssistant, "more text about tokens", session.ContentTypeMarkdown)
	m.refreshFind()

	if len(m.find.matches) <= before {
		t.Fatalf("precondition: the new output added no matches (%d -> %d)", before, len(m.find.matches))
	}
	got := m.currentFindMatchOrFail(t)
	if got.Block != current.Block || got.Range != current.Range {
		t.Fatalf("new output moved the reader from %+v to %+v, want it left on the same hit", current, got)
	}
}

// The current match must be identified by its RANGE, not only by its block.
//
// Several matches in one block are ordinary — a paragraph mentioning the same
// word twice — and a carry-over that only compared blocks would move the reader
// from the second hit in a block to the first one as soon as anything else
// changed. This is the case the block-level tier exists to fall back FROM, and
// the only one that can tell the two tiers apart.
func TestFindKeepsTheReaderOnTheHitWithinABlock(t *testing.T) {
	m := newTestModel(t)
	// One block, two occurrences, with enough text between them that they are
	// genuinely two hits.
	const text = "the token is here, and further along there is another token too"
	m.state.AddMessage(session.RoleAssistant, text, session.ContentTypeMarkdown)
	m.refreshViewport()

	m.openFind("token")
	if len(m.find.matches) != 2 {
		t.Fatalf("precondition: got %d matches in one block, want 2: %+v", len(m.find.matches), m.find.matches)
	}
	if m.find.matches[0].Block != m.find.matches[1].Block {
		t.Fatal("precondition: the two matches are not in the same block")
	}
	// Step to the SECOND hit in the block.
	m.nextFindMatch()
	second := m.currentFindMatchOrFail(t)
	if second.Range == m.find.matches[0].Range {
		t.Fatal("precondition: the cursor is still on the first hit of the block")
	}

	// A rebuild whose match set is unchanged (an unrelated message arrives, so
	// the transcript is rebuilt but the hits are the same).
	m.state.AddMessage(session.RoleUser, "an unrelated remark", session.ContentTypePlain)
	m.refreshViewport()
	m.refreshFind()

	got := m.currentFindMatchOrFail(t)
	if got.Range != second.Range {
		t.Fatalf("the rebuild moved the reader from %+v to %+v, want the same hit in the same block",
			second, got)
	}
}

// currentFindMatchOrFail is the fixture's accessor, so a test that loses every
// match reports that rather than silently comparing zero values.
func (m Model) currentFindMatchOrFail(t *testing.T) conversation.FindMatch {
	t.Helper()
	match, ok := m.currentFindMatch()
	if !ok {
		t.Fatalf("there is no current match (out of %d results)", len(m.find.matches))
	}
	return match
}

// A match whose block vanishes (a branch rewind, a cleared run log) must drop
// out of the results rather than being kept by index: an index would silently
// point at a different match, and the reader would be sent somewhere they did
// not ask for.
func TestFindDropsMatchesWhoseBlockDisappears(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(710, 0)
	m.state.AddMessage(session.RoleUser, "alpha target", session.ContentTypePlain)
	// A run event's searchable text is its Body: the card renders the body, and
	// the title is metadata that is not part of what a reader reads.
	m.state.AddRunEvent(session.RunEvent{
		Kind: session.RunEventCommit, TaskN: 1, Title: "task one", Body: "beta target", At: at,
	})
	m.refreshViewport()
	m.openFind("target")
	if len(m.find.matches) != 2 {
		t.Fatalf("precondition: got %d matches, want 2: %+v", len(m.find.matches), m.find.matches)
	}

	// ClearRunEvents empties the run log, which is how a branch rewind and a new
	// user turn both make blocks disappear.
	m.state.ClearRunEvents()
	m.refreshViewport()
	m.refreshFind()

	if len(m.find.matches) != 1 {
		t.Fatalf("got %d matches after the run log was cleared, want only the survivor: %+v",
			len(m.find.matches), m.find.matches)
	}
	if m.find.matches[0].Block != m.blockSpans[0].id {
		t.Fatalf("the survivor is %+v, want the message that is still there", m.find.matches[0])
	}
	// The count itself must be honest: the disappeared match is gone from the
	// denominator too, not merely from the list.
	if got := m.findStatus(); !strings.Contains(got, "1/1") {
		t.Fatalf("status = %q, want it to report 1/1", got)
	}
}

// A match whose block disappears entirely leaves "no match" rather than a count
// over an empty list.
func TestFindSaysNoMatchWhenEveryBlockDisappears(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(711, 0)
	m.state.AddRunEvent(session.RunEvent{
		Kind: session.RunEventCommit, TaskN: 1, Title: "task one", Body: "vanishing target", At: at,
	})
	m.refreshViewport()
	m.openFind("vanishing")
	if len(m.find.matches) != 1 {
		t.Fatalf("precondition: got %d matches, want 1: %+v", len(m.find.matches), m.find.matches)
	}

	m.state.ClearRunEvents()
	m.refreshViewport()
	m.refreshFind()

	if len(m.find.matches) != 0 {
		t.Fatalf("matches survived their blocks: %+v", m.find.matches)
	}
	if got := m.findStatus(); !strings.Contains(got, "no match") {
		t.Fatalf("status = %q, want it to report that nothing matches", got)
	}
}

// The composer's draft is untouched: find is a reading surface, and a reader
// who opens it mid-sentence must find their sentence still there.
func TestFindLeavesTheDraftAlone(t *testing.T) {
	m := findTestModel(t)
	m.input.SetValue("a half-written prompt")
	m.openFind("tokens")
	m.nextFindMatch()
	m.gotoFindMatch()
	m.closeFind()

	if got := m.input.Value(); got != "a half-written prompt" {
		t.Fatalf("the draft became %q", got)
	}
}

// Opening find must not send anything to the agent. This is the assertion that
// stops a future change from routing the query through the prompt path, which
// would look identical to the reader until the model answered something.
func TestFindSubmitsNothingToTheAgent(t *testing.T) {
	m := findTestModel(t)
	before := len(m.state.Messages())

	m.openFind("parsers")
	m.nextFindMatch()
	m.gotoFindMatch()
	m.acceptFind()

	if got := len(m.state.Messages()); got != before {
		t.Fatalf("find added %d messages to the session", got-before)
	}
	if m.busy {
		t.Fatal("find started a turn")
	}
	if q := m.state.SteeringQueue(); len(q) != 0 {
		t.Fatalf("find queued %d steering messages", len(q))
	}
}

// Searching while drilled into a child searches the CHILD's transcript, which
// is what is on screen. Searching the parent would offer jumps to lines the
// reader cannot see from here.
func TestFindSearchesTheDrilledChildTranscript(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "parent text about widgets", session.ContentTypePlain)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore", child)
	child.AddMessage(session.RoleUser, "child text about gadgets", session.ContentTypePlain)
	m.drillIntoSubagent(view)
	m.refreshViewport()

	m.openFind("gadgets")
	if len(m.find.matches) != 1 {
		t.Fatalf("the child's text was not searched: %+v", m.find.matches)
	}
	m.refreshFind()
	if len(m.find.matches) != 1 {
		t.Fatalf("a refresh lost the child's match: %+v", m.find.matches)
	}

	if got := conversation.FindInDocument(m.conversationDocument(), conversation.NewFindQuery("widgets"), nil); len(got) != 0 {
		t.Fatalf("the parent's text is still searchable while drilled in: %+v", got)
	}
}

// A query with Unicode must match case-insensitively and still name the
// reader's own bytes, which is the adapter's and the search's contract meeting.
func TestFindUnicodeQueryNamesTheOriginalBytes(t *testing.T) {
	m := newTestModel(t)
	const text = "CAFÉ is where we meet"
	m.state.AddMessage(session.RoleUser, text, session.ContentTypePlain)
	m.refreshViewport()

	m.openFind("café")
	if len(m.find.matches) != 1 {
		t.Fatalf("got %d matches for a case-folded query: %+v", len(m.find.matches), m.find.matches)
	}
	hit := m.find.matches[0]
	block, ok := m.conversationDocument().Block(hit.Block)
	if !ok {
		t.Fatalf("the match names a block the document does not have: %q", hit.Block)
	}
	if got := block.Text[hit.Range.Start:hit.Range.End]; got != "CAFÉ" {
		t.Fatalf("the match names %q, want %q", got, "CAFÉ")
	}
}

// A query typed one character at a time must produce the same answer as the
// whole query typed at once: the incremental path is the one a reader uses, so
// it is the one that has to be right.
//
// Both runs happen in ONE model, because a block identity is scoped per State
// instance: comparing two models would be comparing identifiers that are
// deliberately different, and the test would fail for a reason that has nothing
// to do with typing. The ranges are compared for the same reason plus one more
// — the offsets are what a highlight uses, so a match that agreed on the block
// but not on the range would mark the wrong characters.
func TestFindIncrementalTypingMatchesTheFinishedQuery(t *testing.T) {
	m := findTestModel(t)
	m.openFind("")
	for _, prefix := range []string{"p", "pa", "par", "pars", "parse", "parsers"} {
		m.setFindQuery(prefix)
	}
	incremental := m.find.matches
	if len(incremental) == 0 {
		t.Fatal("typing a full query produced no matches at all")
	}

	// The same query, set in one step, in the same model.
	m.acceptFind()
	m.openFind("parsers")
	atOnce := m.find.matches

	if len(incremental) != len(atOnce) {
		t.Fatalf("typing gave %d matches, the whole query gave %d", len(incremental), len(atOnce))
	}
	for i := range incremental {
		if incremental[i].Block != atOnce[i].Block || incremental[i].Range != atOnce[i].Range || incremental[i].Scroll != atOnce[i].Scroll {
			t.Fatalf("match %d differs: %+v vs %+v", i, incremental[i], atOnce[i])
		}
	}
}

// A query that narrows across keystrokes must keep the reader in the BLOCK they
// were on. Restarting at the first result of the whole conversation on every
// character is the difference between a search that narrows and one that
// flickers back to the top.
//
// The assertion is about the block, not the range, and that is not a weakening:
// a longer query matches a LONGER span, so its range is necessarily different
// from the shorter query's. Demanding the same range would demand that narrowing
// preserve the matched text, which it cannot.
func TestFindTypingNarrowerKeepsTheReadersBlock(t *testing.T) {
	m := findTestModel(t)
	m.openFind("tokens")
	if len(m.find.matches) != 2 {
		t.Fatalf("precondition: got %d matches for \"tokens\", want 2: %+v", len(m.find.matches), m.find.matches)
	}
	// Step to the SECOND hit, so "stayed put" cannot be confused with "did not
	// move off the first".
	m.nextFindMatch()
	kept := m.find.matches[m.find.current]
	if m.find.current == 0 {
		t.Fatal("precondition: the reader is still on the first match")
	}

	// A longer query that still matches the hit the reader is on. It has to be
	// chosen from the block actually kept: a query that does not match it would
	// be testing a different thing entirely.
	longer := "about tokens"
	if strings.Contains(m.blockTextFor(t, kept.Block), "parsers") {
		longer = "about parsers"
	}
	m.setFindQuery(longer)

	found, ok := m.currentFindMatch()
	if !ok {
		t.Fatalf("the narrowed query lost every match; the reader was on %+v", kept)
	}
	if found.Block != kept.Block {
		t.Fatalf("narrowing moved the reader from block %q to %q", kept.Block, found.Block)
	}
}

// blockTextFor returns the readable text of a block, for a test that needs to
// choose a query that actually matches it.
func (m Model) blockTextFor(t *testing.T, id conversation.BlockID) string {
	t.Helper()
	block, ok := m.conversationDocument().Block(id)
	if !ok {
		t.Fatalf("no block %q in the document", id)
	}
	return conversation.ReadableText(block)
}

// Narrowing must not throw the reader to the first hit of the conversation when
// the block they were on no longer matches at all: the nearest position is what
// they meant, and "the top of the transcript" is the opposite of it.
func TestFindTypingBroaderKeepsTheReaderNearTheirPlace(t *testing.T) {
	m := findScrollableModel(t)
	m.openFind("the final prompt mentions tokens")
	if len(m.find.matches) == 0 {
		t.Fatal("precondition: the specific query matched nothing")
	}
	onLast := m.find.matches[0]

	// Broaden it, so the reader's exact hit is gone and earlier blocks match too.
	m.setFindQuery("tokens")
	if len(m.find.matches) < 2 {
		t.Fatalf("precondition: broadening produced only %d matches", len(m.find.matches))
	}
	found, ok := m.currentFindMatch()
	if !ok {
		t.Fatal("the broadened query lost every match")
	}
	if found.Block != onLast.Block {
		t.Fatalf("broadening moved the reader from block %q to %q, want it to stay on the nearest",
			onLast.Block, found.Block)
	}
}

// A match inside a collapsed group must scroll to the GROUP.
//
// The match names the member it was found in, and a member is not a top-level
// block: nothing renders at its row, so scrolling to it finds nothing and the
// jump either does nothing or lands somewhere unrelated. The group is what is on
// screen, and it is a separate field precisely so the two cannot be confused.
func TestFindJumpScrollsToTheGroupForACollapsedMatch(t *testing.T) {
	m := findScrollableModel(t)
	at := time.Unix(720, 0)
	// A run of same-tool reads that collapses into one group, with the sought
	// text inside the LAST member.
	for i := 0; i < 4; i++ {
		content := "ordinary captured output"
		if i == 3 {
			content = "the distinctive needle lives here"
		}
		m.state.LogToolCall(registry.AuditEvent{
			ToolName: "file.read", Timestamp: at.Add(time.Duration(i) * time.Second),
			ResultContent: content,
		})
	}
	m.refreshViewport()

	m.openFind("distinctive needle")
	if len(m.find.matches) != 1 {
		t.Fatalf("precondition: got %d matches, want 1: %+v", len(m.find.matches), m.find.matches)
	}
	match := m.find.matches[0]
	if match.Block == match.Scroll {
		t.Fatalf("precondition: the match is not inside a collapsed group (block %q == scroll %q)",
			match.Block, match.Scroll)
	}
	// The member is NOT a rendered block, so scrolling to it must find nothing.
	if _, ok := m.blockStartRow(match.Block); ok {
		t.Fatalf("precondition: the member %q is a rendered block after all", match.Block)
	}
	if _, ok := m.blockStartRow(match.Scroll); !ok {
		t.Fatalf("the group %q is not rendered either, so nothing can scroll to it", match.Scroll)
	}

	// The jump must reach the group's row, which requires using Scroll.
	if !m.gotoFindMatch() {
		t.Fatal("the jump reported that it could not scroll to a match whose group IS rendered")
	}
	want, _ := m.blockStartRow(match.Scroll)
	if got := m.viewport.YOffset(); got != want {
		t.Fatalf("the jump scrolled to row %d, want the group's row %d", got, want)
	}
}

// The index behind the search must not outlive a session: a block identity is
// scoped per session, so carrying entries across a switch would serve one
// conversation's projection for another's block.
func TestFindIndexIsResetOnANewConversation(t *testing.T) {
	m := findTestModel(t)
	m.openFind("parsers")
	if m.findIndex == nil {
		t.Fatal("find did not build an index")
	}
	if m.findIndex.Len() == 0 {
		t.Fatal("the index holds nothing after a search")
	}
	m.resetFindIndex()
	if got := m.findIndex.Len(); got != 0 {
		t.Fatalf("the index holds %d entries after a reset", got)
	}
}

// With no transcript there is nothing to search, and the UI must say that
// rather than showing a count of zero.
func TestFindOnAnEmptyConversation(t *testing.T) {
	m := newTestModel(t)
	m.refreshViewport()
	m.openFind("anything")
	if len(m.find.matches) != 0 {
		t.Fatalf("an empty conversation produced %d matches", len(m.find.matches))
	}
	if got := m.findStatus(); !strings.Contains(got, "no match") {
		t.Fatalf("status = %q", got)
	}
	m.closeFind()
}

// The scope note is only shown when it is true. A reader whose search covered a
// capped tool result must be told; one whose search covered complete text must
// not be, or the note becomes noise that everyone learns to ignore.
func TestFindReportsTheSearchedScopeOnlyWhenTruncated(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleUser, "ordinary complete text", session.ContentTypePlain)
	m.refreshViewport()
	m.openFind("ordinary")
	if got := m.findStatus(); strings.Contains(got, "truncated") {
		t.Fatalf("status %q claims a scope problem that does not exist", got)
	}

	m2 := newTestModel(t)
	m2.state.LogToolCall(auditEventWithNotice())
	m2.refreshViewport()
	m2.openFind("captured")
	if got := m2.findStatus(); !strings.Contains(got, "truncated") {
		t.Fatalf("status = %q, want it to disclose that it searched capped output", got)
	}
}
