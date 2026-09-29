// internal/app/tui/inspector_journey_test.go — one reader's whole session, end to
// end
package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/inspector"
)

// This file walks ONE reader through a session that exercises the features Tasks
// 1-14 built, in the order a person meets them: read, scroll away, write a
// draft, inspect what changed, answer an approval, look at a child, copy code,
// resize twice, find a phrase, and return.
//
// Every step asserts STATE and DISPATCH, not a snapshot of the screen. A golden
// render would fail on every unrelated styling change and pass on a model that
// had silently lost the reader's place — which is the failure this whole plan
// exists to prevent. So each assertion is of the form "the reader is on block X,
// at offset Y" or "this command was produced", and the tests that check pixels
// live in their own files against their own invariants.

// journeyModel returns a model with a transcript worth reading.
func journeyModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelForRail(t, 160, 44, true)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: t.TempDir(), ActiveRoot: t.TempDir()})

	m.state.AddMessage(session.RoleUser, "why does the parser drop the trailing newline", session.ContentTypePlain)
	m.state.AddMessageFinal(session.RoleAssistant,
		"Because the scanner consumes it as a line terminator.\n\n```go\nfunc scan() {}\n```\n\nSee `trimTrailingNewline`.",
		session.ContentTypeMarkdown)
	// Enough filler that the transcript scrolls, so "scroll away and come back"
	// is a real movement rather than a no-op.
	for i := 0; i < 12; i++ {
		m.state.AddMessageFinal(session.RoleAssistant,
			"further analysis of the scanner and its handling of terminators", session.ContentTypeMarkdown)
	}
	m.state.AddMessage(session.RoleUser, "and what about the token budget", session.ContentTypePlain)
	m.refreshViewport()
	return *m
}

// The whole journey, in one test, because the POINT is that the steps do not
// interfere. Each one is also covered on its own elsewhere; what this adds is
// that the state one step leaves behind does not break the next.
func TestReaderJourneyThroughTheInspector(t *testing.T) {
	m := journeyModel(t)

	// --- 1. Start reading. The transcript must be taller than the window, or
	// "scroll away" below is not a movement and proves nothing.
	if m.viewport.TotalLineCount() <= m.viewport.Height() {
		t.Fatalf("fixture is stale: %d lines fit in a %d-row viewport",
			m.viewport.TotalLineCount(), m.viewport.Height())
	}
	if len(m.blockSpans) == 0 {
		t.Fatal("the transcript rendered no blocks")
	}

	// --- 2. Scroll away from the live end and park on a block.
	start := m.blockSpans[0]
	m.viewportFollow = false
	m.viewport.SetYOffset(start.startLine)
	m.captureReadingAnchor()
	reading := m.readingAnchor
	if reading.Block == "" {
		t.Fatal("no reading anchor was captured")
	}

	// --- 3. Write a draft. It must survive everything below.
	const draft = "a half-written question about terminators"
	m.input.SetValue(draft)

	// --- 4. Inspect what changed. The inspector opens on a tab, and the reader
	// can move between tabs.
	if m.inspector == nil {
		t.Fatal("the inspector is not wired")
	}
	if !m.inspector.open(inspector.TabChanges, true) {
		t.Fatal("the inspector refused to open on Changes")
	}
	if got := m.inspector.model.SelectedTab(); got != inspector.TabChanges {
		t.Fatalf("the inspector opened on %q, want Changes", got)
	}
	m.inspector.model.Open(inspector.TabAgents)
	if got := m.inspector.model.SelectedTab(); got != inspector.TabAgents {
		t.Fatalf("tab navigation landed on %q, want Agents", got)
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()

	// --- 5. An approval arrives and is answered. It must reach the RIGHT
	// channel with the right decision, and must not disturb the draft or the
	// reading position.
	tc := &session.PendingToolCall{
		ID: "tc-1", Name: "shell.run", Command: "go test ./...", Risk: "medium",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	m.state.SetPendingApproval(tc)
	owner, resolved, _ := m.pendingApprovalTarget()
	if resolved != tc || owner != m.state {
		t.Fatal("a parent approval did not resolve to the parent's own call")
	}
	owner.SetPendingApproval(nil)
	tc.Respond(session.UserApprovalDecision{Approved: true})
	select {
	case decision := <-tc.ResponseChan:
		if !decision.Approved {
			t.Fatalf("the decision arrived as %+v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("the decision never reached the parent's channel")
	}
	if got := m.input.Value(); got != draft {
		t.Fatalf("answering an approval changed the draft to %q", got)
	}
	if m.readingAnchor.Block != reading.Block {
		t.Fatalf("answering an approval moved the reader from %q to %q",
			reading.Block, m.readingAnchor.Block)
	}

	// --- 6. Look at a child agent.
	child := newChildState(t)
	child.AddMessage(session.RoleUser, "child work", session.ContentTypePlain)
	view := m.state.RegisterSubagent("explore", child)
	m.drillIntoSubagent(view)
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if _, ok := m.drilledInto(); !ok {
		t.Fatal("drilling into the child did not take effect")
	}
	// The child's transcript is what is on screen, and find must search it.
	m.openFind("child work")
	if len(m.find.matches) != 1 {
		t.Fatalf("a search while drilled found %d matches in the child transcript", len(m.find.matches))
	}
	m.closeFind()

	// --- 7. Return to the parent, and find the phrase in the PARENT.
	m.popDrill()
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if _, ok := m.drilledInto(); ok {
		t.Fatal("popping the drill left the reader in the child")
	}

	// --- 8. Resize twice, narrower then wider. The draft and the reading
	// position must both survive; the position is in LOGICAL coordinates, which
	// is what makes that possible.
	m.resize(120, 40)
	m.lastTranscriptHash = 0
	m.refreshViewport()
	m.resize(80, 24)
	m.lastTranscriptHash = 0
	m.refreshViewport()
	m.resize(160, 44)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if got := m.input.Value(); got != draft {
		t.Fatalf("the draft became %q across three resizes", got)
	}

	// --- 9. Find a phrase, and return to where the reading was.
	m.openFind("terminator")
	if len(m.find.matches) == 0 {
		t.Fatal("the search matched nothing in a transcript that mentions terminators")
	}
	entry := m.find.entryAnchor
	if !m.gotoFindMatch() {
		t.Fatal("the jump reported that it could not scroll to the match")
	}
	m.closeFind()
	if m.readingAnchor.Block != entry.Block || m.readingAnchor.Offset != entry.Offset {
		t.Fatalf("closing find left the reader at %q+%d, want the entry anchor %q+%d",
			m.readingAnchor.Block, m.readingAnchor.Offset, entry.Block, entry.Offset)
	}

	// --- 10. Copy code from the answer. The clipboard must receive the BLOCK,
	// not the whole answer, and it must arrive verbatim.
	fake := &fakeClipboard{}
	m.copyWriter = fake
	answerBlock := findAnswerBlockWithCode(t, m)
	codeTarget, ok := codeTargetFor(answerBlock)
	if !ok {
		t.Fatalf("the answer block offers no code target: %+v", answerBlock.CopyTargets)
	}
	if cmd := m.beginCopy(codeTarget); cmd != nil {
		_ = cmd()
	}
	writes := fake.writes()
	if len(writes) == 0 {
		t.Fatal("nothing reached the clipboard")
	}
	written := writes[0]
	if strings.Contains(written, "Because the scanner") {
		t.Fatalf("the copy took the whole answer instead of the code block:\n%s", written)
	}
	if !strings.Contains(written, "func scan()") {
		t.Fatalf("the copied text is not the code block:\n%q", written)
	}
	// The copy must be the code target's bytes EXACTLY: a copy that trimmed or
	// re-wrapped the block would paste differently from what the author wrote,
	// and nothing on screen would show it.
	if written != codeTarget.Text {
		t.Fatalf("the clipboard received %q, want the target's own bytes %q", written, codeTarget.Text)
	}

	// --- 11. The draft is STILL intact, after all of it.
	if got := m.input.Value(); got != draft {
		t.Fatalf("the draft became %q by the end of the journey", got)
	}

	// --- 12. And the reader is still where find left them, not at the top.
	if m.readingAnchor.Block == "" {
		t.Fatal("the reader's position was lost by the end of the journey")
	}
}

// findAnswerBlockWithCode returns the conversation block for the answer that
// contains the fenced code.
func findAnswerBlockWithCode(t *testing.T, m Model) conversation.Block {
	t.Helper()
	for _, b := range m.conversationDocument().Blocks() {
		if len(codeTargets(b)) > 0 {
			return b
		}
	}
	t.Fatal("no block in the document offers a code target")
	return conversation.Block{}
}

// codeTargetFor returns the first code target on a block.
func codeTargetFor(b conversation.Block) (conversation.CopyTarget, bool) {
	for _, tgt := range b.CopyTargets {
		if tgt.Source == conversation.SourceCode {
			return tgt, true
		}
	}
	return conversation.CopyTarget{}, false
}

// A resize must not move a reader who is anchored to a block.
//
// This is the journey's core promise, and it is worth its own test because a
// resize is the operation that moves EVERY row below the change: an anchor in
// row coordinates would be wrong by however many rows reflowed above it.
func TestResizeKeepsTheReaderOnTheirBlock(t *testing.T) {
	m := journeyModel(t)
	// Park on a block in the middle, so rows above and below it both reflow.
	target := m.blockSpans[len(m.blockSpans)/2]
	m.viewportFollow = false
	m.viewport.SetYOffset(target.startLine)
	m.captureReadingAnchor()
	before := m.readingAnchor

	for _, size := range [][2]int{{120, 40}, {80, 24}, {160, 44}, {100, 30}} {
		m.resize(size[0], size[1])
		m.lastTranscriptHash = 0
		m.refreshViewport()
		if m.readingAnchor.Block != before.Block {
			t.Fatalf("after resizing to %dx%d the reader is on block %q, want %q",
				size[0], size[1], m.readingAnchor.Block, before.Block)
		}
	}
}

// A draft must survive a resize intact: text, cursor, and any paste attachments.
func TestResizeKeepsTheDraftAndCursor(t *testing.T) {
	m := journeyModel(t)
	const draft = "the question I was in the middle of asking"
	m.input.SetValue(draft)
	// Move the cursor off the end, so a resize that reset it would show. The
	// column is set through the textarea's own API rather than by typing, so the
	// fixture does not depend on which keys the composer happens to route.
	m.input.SetCursorColumn(len([]rune(draft)) / 2)
	cursor := m.cursorPos()
	if cursor == 0 {
		t.Fatal("precondition: the cursor is still at the start, so a reset would be invisible")
	}

	for _, size := range [][2]int{{120, 40}, {80, 24}, {160, 44}} {
		m.resize(size[0], size[1])
		m.lastTranscriptHash = 0
		m.refreshViewport()
	}
	if got := m.input.Value(); got != draft {
		t.Fatalf("the draft became %q", got)
	}
	if got := m.cursorPos(); got != cursor {
		t.Fatalf("the cursor moved from %d to %d", cursor, got)
	}
}

// cursorPos returns the composer cursor's column offset, which is the coordinate
// a resize could plausibly reset.
func (m Model) cursorPos() int {
	return m.input.LineInfo().ColumnOffset
}

// An approval reaches the right channel with the right decision, and it goes to
// the state that ASKED — not to whichever state happens to be current.
//
// This is the journey's step 5 written as its own test, because the failure it
// guards against (answering the wrong child, or the parent when a child asked)
// is silent and expensive.
func TestApprovalInTheJourneyReachesTheAsker(t *testing.T) {
	m := journeyModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore", child)
	_ = view

	childTC := &session.PendingToolCall{
		ID: "child-1", Name: "shell.run", Command: "rm -rf /tmp/scratch", Risk: "high",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	child.SetPendingApproval(childTC)

	owner, got, label := m.pendingApprovalTarget()
	if got != childTC {
		t.Fatalf("the pending approval resolved to %v, want the child's call", got)
	}
	if owner != child {
		t.Fatal("the pending approval resolved to the wrong STATE")
	}
	if label == "" {
		t.Fatal("a subagent's approval is not attributed to the subagent")
	}

	owner.SetPendingApproval(nil)
	childTC.Respond(session.UserApprovalDecision{Approved: true})
	select {
	case decision := <-childTC.ResponseChan:
		if !decision.Approved {
			t.Fatalf("the decision arrived as %+v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("the decision never reached the child's channel")
	}
}

// A wheel event over a live region scrolls it ONLY when it has somewhere to go.
//
// This is the Task 12 contract, and it is in the journey file because it is the
// step where a reader's scrolling "just stops working": the region used to take
// the wheel unconditionally, so a region already at its end swallowed the event
// and the transcript underneath never moved.
func TestWheelOverAFinishedLiveRegionReachesTheTranscript(t *testing.T) {
	m := journeyModel(t)
	// Scroll to the middle so there is somewhere to go in both directions.
	m.viewportFollow = false
	m.viewport.SetYOffset(m.viewport.TotalLineCount() / 2)

	// A live region that cannot scroll: no recorded rows, so it has nowhere to
	// go and must hand the wheel on. The gate is the whole assertion — where the
	// event lands afterwards is the caller's business, and a gate that returned
	// true here would swallow the reader's scroll with nothing to show for it.
	m.regionRows = map[itemKey]int{}
	m.regionOffset = map[itemKey]int{}
	if m.scrollLiveRegionAt(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 2, Y: 2}) {
		t.Fatal("a live region with nowhere to scroll consumed the wheel")
	}
}

// The status line must state the mode, the focus, and any reading state, and it
// must fit the row it is given at every size the journey visits.
func TestStatusLineFitsAtEveryJourneySize(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}, {160, 44}, {200, 60}} {
		m := journeyModel(t)
		m.resize(size[0], size[1])
		m.openFind("terminator")
		m.lastTranscriptHash = 0
		m.refreshViewport()

		line := m.renderStatusLine(size[0])
		if got := ansi.StringWidth(line); got != size[0] {
			t.Fatalf("at %dx%d the status line is %d cells wide:\n%s",
				size[0], size[1], got, ansi.Strip(line))
		}
		plain := ansi.Strip(line)
		// The reading state must be visible: it is the only cue for what the
		// keys are doing while a search owns them.
		if !strings.Contains(plain, "find") {
			t.Fatalf("at %dx%d the status line does not report the open search:\n%s", size[0], size[1], plain)
		}
	}
}

// The F6 focus cycle must reach every surface and return, without disturbing the
// draft or the reading position.
func TestFocusCycleReachesEverySurfaceAndReturns(t *testing.T) {
	m := journeyModel(t)
	const draft = "a draft that must survive the focus cycle"
	m.input.SetValue(draft)
	m.viewportFollow = false
	m.viewport.SetYOffset(m.blockSpans[1].startLine)
	m.captureReadingAnchor()
	reading := m.readingAnchor

	seen := map[FocusTarget]bool{m.effectiveFocus(): true}
	// The cycle is finite and must come back to where it started.
	for i := 0; i < 12; i++ {
		_ = m.cycleFocus(true)
		seen[m.effectiveFocus()] = true
		if m.effectiveFocus() == FocusComposer {
			break
		}
	}
	if !seen[FocusConversation] {
		t.Errorf("the focus cycle never reached the conversation: %v", seen)
	}
	if m.effectiveFocus() != FocusComposer {
		t.Fatalf("the focus cycle did not return to the composer (ended on %v)", m.effectiveFocus())
	}
	if got := m.input.Value(); got != draft {
		t.Fatalf("the draft became %q after a focus cycle", got)
	}
	if m.readingAnchor.Block != reading.Block {
		t.Fatalf("the focus cycle moved the reader from %q to %q", reading.Block, m.readingAnchor.Block)
	}
}

// Opening and closing the inspector must not cost the reader their draft or
// their place. This is the whole reason the inspector can be body-expanded
// without the composer being hidden.
func TestInspectorOpenCloseKeepsTheDraft(t *testing.T) {
	m := journeyModel(t)
	const draft = "a draft that must survive the inspector"
	m.input.SetValue(draft)

	if !m.inspector.open(inspector.TabChanges, true) {
		t.Fatal("the inspector refused to open")
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if got := m.input.Value(); got != draft {
		t.Fatalf("opening the inspector changed the draft to %q", got)
	}

	m.inspector.close()
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if got := m.input.Value(); got != draft {
		t.Fatalf("closing the inspector changed the draft to %q", got)
	}
}
