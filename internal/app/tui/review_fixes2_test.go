// internal/app/tui/review_fixes2_test.go — regressions for the second review
// pass: the mapped body's row origin, inspector freshness, the session-scoped
// reset, and the transcript path's selection revision.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/inspector"
)

// ---- C2: the mapped body's row origin -------------------------------

// reasoningAnswerModel builds a model whose single answer carries captured
// reasoning, so its block renders the `⚙ thought for Ns ▸` summary line above
// the mapped body.
func reasoningAnswerModel(t *testing.T, answer string) (Model, renderedBlockSpan) {
	t.Helper()
	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: t.TempDir()})
	// The reasoning is attached to the final message by the session, exactly as
	// a reasoning-capable provider does: BeginStreaming/AppendThinking, then the
	// answer, which copies the captured text onto the message.
	m.state.BeginStreaming()
	m.state.AppendThinking("weighing the options")
	m.state.AddMessageFinal(session.RoleAssistant, answer, session.ContentTypeMarkdown)
	m.refreshViewport()

	if len(m.blockRenderSpans) == 0 {
		t.Fatalf("the reasoning answer published no mapping: %+v", m.blockRenderSpans)
	}
	for _, s := range m.blockRenderSpans {
		if strings.Contains(s.rendered.Logical, answer) {
			return m, s
		}
	}
	t.Fatalf("no mapped block holds %q: %+v", answer, m.blockRenderSpans)
	return m, renderedBlockSpan{}
}

// TestReasoningSummaryDoesNotShiftTheMappedBody is the regression for the
// review's Critical 2.
//
// renderFinalAnswerWithSink writes the salvage note and the `thought for Ns ▸`
// summary BEFORE the mapped body, while addBlock claimed the mapping at the
// block's FIRST row. Every row-based lookup then converted row-blockRow into the
// body's row, so the summary line mapped onto body row 0 and every offset in the
// block was shifted by the number of leading lines.
//
// The offset is not an edge case: appendMessage copies inProgress.Reasoning onto
// every final message, so it is the ordinary shape of an answer from any
// reasoning-capable model.
func TestReasoningSummaryDoesNotShiftTheMappedBody(t *testing.T) {
	const answer = "alpha beta gamma"
	m, span := reasoningAnswerModel(t, answer)

	if span.bodyOffset <= 0 {
		t.Fatalf("the block records bodyOffset %d for an answer with a reasoning summary: "+
			"the summary line is being treated as part of the body", span.bodyOffset)
	}

	lines := strings.Split(m.viewport.GetContent(), "\n")
	if span.blockRow >= len(lines) {
		t.Fatalf("the block claims row %d but the transcript has %d lines", span.blockRow, len(lines))
	}
	// The block's FIRST line must be the reasoning summary, not the answer: if
	// the fixture stopped producing one, the rest of this test would pass while
	// proving nothing.
	first := ansi.Strip(lines[span.blockRow])
	if !strings.Contains(first, "thought for") {
		t.Fatalf("the block's first line is %q, want the reasoning summary — the fixture "+
			"no longer exercises the leading-line case", first)
	}

	// THE assertion. The body's first row must be the answer's own first line.
	bodyRow := span.bodyRow()
	if bodyRow >= len(lines) {
		t.Fatalf("the body starts at row %d but the transcript has %d lines", bodyRow, len(lines))
	}
	drawn := ansi.Strip(lines[bodyRow])
	if !strings.Contains(drawn, "alpha") {
		t.Fatalf("the body's first row %d reads %q, want the answer's first line", bodyRow, drawn)
	}

	id, off, ok := m.OffsetAtTranscriptCell(bodyRow, 0)
	if !ok {
		t.Fatal("a cell on the body's first row resolved to nothing")
	}
	if id != span.id {
		t.Fatalf("the body's first row resolved to block %q, want %q", id, span.id)
	}
	// Cell 0 is the gutter, so the exact offset is the gutter's business; what
	// matters is that it is the mapping's own answer for BODY ROW 0 rather than
	// for the row the summary occupies.
	if want := span.rendered.OffsetAt(0, 0); off != want {
		t.Fatalf("the body's first row resolved to offset %d, want %d (the mapping's own "+
			"answer for body row 0)", off, want)
	}

	// And the summary row itself must NOT resolve into the body: it carries no
	// mapping, and mapping it onto body row 0 is precisely the bug.
	if _, _, ok := m.OffsetAtTranscriptCell(span.blockRow, 0); ok {
		t.Fatal("the reasoning summary row resolved into the body: it has no mapping")
	}
}

// TestSelectionAndCopyUseTheHighlightedRowWithAReasoningSummary is the reader-
// visible half of the same bug: a drag over a phrase must copy the phrase the
// highlight is on.
func TestSelectionAndCopyUseTheHighlightedRowWithAReasoningSummary(t *testing.T) {
	const answer = "alpha beta gamma"
	m, span := reasoningAnswerModel(t, answer)

	row := span.bodyRow()
	startCell, ok := span.rendered.CellAt(0, 0)
	if !ok {
		t.Fatal("precondition: no cell for body offset 0")
	}
	endOff := strings.Index(span.rendered.Logical, "beta")
	if endOff < 0 {
		t.Fatal("fixture is stale: the answer does not contain the word being selected")
	}
	endCell, ok := span.rendered.CellAt(0, endOff)
	if !ok {
		t.Fatal("precondition: no cell for the end offset")
	}
	if !m.beginSelectionAt(row, startCell) {
		t.Fatal("a press on the body's first row began no selection")
	}
	m.extendSelectionTo(row, endCell)
	m.endSelection()

	got, ok := m.selectedText()
	if !ok {
		t.Fatal("the selection does not resolve to any text")
	}
	if !strings.HasPrefix(got, "alpha") {
		t.Fatalf("the selection is %q, want it to begin with the answer's first word — "+
			"a selection resolving one line up would name the reasoning summary instead", got)
	}

	hlRow, start, end, hit := findHighlightRow(t, &m)
	if !hit {
		t.Fatal("the selection paints no cells")
	}
	if hlRow != row {
		t.Fatalf("the highlight is painted on row %d, want the body's first row %d", hlRow, row)
	}
	painted := ansi.Strip(ansi.Cut(linesAt(t, &m, hlRow), start, end))
	if !strings.Contains(painted, "alpha") {
		t.Fatalf("the highlighted cells read %q, want the selected words", painted)
	}
}

// linesAt returns one rendered transcript row, so a test can read the cells a
// highlight covers.
func linesAt(t *testing.T, m *Model, row int) string {
	t.Helper()
	lines := strings.Split(m.viewport.GetContent(), "\n")
	if row < 0 || row >= len(lines) {
		t.Fatalf("row %d is outside the %d-line transcript", row, len(lines))
	}
	return lines[row]
}

// ---- I4: the transcript path stamps a revision ----------------------

// TestTranscriptMappingCarriesAContentRevision is the regression for the review's
// Important 4.
//
// conversation_adapter fixes Block.Revision for the DOCUMENT path, but the
// transcript renders a message straight to rows and never built a Block: it
// constructed RenderedBlock with Revision left zero. Selections store Revision 0
// and MatchesRevision compares 0 == 0 — always true — so the guard the docs
// promise ("a highlight is dropped when the text moved on") was vacuous and an
// old logical offset got painted over changed text on a streaming answer.
func TestTranscriptMappingCarriesAContentRevision(t *testing.T) {
	m, span := modelWithAnswerForReview(t, "the first answer")

	if span.rendered.Revision == 0 {
		t.Fatal("the transcript's mapping carries revision 0: MatchesRevision compares " +
			"0 == 0 and the guard is vacuous")
	}
	if want := blockTextRevision(span.rendered.Logical); span.rendered.Revision != want {
		t.Fatalf("revision = %d, want the block's content hash %d",
			span.rendered.Revision, want)
	}

	// The decisive property: content change must invalidate it, driven by a real
	// content change rather than by hand-mutating the mapping.
	frozen := span.rendered.Revision
	m.state.AddMessageFinal(session.RoleAssistant, "a completely different answer", session.ContentTypeMarkdown)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	other := mappingForText(t, &m, "a completely different answer")
	if other.rendered.Revision == frozen {
		t.Fatalf("two different answers share revision %d", frozen)
	}
}

// TestAChangedBlockRefusesTheSelectionHighlight is the reader-visible half: a
// selection whose block's text changed must stop painting, rather than tinting
// whatever now sits at those offsets.
//
// The change is driven through the REAL renderer — the same block identity laid
// out from different text — rather than by mutating a stored revision, because
// the property under test is that a rendering's revision FOLLOWS ITS TEXT. A
// test that bumped the number by hand would pass on a renderer that stamped a
// constant.
func TestAChangedBlockRefusesTheSelectionHighlight(t *testing.T) {
	m, span := modelWithAnswerForReview(t, "alpha beta gamma")
	row := span.bodyRow()
	startCell, _ := span.rendered.CellAt(0, 0)
	endCell, _ := span.rendered.CellAt(0, strings.Index(span.rendered.Logical, "beta"))

	if !m.beginSelectionAt(row, startCell) {
		t.Fatal("a press on the answer began no selection")
	}
	m.extendSelectionTo(row, endCell)
	m.endSelection()
	if _, _, _, hit := findHighlightRow(t, &m); !hit {
		t.Fatal("precondition: the selection paints cells before the content changes")
	}
	frozen, ok := m.selectedText()
	if !ok || !strings.HasPrefix(frozen, "alpha") {
		t.Fatalf("precondition: selectedText = %q (ok=%v)", frozen, ok)
	}

	// The SAME block identity, laid out from different text — which is what the
	// live mapping holds after the conversation's content changed under a
	// selection. The rendering goes through the production renderer, so its
	// revision is whatever that renderer derives from the text.
	_, changed := renderMappedMessage("totally rewritten prose that is longer", 80, BlockRenderFull)
	changed.BlockID = span.id
	live, idx := findLiveSpan(t, &m, span.id)
	if live == nil {
		t.Fatal("the block has no live mapping to stand in for the change")
	}
	m.blockRenderSpans[idx].rendered = changed
	m.blockRenderSpans[idx].rows = len(changed.Rows)

	if _, _, _, hit := findHighlightRow(t, &m); hit {
		t.Fatal("the highlight was painted over text the selection never covered")
	}
	// The bytes the reader dragged over are still theirs: the frozen snapshot is
	// untouched, so `y` yields exactly what they saw.
	if after, ok := m.selectedText(); !ok || after != frozen {
		t.Errorf("selectedText = %q (ok=%v), want the frozen original %q", after, ok, frozen)
	}
}

// ---- I2: the inspector's data is pushed on runtime events -----------

// TestSubagentRegistrationReachesAnOpenInspector pins the review's Important 2.
//
// refreshInspector was called only from the inspector's own key handling, from
// New and from two messages, so an open Agents tab described the fleet as it was
// when the reader last pressed a key. A reader with no reason to press one
// watched a completed agent stay "running".
func TestSubagentRegistrationReachesAnOpenInspector(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(160, 40)
	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	m.refreshInspector()
	if len(m.inspector.model.AgentsSnapshot()) != 0 {
		t.Fatal("precondition: the roster is not empty")
	}

	// The runtime message the subagent broker emits, through Update — the same
	// path production uses.
	child := session.New(m.state.Config, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	view := m.state.RegisterSubagent("reviewer", child)
	mm, _ := m.Update(subagentMsg{view: view})
	got := asModel(t, mm)

	roster := got.inspector.model.AgentsSnapshot()
	if len(roster) == 0 {
		t.Fatal("a registered subagent never reached the open inspector's roster: " +
			"the tab shows the fleet as it was when the reader last pressed a key")
	}
	if roster[0].Label != "reviewer" {
		t.Fatalf("roster[0].Label = %q, want %q", roster[0].Label, "reviewer")
	}
}

// TestSubagentFinishReachesAnOpenInspector is the other half: a finishing agent
// must stop reading as running without a keystroke.
func TestSubagentFinishReachesAnOpenInspector(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(160, 40)
	child := session.New(m.state.Config, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	view := m.state.RegisterSubagent("reviewer", child)

	m.inspector.open(inspector.TabAgents, m.inspectorSideAvailable())
	mm, _ := m.Update(subagentMsg{view: view})
	m = asModel(t, mm)
	if len(m.inspector.model.AgentsSnapshot()) == 0 {
		t.Fatal("precondition: the roster is empty after registration")
	}

	m.state.FinishSubagent(view.ID, "all done", nil)
	done, _ := m.state.Subagent(view.ID)
	mm, _ = m.Update(subagentMsg{view: done})
	got := asModel(t, mm)

	roster := got.inspector.model.AgentsSnapshot()
	if len(roster) == 0 {
		t.Fatal("the finished agent vanished from the roster")
	}
	if roster[0].Status == inspector.AgentRunning {
		t.Fatal("the finished agent still reads as running in an open Agents tab")
	}
}

// TestLaneClickOpensAPopulatedRoster pins the lane's click-to-inspect path: it
// opened the Agents tab without refreshing, so on a session whose first
// interaction is that click the reader got the roster built at construction —
// empty — from a band reporting running agents.
func TestLaneClickOpensAPopulatedRoster(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(160, 40)
	registerRunningSubagent(t, &m, "lane-agent")
	// Deliberately NO refreshInspector: the click is the only interaction.

	if !m.openAgentLaneInspector() {
		t.Fatal("the lane click did not open the inspector")
	}
	if got := m.inspector.model.SelectedTab(); got != inspector.TabAgents {
		t.Fatalf("the lane click opened %q, want the Agents tab", got)
	}
	if len(m.inspector.model.AgentsSnapshot()) == 0 {
		t.Fatal("the lane's click-to-inspect opened an empty roster")
	}
}

// TestRailBaseRefReachesAnOpenInspector pins the changed-tree half: the rail
// reads m.railChanged in View, but the inspector renders a COPY of the snapshot,
// so a new reading had to be pushed into it.
func TestRailBaseRefReachesAnOpenInspector(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(160, 40)
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.refreshInspector()
	if got := m.inspector.model.ChangesSnapshot().Status; got != "" {
		t.Fatalf("precondition: the inspector already holds a snapshot with status %q", got)
	}

	// The message the rail's base-ref command emits. A missing dir is rejected,
	// so the test uses the model's own active root.
	mm, _ := m.Update(railBaseRefMsg{dir: m.state.Workspace().ActiveRoot, ref: "HEAD"})
	got := asModel(t, mm)

	// The fixture's temp dir is not a git repository, so the read FAILS — and a
	// failure is exactly what must reach the panel: an empty list would be
	// indistinguishable from a clean tree.
	if got.inspector.model.ChangesSnapshot().Status == "" {
		t.Fatal("the rail's base-ref reading never reached the open inspector")
	}
}

// ---- I3: /new resets the inspector and the session-scoped caches -----

// TestNewSessionClearsTheInspectorsChanges drives the real /new effect and
// asserts the Changes tab no longer shows the previous session's files.
//
// The reset cleared m.railChanged with a comment that the old session's list
// "must never render in the new session", but SetChanges renders from
// m.railSnapshot — a different field — and nothing cleared it or refreshed the
// inspector.
func TestNewSessionClearsTheInspectorsChanges(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	m.sessionSwapper = &fakeSessionSwapper{
		newState: session.New(m.state.Config, t.TempDir(), time.Unix(200, 0), session.Persistence{}),
	}
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.railSnapshot = staleSnapshotForTest()
	m.railChanged = changedfiles.RailFiles(m.railSnapshot)
	m.refreshInspector()
	if got := m.inspector.model.ChangesSnapshot().Status; got == "" {
		t.Fatal("precondition: the inspector never received the snapshot")
	}

	mm, _ := m.dispatchCommand("/new")
	got := asModel(t, mm)

	if got.railSnapshot.Status != "" || len(got.railSnapshot.Files) != 0 {
		t.Fatalf("the old session's changed-files snapshot survived /new: %+v", got.railSnapshot)
	}
	if len(got.railChanged) != 0 {
		t.Fatalf("%d old rail rows survived /new", len(got.railChanged))
	}
	if snap := got.inspector.model.ChangesSnapshot(); snap.Status != "" || len(snap.Files) != 0 {
		t.Fatalf("the inspector's Changes tab still renders the old session's files: %+v", snap)
	}
}

// TestNewSessionResetsTheViewStack drives the same effect and asserts the drill
// stack and its saved anchors are gone, so the next Esc cannot "pop back" to a
// parent from a conversation that no longer exists.
func TestNewSessionResetsTheViewStack(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.TUI.SidePanel.Enabled = true
	m.resize(200, 60)
	m.sessionSwapper = &fakeSessionSwapper{
		newState: session.New(m.state.Config, t.TempDir(), time.Unix(200, 0), session.Persistence{}),
	}
	child := session.New(m.state.Config, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	view := m.state.RegisterSubagent("explore", child)
	child.AddMessage(session.RoleUser, "child question", session.ContentTypePlain)
	m.drillIntoSubagent(view)
	if len(m.viewStackAnchors) == 0 {
		t.Fatal("precondition: no drill anchor was saved")
	}

	mm, _ := m.dispatchCommand("/new")
	got := asModel(t, mm)

	if len(got.viewStack) != 0 {
		t.Fatalf("%d drill levels survived /new", len(got.viewStack))
	}
	if len(got.viewStackAnchors) != 0 {
		t.Fatalf("%d saved drill anchors survived /new", len(got.viewStackAnchors))
	}
}

// staleSnapshotForTest is a snapshot with a status and one file, so the
// inspector's Changes tab has something to show and something to clear.
func staleSnapshotForTest() changedfiles.Snapshot {
	return changedfiles.Snapshot{
		Status:  changedfiles.StatusOK,
		BaseRef: "HEAD",
		BaseOID: "0123456789abcdef0123456789abcdef01234567",
		Files: []changedfiles.File{{
			Path:        "internal/app/tui/old_session.go",
			Kind:        changedfiles.FileModified,
			Status:      'M',
			Added:       3,
			Removed:     1,
			CountsKnown: true,
		}},
	}
}

// ---- I5: the action snapshot is memoized ----------------------------

// TestActionSnapshotIsMemoizedPerStateChange pins the review's Important 5(a).
//
// The footer resolves the snapshot on EVERY frame — a spinner tick is 80ms — and
// resolving it builds the whole conversation document, because the copy actions
// read m.copyBlock(). That is O(total conversation text) per tick, for a value
// that only changes when the state behind it does.
func TestActionSnapshotIsMemoizedPerStateChange(t *testing.T) {
	m := newTestModel(t)
	m.actionCache = &actionSnapshotCache{}

	m.actionSnapshot()
	if !m.actionCache.valid {
		t.Fatal("the first resolution did not populate the cache")
	}

	// A second resolution with nothing changed must reuse the cached value. The
	// cache is seeded with a recognisable marker, since the two answers are equal
	// either way and equality alone could not tell a hit from a re-resolution.
	key := m.actionCache.key
	m.actionCache.ctx = actionContext{Busy: true, QueueLen: 7}
	second := m.actionSnapshot()
	if !second.Busy || second.QueueLen != 7 {
		t.Fatal("the cache was not consulted: the snapshot was re-resolved with no state change")
	}
	if m.actionCache.key != key {
		t.Fatal("the cache key moved with no state change")
	}

	// A state change must invalidate it. The transcript version is in the key, so
	// a new answer is a change the key can see.
	m.state.SetWorkspace(session.Workspace{ProjectRoot: t.TempDir()})
	m.state.AddMessageFinal(session.RoleAssistant, "an answer", session.ContentTypeMarkdown)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	third := m.actionSnapshot()
	if third.QueueLen == 7 && third.Busy {
		t.Fatal("the snapshot is stale after the transcript changed")
	}
}

// modelWithAnswerForReview builds a model with one final answer and returns its
// placed mapping.
func modelWithAnswerForReview(t *testing.T, answer string) (Model, renderedBlockSpan) {
	t.Helper()
	m := newTestModel(t)
	m.state.SetWorkspace(session.Workspace{ProjectRoot: t.TempDir()})
	m.state.AddMessageFinal(session.RoleAssistant, answer, session.ContentTypeMarkdown)
	m.refreshViewport()
	return m, mappingForText(t, &m, answer)
}

// mappingForText finds the placed mapping whose logical text is want.
func mappingForText(t *testing.T, m *Model, want string) renderedBlockSpan {
	t.Helper()
	for _, s := range m.blockRenderSpans {
		if strings.TrimSpace(s.rendered.Logical) == want {
			return s
		}
	}
	t.Fatalf("no mapping for %q: %+v", want, m.blockRenderSpans)
	return renderedBlockSpan{}
}
