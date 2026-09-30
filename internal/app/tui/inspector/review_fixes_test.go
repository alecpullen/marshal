package inspector

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/changedfiles"
)

// --- C1: every budgeted row is bounded in cells -------------------------
//
// Five chrome rows were written through the row budget WITHOUT being clamped:
// the Changes header (which embeds an author-controlled base ref), the two
// vanished notes, the loading line, and the Agents empty-roster note. At the
// product's minimum panel width of 30 the vanished note is 77 cells, so it
// wrapped to roughly three display rows inside a slot the height budget had
// already spent on one — reflowing everything below it and pushing the panel's
// own chrome off the bottom.
//
// The fix is centralised in rowBudget.line, so these tests assert the property
// through the tabs' own renderers rather than against the helper: what matters
// is that a tab cannot emit a wide row, not that one function clamps.

// assertRowsFitWidth fails when any rendered row of out exceeds width cells.
func assertRowsFitWidth(t *testing.T, label string, out string, width int) {
	t.Helper()
	for i, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("%s: row %d is %d cells wide, over the %d-cell panel: %q",
				label, i, got, width, line)
		}
	}
}

// testWidths are the widths a panel can actually be laid out at, plus the
// narrowest one the rail is allowed to open at (tui.side_panel.min_cols).
var testWidths = []int{30, 31, 40, 80}

// TestChangesChromeRowsAreBoundedInCells pins the Changes arm of C1: the header
// carries an author-controlled base ref, and the vanished and loading notes are
// fixed sentences longer than the narrowest panel.
func TestChangesChromeRowsAreBoundedInCells(t *testing.T) {
	long := strings.Repeat("an-extremely-long-branch-or-tag-name/", 8)
	snap := changesSnapshot(changedfiles.StatusOK, manyChangedFiles(40)...)
	snap.BaseRef = long
	snap.BaseOID = "abc123"

	for _, width := range testWidths {
		// (a) the header, with the long base ref.
		m := New()
		m.Resize(width, 30)
		m.SetChanges(snap)
		assertRowsFitWidth(t, "Changes header", m.viewChanges(), width)

		// (b) the vanished note.
		m = New()
		m.Resize(width, 30)
		m.SetChanges(snap)
		m.MoveChangesSelection(39)
		m.SetChanges(changesSnapshot(changedfiles.StatusOK, manyChangedFiles(2)...))
		if !m.SelectionVanished() {
			t.Fatalf("width %d: precondition failed — no vanished selection", width)
		}
		out := m.viewChanges()
		// The note's own wording is clamped at the narrow widths under test, so
		// the assertion is on the part that always survives.
		if !strings.Contains(out, "you were on is no") {
			t.Fatalf("width %d: the vanished note is missing at a height that affords it:\n%s", width, out)
		}
		assertRowsFitWidth(t, "Changes vanished note", out, width)

		// (c) the loading line.
		m = New()
		m.Resize(width, 30)
		m.SetChanges(snap)
		if !m.EnterSelected() {
			t.Fatal("EnterSelected refused a selection that exists")
		}
		out = m.viewChanges()
		if !strings.Contains(out, "Loading diff") {
			t.Fatalf("width %d: the loading line is missing:\n%s", width, out)
		}
		assertRowsFitWidth(t, "Changes loading line", out, width)
	}
}

// TestAgentsChromeRowsAreBoundedInCells pins the Agents arm of C1: the
// empty-roster note and the vanished note.
func TestAgentsChromeRowsAreBoundedInCells(t *testing.T) {
	for _, width := range testWidths {
		// (a) the empty-roster note.
		m := New()
		m.Resize(width, 20)
		out := m.viewAgents()
		if !strings.Contains(out, "No agents have run") {
			t.Fatalf("width %d: the empty-roster note is missing:\n%s", width, out)
		}
		assertRowsFitWidth(t, "Agents empty-roster note", out, width)

		// (b) the vanished note.
		m = New()
		m.Resize(width, 20)
		m.SetAgents(agentsFixture(1, 2, 3))
		m.MoveAgentSelection(2)
		m.SetAgents(agentsFixture(1, 2))
		if !m.AgentSelectionVanished() {
			t.Fatalf("width %d: precondition failed — no vanished agent", width)
		}
		out = m.viewAgents()
		if !strings.Contains(out, "you were on is no") {
			t.Fatalf("width %d: the vanished note is missing:\n%s", width, out)
		}
		assertRowsFitWidth(t, "Agents vanished note", out, width)
	}
}

// --- C3: a measured height with an unmeasured width still bounds ---------

// TestMeasuredHeightUnmeasuredWidthStillBounds is the regression for C3.
//
// Resize records width and height independently, so Resize(0, 24) takes the
// BUDGETED path — the emission is being bounded — with no width. That used to
// pass every row through unclamped, which is "unmeasured means unbounded" in the
// one place it cannot be true: the panel is about to be joined into a frame.
// Rows are now bounded at the minimum panel width.
func TestMeasuredHeightUnmeasuredWidthStillBounds(t *testing.T) {
	const height = 24

	// Changes, with a long path and a diff open, so the list and the body are
	// both measured.
	m := New()
	m.Resize(0, height)
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK,
		strings.Repeat("deeply/nested/path/", 20)+"file.go"))
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused a selection that exists")
	}
	req, _ := m.PendingDiffRequest()
	m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: req.Scope, Request: req.Request, Path: req.Path,
		Diff: changedfiles.Diff{Path: req.Path,
			Patch: "--- a/x\n+++ b/x\n+" + strings.Repeat("wide ", 60) + "\n"},
	})
	out := m.viewChanges()
	assertRowsFitWidth(t, "Changes at width 0", out, minBudgetWidth)
	if got := renderedRows(out); got > height {
		t.Fatalf("Changes at width 0 emitted %d rows, over the %d-row budget", got, height)
	}

	// Agents, with an empty roster (the fixed note) and with a long label.
	a := New()
	a.Resize(0, height)
	out = a.viewAgents()
	assertRowsFitWidth(t, "Agents empty roster at width 0", out, minBudgetWidth)
	if got := renderedRows(out); got > height {
		t.Fatalf("Agents at width 0 emitted %d rows, over the %d-row budget", got, height)
	}

	a = New()
	a.Resize(0, height)
	a.SetAgents([]Agent{{ID: 1, Label: strings.Repeat("wide-label ", 20), Status: AgentRunning}})
	assertRowsFitWidth(t, "Agents roster at width 0", a.viewAgents(), minBudgetWidth)

	// Context, with an open row.
	c := New()
	c.Resize(0, height)
	c.SetContext(manySectionContext())
	if !c.OpenContextRow(0) {
		t.Fatal("OpenContextRow refused row 0")
	}
	out = c.viewContext()
	assertRowsFitWidth(t, "Context at width 0", out, minBudgetWidth)
	if got := renderedRows(out); got > height {
		t.Fatalf("Context at width 0 emitted %d rows, over the %d-row budget", got, height)
	}
}

// --- C4: the bound never silently disappears ----------------------------

// TestClampToWidthNeverDisappears pins C4: the helper's zero-width arm used to
// return the input unchanged, so a caller that reached it with no measurement
// got no bound at all. The "nothing has been measured, so render everything"
// rule is a CALL-SITE decision — changeRowText, agentRowText and clampLines each
// make it explicitly — and reaching this helper means the caller wants a bound.
func TestClampToWidthNeverDisappears(t *testing.T) {
	const long = "a-very-long-row-that-would-wrap-in-any-panel"
	for _, width := range []int{-5, -1, 0} {
		got := clampToWidth(long, width)
		if got == long {
			t.Errorf("clampToWidth(long, %d) passed the row through unbounded: %q", width, got)
		}
		if w := ansi.StringWidth(got); w > 1 {
			t.Errorf("clampToWidth(long, %d) is %d cells wide, want at most 1: %q", width, w, got)
		}
	}

	// The documented call-site exceptions must still be expressible, or the fix
	// would have traded one silent behaviour for another: an unmeasured row
	// renderer renders its whole row rather than one ellipsis.
	f := changedfiles.File{Path: long, Status: 'M', CountsKnown: true, Added: 1}
	if got := changeRowText(f, 0); got == "" || !strings.Contains(got, long) {
		t.Errorf("changeRowText at width 0 = %q, want the whole row", got)
	}
	if got := agentRowText(Agent{ID: 1, Label: long}, 0); !strings.Contains(got, long) {
		t.Errorf("agentRowText at width 0 = %q, want the whole row", got)
	}
	lines := []string{long}
	if got := clampLines(lines, 0); got[0] != long {
		t.Errorf("clampLines at width 0 = %q, want the line unchanged", got[0])
	}
}

// --- C2: a reserved note row is spent, not lost -------------------------

// TestChangesDoesNotStarveTheListForANoteItCannotDraw pins C2.
//
// The vanished note's two rows were deducted from the list BEFORE the panel knew
// whether the note could be drawn at all. At a mid-range height the gate then
// failed and the reservation was simply lost: the reader got no note AND a list
// one row shorter than the panel could afford.
func TestChangesDoesNotStarveTheListForANoteItCannotDraw(t *testing.T) {
	// Height 4 leaves four rows once the header pair and the blank are spent —
	// two list rows, and no room for a note. The note must not be charged, and
	// the two rows must go to the list.
	m := New()
	m.SetScope("s1")
	m.Resize(60, 4)
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, manyChangedFiles(40)...))
	m.MoveChangesSelection(39)
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go"))
	if !m.SelectionVanished() {
		t.Fatal("precondition failed — no vanished selection")
	}

	out := m.viewChanges()
	if strings.Contains(out, "no longer changed") {
		t.Fatalf("precondition failed: the note fitted at height 4, so this proves nothing:\n%s", out)
	}
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, ".go") {
			rows++
		}
	}
	if rows < 2 {
		t.Fatalf("height 4 drew %d list rows; the panel affords 2 and the note was not charged:\n%s",
			rows, out)
	}
	if got := renderedRows(out); got > 4 {
		t.Fatalf("height 4 emitted %d rows, over budget:\n%s", got, out)
	}
}

// TestChangesLoadingLineDegradesToARow pins the other half of C2: while a diff
// read is in flight the panel must say so, and it must keep saying so when only
// one row is left rather than being dropped entirely.
func TestChangesLoadingLineDegradesToARow(t *testing.T) {
	for height := 3; height <= 30; height++ {
		m := New()
		m.SetScope("s1")
		m.Resize(60, height)
		m.SetChanges(changesSnapshot(changedfiles.StatusOK, manyChangedFiles(40)...))
		if !m.EnterSelected() {
			t.Fatal("EnterSelected refused a selection that exists")
		}
		if !m.ChangesLoading() {
			t.Fatal("precondition failed: no diff is in flight")
		}
		out := m.viewChanges()
		if !strings.Contains(out, "Loading diff") {
			t.Errorf("height %d: the loading line is not on screen:\n%s", height, out)
		}
		if got := renderedRows(out); got > height {
			t.Errorf("height %d: emitted %d rows, over budget:\n%s", height, got, out)
		}
	}
}

// --- C5: a scope swap clears the Changes pane ---------------------------

// TestSetScopeResetsTheChangesPane pins C5. SetScope reset the Agents and
// Context state but left the Changes tab holding the previous conversation's
// file list, cursor, vanished flag, in-flight request and loaded diff — a diff
// labelled with a path from a session the reader has left, which on screen is
// indistinguishable from the new session's.
func TestSetScopeResetsTheChangesPane(t *testing.T) {
	m := New()
	m.SetScope("session-a")
	m.Resize(80, 20)
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "a.go", "b.go", "c.go"))
	m.MoveChangesSelection(2)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused a selection that exists")
	}
	req, ok := m.PendingDiffRequest()
	if !ok {
		t.Fatal("EnterSelected stamped no request")
	}
	m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: req.Scope, Request: req.Request, Path: req.Path,
		Diff: changedfiles.Diff{Path: req.Path, Patch: "+++ b/a.go\n+OLD SESSION MARKER\n"},
	})
	if !strings.Contains(m.changesDetail().Content(), "OLD SESSION MARKER") {
		t.Fatalf("precondition failed: the diff did not reach the body:\n%s", m.changesDetail().Content())
	}

	m.SetScope("session-b")

	if path, ok := m.SelectedPath(); ok || path != "" {
		t.Errorf("SelectedPath() = %q/%v after a scope swap, want no selection", path, ok)
	}
	if m.SelectionVanished() {
		t.Error("the vanished flag survived a scope swap")
	}
	if m.ChangesLoading() {
		t.Error("an in-flight load survived a scope swap")
	}
	if m.HasDiff() {
		t.Error("HasDiff() is true after a scope swap, so a diff from the old session is still on screen")
	}
	if got := m.changesDetail().Content(); strings.Contains(got, "OLD SESSION MARKER") {
		t.Errorf("the previous session's diff is still in the body:\n%s", got)
	}
	if got := m.changesDetail().Label(); got == "a.go" || got == "c.go" {
		t.Errorf("the body label %q still names a file from the old session", got)
	}

	// The stale-reply guard must hold on top of the reset: an id issued under
	// the previous scope can never be applied.
	if m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: req.Scope, Request: req.Request, Path: req.Path,
		Diff: changedfiles.Diff{Path: req.Path, Patch: "+++ b/a.go\n+LATE\n"},
	}) {
		t.Error("a reply from the previous scope was accepted after SetScope")
	}

	// And an unchanged scope must still be a no-op, or a refresh would wipe the
	// reader's place several times a turn.
	m.SetScope("session-b")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, "x.go"))
	m.MoveChangesSelection(0)
	m.SetScope("session-b")
	if path, _ := m.SelectedPath(); path != "x.go" {
		t.Errorf("re-stamping the SAME scope cleared the pane: selection = %q, want x.go", path)
	}
}

// --- C6: the paging keys are cursor moves on list tabs ------------------

// keyMsg builds the key the dock adapter switches on.
func keyMsg(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// TestDockAdapterPageKeysMoveTheListCursor pins C6.
//
// pgup/pgdown/home/end on the three cursor-driven tabs used to write
// TabState.Scroll, which those tabs only PARTIALLY honour: the window is
// anchored to the cursor, so a scroll no cursor movement accompanies usually
// moves nothing. The keys read as broken while quietly storing a number that
// meant something on another tab. They now move the cursor — paging by a
// viewport, home to the first row, end to the last — and the Overview keeps its
// document scroll.
func TestDockAdapterPageKeysMoveTheListCursor(t *testing.T) {
	m := New()
	m.Resize(80, 10)
	a := NewDockAdapter(m)
	paths := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		paths = append(paths, "file-"+itoa(int64(i))+".go")
	}
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, paths...))

	m.Open(TabChanges)
	start, _ := m.SelectedPath()

	a.Update(keyMsg(tea.KeyPgDown))
	down, _ := m.SelectedPath()
	if down == start {
		t.Fatalf("pgdown on Changes left the cursor on %q", down)
	}
	a.Update(keyMsg(tea.KeyPgDown))
	further, _ := m.SelectedPath()
	if further == down {
		t.Fatalf("a second pgdown on Changes left the cursor on %q", further)
	}

	a.Update(keyMsg(tea.KeyEnd))
	if got, _ := m.SelectedPath(); got != paths[len(paths)-1] {
		t.Errorf("end on Changes selected %q, want the last row %q", got, paths[len(paths)-1])
	}
	a.Update(keyMsg(tea.KeyHome))
	if got, _ := m.SelectedPath(); got != paths[0] {
		t.Errorf("home on Changes selected %q, want the first row %q", got, paths[0])
	}
	a.Update(keyMsg(tea.KeyPgUp))
	if got, _ := m.SelectedPath(); got != paths[0] {
		t.Errorf("pgup at the top selected %q, want it clamped to %q", got, paths[0])
	}

	// Agents: same keys, same meaning.
	m.Open(TabAgents)
	m.SetAgents(agentsFixture(1, 2, 3, 4, 5, 6, 7, 8))
	a.Update(keyMsg(tea.KeyEnd))
	if got := m.AgentIDSelected(); got != 8 {
		t.Errorf("end on Agents selected %d, want 8", got)
	}
	a.Update(keyMsg(tea.KeyHome))
	if got := m.AgentIDSelected(); got != 1 {
		t.Errorf("home on Agents selected %d, want 1", got)
	}
	a.Update(keyMsg(tea.KeyPgDown))
	if got := m.AgentIDSelected(); got == 1 {
		t.Errorf("pgdown on Agents left the selection at %d", got)
	}

	// Context: same keys, same meaning.
	m.Open(TabContext)
	m.SetContext(manySectionContext())
	a.Update(keyMsg(tea.KeyEnd))
	last := m.ContextCursor()
	if last != m.ContextRowCount()-1 {
		t.Errorf("end on Context moved the cursor to %d, want the last row %d",
			last, m.ContextRowCount()-1)
	}
	a.Update(keyMsg(tea.KeyHome))
	if got := m.ContextCursor(); got != 0 {
		t.Errorf("home on Context moved the cursor to %d, want 0", got)
	}

	// The Overview has no cursor, so it keeps the document scroll.
	om := overviewModel(overviewData(overviewNow), 80, 24)
	oa := NewDockAdapter(om)
	oa.Update(keyMsg(tea.KeyEnd))
	end := om.State(TabOverview).Scroll
	if end <= 0 {
		t.Fatalf("end on Overview left Scroll at %d, want the last page", end)
	}
	oa.Update(keyMsg(tea.KeyHome))
	if got := om.State(TabOverview).Scroll; got != 0 {
		t.Errorf("home on Overview left Scroll at %d, want 0", got)
	}
	oa.Update(keyMsg(tea.KeyPgDown))
	if got := om.State(TabOverview).Scroll; got <= 0 {
		t.Errorf("pgdown on Overview left Scroll at %d, want it to move", got)
	}
}

// --- C9: the Overview's body has a stable identity ----------------------

// TestActiveDetailHasAStableIdentityForTheOverview pins C9. ActiveDetail used to
// allocate a fresh empty DetailView per call for the Overview, so two calls for
// the same question — "is this the body I was just looking at?" — disagreed.
func TestActiveDetailHasAStableIdentityForTheOverview(t *testing.T) {
	m := New()
	m.Resize(80, 30)
	m.Open(TabOverview)

	first := m.ActiveDetail()
	if first == nil {
		t.Fatal("ActiveDetail returned nil for the Overview")
	}
	if second := m.ActiveDetail(); second != first {
		t.Fatal("ActiveDetail allocated a new body per call for the Overview, so its identity is unstable")
	}
	if !first.Empty() {
		t.Error("the Overview's body is not empty, so it is claiming content it does not have")
	}

	// A zero Model — which the tests construct — must behave the same way.
	zero := &Model{}
	if zero.ActiveDetail() == nil {
		t.Fatal("a zero Model's ActiveDetail returned nil")
	}
	if zero.ActiveDetail() != zero.ActiveDetail() {
		t.Fatal("a zero Model allocates a fresh Overview body per call")
	}
	// And a REAL tab's body still takes precedence over the empty one.
	m.Open(TabChanges)
	if m.ActiveDetail() == m.overviewDetail() {
		t.Fatal("ActiveDetail returned the empty Overview body while Changes was selected")
	}
}

// --- C8: the Context stale note is drawn where its row was reserved -----

// TestContextStaleNoteIsDrawnAtTheReservationPoint pins C8's ordering rule: the
// note's row is reserved before the list/body split, so the note must be emitted
// at that boundary — after the list and BEFORE the body's blank separator, which
// would otherwise spend the row first.
func TestContextStaleNoteIsDrawnAtTheReservationPoint(t *testing.T) {
	m := contextModelWithStaleDetail(t, 60, 24)
	out := stripANSIForTest(m.viewContext())
	note := strings.Index(out, "changed since you opened it")
	if note < 0 {
		t.Fatalf("the stale note is not on screen:\n%s", out)
	}
	// The body is deliberately NOT refreshed when it goes stale — the reader is
	// told, not moved — so the content on screen is still the ORIGINAL section.
	body := strings.Index(out, "section line")
	if body < 0 {
		t.Fatalf("the stale body is not on screen, so this proves nothing:\n%s", out)
	}
	if note > body {
		t.Fatalf("the stale note is drawn AFTER the body, so the body's separator can spend its reserved row:\n%s", out)
	}
}

// --- the Context tab's own trailing-note reservation --------------------

// TestContextNeverStarvesItsListForAStaleNote pins the Context half of C2: a
// stale note that cannot be charged must not cost the list a row anyway.
func TestContextNeverStarvesItsListForAStaleNote(t *testing.T) {
	// At height 6 exactly one row is left once the heading is spent, and the
	// note needs two rows to be worth charging. The row goes to the list.
	m := contextModelWithStaleDetail(t, 60, 6)
	out := m.viewContext()
	if got := renderedRows(out); got > 6 {
		t.Fatalf("height 6 emitted %d rows, over budget:\n%s", got, out)
	}
	if !strings.Contains(stripANSIForTest(out), "file_snippet") {
		t.Fatalf("height 6 drew no list row at all:\n%s", stripANSIForTest(out))
	}
}
