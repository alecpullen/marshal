// internal/app/tui/review_fixes_test.go — regression tests for the pre-merge
// review of the conversation-inspector branch.
//
// Each test here pins a defect the review found by reading or by probing, and
// each one fails against the code as it was BEFORE the fix. They are grouped in
// one file rather than scattered because they share a provenance: they are the
// evidence that a specific reported bug is closed, not a description of how the
// feature is meant to work.
package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/changedfiles"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/inspector"
)

// ---- C1: the inspector column must not defeat the frame budget -------------

// changesSnapshot builds a Changes snapshot listing n files, which the Changes
// tab renders one row per file.
//
// The paths are distinct and realistic because the tab's row text is derived
// from them, and two rows that rendered identically would make a "the cursor
// row is on screen" assertion pass for the wrong row.
func changesSnapshot(n int) changedfiles.Snapshot {
	files := make([]changedfiles.File, 0, n)
	for i := 0; i < n; i++ {
		files = append(files, changedfiles.File{
			Path:        "internal/app/tui/review_fixture_" + strconv.Itoa(i) + ".go",
			Kind:        changedfiles.FileModified,
			Status:      'M',
			Added:       i,
			Removed:     1,
			CountsKnown: true,
		})
	}
	return changedfiles.Snapshot{
		BaseRef:    "HEAD",
		BaseOID:    "0123456789abcdef0123456789abcdef01234567",
		Files:      files,
		Status:     changedfiles.StatusOK,
		CapturedAt: time.Now(),
	}
}

// TestInspectorColumnCannotHideTheFooter is the regression for the review's
// Critical 1.
//
// The frame clipped the LEFT column to the body budget and then joined the
// inspector column with lipgloss.JoinHorizontal, which pads the shorter column
// to the TALLER one. The inspector column was never clipped, and every
// non-Overview tab rendered every row, so a large working tree produced a frame
// far taller than the terminal and pushed the status line and composer off the
// bottom. The review measured 200 files in a 60-row terminal producing a
// 204-row frame.
//
// The asserted invariant is the one the reader cares about: the frame is exactly
// terminal-height, and the footer is on the last row.
func TestInspectorColumnCannotHideTheFooter(t *testing.T) {
	cases := []struct {
		name  string
		tab   inspector.Tab
		files int
		w, h  int
	}{
		{"changes at 120x40 with 40 files", inspector.TabChanges, 40, 120, 40},
		{"changes at 200x60 with 200 files", inspector.TabChanges, 200, 200, 60},
		{"changes at 160x40 with 160 files", inspector.TabChanges, 160, 160, 40},
		{"changes at 120x40 with one file", inspector.TabChanges, 1, 120, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := inspectTestModel(t, tc.w, tc.h)
			m.inspector.open(tc.tab, m.inspectorSideAvailable())
			if m.inspector.placement() != inspectorSide {
				t.Fatalf("precondition: inspector is %v, not side", m.inspector.placement())
			}
			// Installed directly on the inspector: refreshInspector would
			// re-copy the session's own (empty) changed-files snapshot.
			m.inspector.model.SetChanges(changesSnapshot(tc.files))

			lines := strings.Split(stripANSI(m.viewString()), "\n")
			if len(lines) != tc.h {
				t.Fatalf("frame rows = %d, want %d — the inspector column escaped the frame budget.\n%s",
					len(lines), tc.h, strings.Join(lines, "\n"))
			}
			if strings.TrimSpace(lines[tc.h-1]) == "" {
				t.Errorf("the status footer row is blank; the inspector pushed it off screen:\n%s",
					strings.Join(lines, "\n"))
			}
		})
	}
}

// TestInspectorColumnCannotHideTheFooterWithADetailOpen covers the same
// invariant with the heaviest state the tab can be in: a long diff open in the
// detail body underneath the list.
//
// A window that budgets the list but not the body would still overflow, and this
// is the case a reader is most likely to be in when the terminal is small.
func TestInspectorColumnCannotHideTheFooterWithADetailOpen(t *testing.T) {
	const w, h = 120, 40
	m := inspectTestModel(t, w, h)
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	// The snapshot is installed DIRECTLY on the model, with no refreshInspector
	// afterwards: refreshInspector re-copies the changed-files snapshot from the
	// session, which in a test holds no files and would empty the list.
	m.inspector.model.SetChanges(changesSnapshot(30))

	path, ok := m.inspector.model.SelectedPath()
	if !ok {
		t.Fatal("precondition: no file selected")
	}
	m.inspector.model.EnterSelected()
	req, ok := m.inspector.model.PendingDiffRequest()
	if !ok {
		t.Fatal("precondition: no diff request was stamped")
	}
	// A diff far longer than the panel, which is what the detail body has to
	// window.
	long := strings.Repeat("+ a changed line in the patch\n", 300)
	m.inspector.model.ApplyDiffLoaded(inspector.DiffLoadedMsg{
		Scope:   req.Scope,
		Request: req.Request,
		Path:    req.Path,
		Diff: changedfiles.Diff{
			Status: changedfiles.StatusOK,
			Patch:  long,
			Kind:   changedfiles.FileModified,
		},
	})
	if got, _ := m.inspector.model.SelectedPath(); got != path {
		t.Fatalf("precondition: selection moved to %q", got)
	}
	m.refreshInspector()

	lines := strings.Split(stripANSI(m.viewString()), "\n")
	if len(lines) != h {
		t.Fatalf("frame rows = %d, want %d with a diff open — the detail body escaped the budget",
			len(lines), h)
	}
}

// TestInspectorListWindowsWithTheCursor proves the fix is a WINDOW and not only
// a clip: the reader's cursor row stays on screen as they walk down a list
// longer than the panel.
//
// A clip alone would satisfy the frame invariant while making the list
// unusable — the reader would move the cursor off the bottom and see nothing
// change, which reads as a broken key.
func TestInspectorListWindowsWithTheCursor(t *testing.T) {
	m := inspectTestModel(t, 120, 24)
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	snap := changesSnapshot(60)
	m.inspector.model.SetChanges(snap)

	// The panel's OWN recorded height, not the terminal's: the frame is clamped
	// to a minimum size, so the column the inspector renders into is shorter than
	// the terminal. Asserting against the terminal would be asserting the wrong
	// thing — the invariant is that the panel fills its own column and no more.
	_, panelHeight := m.inspector.model.Size()
	if panelHeight < 5 || panelHeight >= len(snap.Files) {
		t.Fatalf("fixture is stale: the panel is %d rows for %d files, so windowing cannot be tested",
			panelHeight, len(snap.Files))
	}

	// Walk to the last row.
	for i := 1; i < len(snap.Files); i++ {
		m.inspector.model.MoveChangesSelection(1)
	}

	sel, ok := m.inspector.model.SelectedPath()
	if !ok {
		t.Fatal("no path is selected")
	}
	if sel != snap.Files[len(snap.Files)-1].Path {
		t.Fatalf("selection is %q, want the last file %q", sel, snap.Files[len(snap.Files)-1].Path)
	}

	body := m.inspector.model.View(m.inspectorData())
	rows := strings.Split(body, "\n")
	if len(rows) > panelHeight {
		t.Fatalf("the inspector emitted %d rows for a %d-row panel", len(rows), panelHeight)
	}
	// The selected file's own row must be inside the window, and it must be the
	// row carrying the cursor marker — not merely present somewhere in the list.
	cursorRow := -1
	for i, r := range rows {
		if strings.Contains(stripANSI(r), "▸") {
			cursorRow = i
		}
	}
	if cursorRow < 0 {
		t.Fatalf("no cursor row in the window, so the reader cannot see where they are:\n%s", body)
	}
	// The row identifies the selected file, and the comparison allows for the
	// row being CLAMPED to the panel width: a long, author-controlled path whose
	// row wrapped would shift every row below it and push the panel's own chrome
	// off the bottom, so the list truncates it with an ellipsis. The assertion is
	// therefore "the visible part of the cursor row is a prefix of the selection"
	// rather than "the whole path is on screen" — the latter would fail for any
	// path longer than the panel, which is the case the clamp exists for.
	cursorText := stripANSI(rows[cursorRow])
	for _, prefix := range []string{"▸", "M", " "} {
		cursorText = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cursorText), prefix))
	}
	visible := strings.TrimSuffix(strings.TrimSpace(cursorText), "…")
	if visible == "" {
		t.Fatalf("the cursor row %d names nothing:\n%s", cursorRow, body)
	}
	if !strings.HasPrefix(sel, visible) {
		t.Fatalf("the cursor row %d names %q, which is not the selected file %q:\n%s",
			cursorRow, visible, sel, body)
	}
}

// ---- C2: an open search must follow the transcript -------------------------

// TestOpenFindTracksNewOutput is the regression for the review's Critical 2.
//
// refreshFind documented itself as "what the model calls after a transcript
// rebuild", but nothing on the production path called it: its only caller was
// setFindQuery, which early-returns on an unchanged query. An open search
// therefore kept its result list forever, and output that streamed in was never
// found — the count in the status line described a transcript that no longer
// existed.
//
// This test drives only the PRODUCTION path: nothing below calls refreshFind
// directly, so it fails if the rebuild stops rebuilding the matches.
func TestOpenFindTracksNewOutput(t *testing.T) {
	m := findScrollableModel(t)
	m.openFind("tokens")
	m.refreshViewport()

	before := len(m.find.matches)
	if before == 0 {
		t.Fatal("precondition: the query matched nothing to begin with")
	}
	previous, hadPrevious := m.currentFindMatch()
	if !hadPrevious {
		t.Fatal("precondition: no current match")
	}

	// New output arrives that also contains the query.
	m.state.AddMessageFinal(session.RoleAssistant,
		"a brand new answer that also mentions tokens", session.ContentTypeMarkdown)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if got := len(m.find.matches); got <= before {
		t.Fatalf("matches stayed at %d after output containing the query arrived — "+
			"nothing on the production path rebuilds the result list", got)
	}
	// The reader's position is carried by IDENTITY, so the match they were on
	// must still be the one selected — not necessarily at the same index.
	current, ok := m.currentFindMatch()
	if !ok {
		t.Fatal("no current match after the rebuild")
	}
	if current.Block != previous.Block || current.Range != previous.Range {
		t.Errorf("the rebuild moved the reader from %v/%v to %v/%v; the match survived the rebuild "+
			"and should have been carried by identity",
			previous.Block, previous.Range, current.Block, current.Range)
	}
}

// TestOpenSearchStatusFollowsTheRebuild pins the other half: the rebuilt list is
// what the STATUS reports, so the count the reader sees describes the transcript
// they are looking at.
func TestOpenSearchStatusFollowsTheRebuild(t *testing.T) {
	m := findScrollableModel(t)
	m.openFind("filler")
	m.refreshViewport()

	before := m.findStatus()
	if before == "" {
		t.Fatal("precondition: no status was reported for an open search")
	}
	m.state.AddMessageFinal(session.RoleAssistant,
		"more filler words to match in a fresh answer", session.ContentTypeMarkdown)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	if after := m.findStatus(); after == before {
		t.Fatalf("the find status is unchanged (%q) after matching output arrived", after)
	}
}

// ---- C3: the selection highlight follows the live layout -------------------

// findHighlightRow returns the first transcript row the selection covers cells
// on, and the cells it covers there.
func findHighlightRow(t *testing.T, m *Model) (row, start, end int, ok bool) {
	t.Helper()
	for r := 0; r < m.viewport.TotalLineCount(); r++ {
		s, e, hit := m.selectionHighlight(r)
		if hit {
			return r, s, e, true
		}
	}
	return 0, 0, 0, false
}

// TestSelectionHighlightFollowsAReflow is the regression for the review's
// Critical 3.
//
// selectionHighlight took its ROWS from the frozen snapshot — which
// selectedBlock prefers once a drag completes — but its row PLACEMENT from the
// live mapping. After any reflow the two disagreed, and the review measured the
// tint landing forty cells past the selected text, over adjacent text and
// chrome.
//
// The fix paints from the live geometry. This test asserts the property a reader
// can see: after a resize, the highlighted cells still lie on the text.
func TestSelectionHighlightFollowsAReflow(t *testing.T) {
	const answer = "alpha beta gamma delta epsilon zeta eta theta"
	m, blockRow := modelWithAnswer(t, answer)
	id := anchorBlock(m, answer)
	sr, sc, ok := cellAtOffset(t, &m, id, 0)
	if !ok {
		t.Fatal("precondition: no cell for offset 0")
	}
	er, ec, ok := cellAtOffset(t, &m, id, 11)
	if !ok {
		t.Fatal("precondition: no cell for offset 11")
	}
	_ = blockRow
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()

	if _, _, _, hit := findHighlightRow(t, &m); !hit {
		t.Fatal("precondition: the selection paints no cells at the original width")
	}

	// Reflow: a much narrower width rewraps the block.
	m.viewport.SetWidth(40)
	m.lastTranscriptHash = 0
	m.refreshViewport()

	row, start, end, hit := findHighlightRow(t, &m)
	if !hit {
		t.Fatal("the highlight vanished after a reflow")
	}
	if start >= end {
		t.Fatalf("highlight range (%d,%d) is empty or reversed", start, end)
	}
	// The decisive assertion. The highlight must lie within the row it is drawn
	// on AND cover text that is really there: the live layout is what says where
	// "alpha beta " ends, so a frozen-geometry answer shows up as a range that
	// runs past the content.
	lines := strings.Split(stripANSI(m.viewport.View()), "\n")
	if row < 0 || row >= len(lines) {
		t.Fatalf("highlight row %d is outside the %d rendered rows", row, len(lines))
	}
	width := ansi.StringWidth(lines[row])
	if end > width {
		t.Fatalf("the highlight ends at cell %d on a %d-cell row — it runs past the end of the row",
			end, width)
	}
	// And the highlighted span must be the selected WORDS, measured in the live
	// geometry: offset 0..11 of the answer is "alpha beta ".
	live, _ := findLiveSpan(t, &m, id)
	if live == nil {
		t.Fatal("the block has no live mapping after the reflow")
	}
	liveRow := row - live.blockRow
	if liveRow < 0 || liveRow >= len(live.rendered.Rows) {
		t.Fatalf("the highlight is on row %d, but the live block starts at %d and has %d rows",
			row, live.blockRow, len(live.rendered.Rows))
	}
	liveFrom, liveTo, ok := live.rendered.HighlightRange(liveRow, 0, 11)
	if !ok {
		t.Fatalf("the live mapping reports no cells for the selection on row %d", liveRow)
	}
	if start != liveFrom || end != liveTo {
		t.Fatalf("highlight = (%d,%d), but the LIVE geometry puts the selection at (%d,%d) — "+
			"the highlight is being painted from stale geometry", start, end, liveFrom, liveTo)
	}
}

// findLiveSpan returns the current mapping for a block.
func findLiveSpan(t *testing.T, m *Model, id conversation.BlockID) (*renderedBlockSpan, int) {
	t.Helper()
	for i := range m.blockRenderSpans {
		if m.blockRenderSpans[i].id == id {
			return &m.blockRenderSpans[i], i
		}
	}
	return nil, -1
}

// TestSelectionHighlightRefusesWhenTheTextChanged pins the honesty rule the fix
// added: a selection whose block was genuinely REPLACED must not be tinted over
// whatever now sits at those offsets. The bytes stay copyable from the frozen
// snapshot, but the cells are no longer a claim the renderer can make.
func TestSelectionHighlightRefusesWhenTheTextChanged(t *testing.T) {
	const answer = "alpha beta gamma"
	m, _ := modelWithAnswer(t, answer)
	id := anchorBlock(m, answer)
	sr, sc, _ := cellAtOffset(t, &m, id, 0)
	er, ec, _ := cellAtOffset(t, &m, id, 5)
	m.beginSelectionAt(sr, sc)
	m.extendSelectionTo(er, ec)
	m.endSelection()

	if _, _, _, hit := findHighlightRow(t, &m); !hit {
		t.Fatal("precondition: no highlight before the content change")
	}
	frozen, ok := m.selectedText()
	if !ok || !strings.Contains(frozen, "alpha") {
		t.Fatalf("precondition: selectedText = %q (ok=%v)", frozen, ok)
	}

	// Change the block's content underneath the selection WITHOUT moving the
	// selection's identity — exactly the case a revision check exists for. The
	// live mapping is what the renderer reads, so mutating it here is the same
	// thing streaming output does.
	span, _ := findLiveSpan(t, &m, id)
	if span == nil {
		t.Fatal("no live mapping for the selected block")
	}
	span.rendered.Revision++

	if _, _, _, hit := findHighlightRow(t, &m); hit {
		t.Error("the highlight was painted over text the selection never covered")
	}
	// The copy must still yield the ORIGINAL bytes from the frozen snapshot.
	if after, ok := m.selectedText(); !ok || after != frozen {
		t.Errorf("selectedText = %q (ok=%v), want the frozen original %q", after, ok, frozen)
	}
}

// ---- C4: the tab arithmetic (conversation package) -------------------------

// TestTabArithmeticRoundTripsAtAnyStartColumn is the regression for the review's
// Critical 4.
//
// cellsForLogicalLen and logicalLenForCells both walked from column 0, so a tab
// was measured as a full tab stop no matter which column it started at, while
// the layout recorded the true width in Span.Cells. The two disagreed for any
// tab that did not start at column 0 — which is every Markdown table's second
// column, since the cell separator IS a tab.
//
// The invariant is the round trip: mapping an offset to a cell and back must
// return the offset it started from, at every start column.
func TestTabArithmeticRoundTripsAtAnyStartColumn(t *testing.T) {
	sources := []string{
		"a\tb",
		"abc\tdef",
		"a\tb\tc",
		"ab\tcd\tef\tgh",
	}
	for _, src := range sources {
		for _, width := range []int{20, 40, 80} {
			block := conversation.LayoutBlock(
				conversation.Block{ID: "msg:1", Text: src},
				conversation.LayoutOptions{Width: width, Breakpoints: WrapBreakpoints},
			)
			for row := range block.Rows {
				for off := 0; off <= len(src); off++ {
					cell, ok := block.CellAt(row, off)
					if !ok {
						continue
					}
					back := block.OffsetAt(row, cell)
					if back != off {
						t.Errorf("src %q width %d: row %d offset %d -> cell %d -> offset %d "+
							"(round trip must be the identity)", src, width, row, off, cell, back)
					}
				}
			}
		}
	}
}

// TestTabHighlightCoversTheSecondTableColumn is the user-visible consequence of
// the same defect: a find match in a table's second column must be tinted over
// its own cells, not three cells past the text.
func TestTabHighlightCoversTheSecondTableColumn(t *testing.T) {
	src := "| alpha | beta |\n| one | two |\n"
	block := conversation.LayoutBlock(
		conversation.Block{ID: "msg:1", Text: src},
		conversation.LayoutOptions{Width: 80},
	)
	// Locate "two" in the LOGICAL text and highlight it on the row that draws
	// it.
	at := strings.Index(src, "two")
	if at < 0 {
		t.Fatal("fixture is stale: 'two' is not in the source")
	}
	row, ok := block.RowForOffset(at)
	if !ok {
		t.Fatalf("no row displays offset %d (%+v)", at, block.Rows)
	}
	from, to, ok := block.HighlightRange(row, at, at+3)
	if !ok {
		t.Fatal("the highlight range was not resolvable on its own row")
	}
	if from >= to {
		t.Fatalf("highlight range (%d,%d) is zero-width — the match in the second table "+
			"column is tinted off the text", from, to)
	}
	// And it must lie within the row's own cells.
	if got := block.Rows[row].Cells; to > got {
		t.Fatalf("highlight ends at cell %d on a %d-cell row — past the end of the row", to, got)
	}
}

// ---- the palette / key contract ------------------------------------------

// resolvedActions returns the whole catalog resolved against the model's own
// state, which is what the palette lists.
func resolvedActions(m *Model) []Action {
	return resolveActions(m.actionSnapshot())
}

// findAction returns the resolved catalog entry for an id.
func findAction(t *testing.T, m *Model, id ActionID) Action {
	t.Helper()
	for _, a := range resolvedActions(m) {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no palette row for action %q", id)
	return Action{}
}

// TestCtrlBAndThePaletteAgreeNowToggleTheInspector is the regression for the
// review's finding that the row labelled "Side rail" claimed Ctrl+B while
// Ctrl+B toggled the INSPECTOR, and the rail itself had no key at all.
//
// That violates the catalog's contract: the footer, the palette and key
// dispatch must resolve one key to one meaning.
func TestCtrlBAndThePaletteAgreeNowToggleTheInspector(t *testing.T) {
	m := inspectTestModel(t, 160, 40)

	if got := findAction(t, &m, ActionToggleInspector).KeyHint; got != "Ctrl+B" {
		t.Fatalf("the inspector action is bound to %q, want Ctrl+B", got)
	}
	if got := findAction(t, &m, ActionSideRail).KeyHint; got != "" {
		t.Fatalf("the side rail still claims the key %q; Ctrl+B belongs to the inspector", got)
	}

	// Pressing Ctrl+B must do what the Ctrl+B row says it does.
	if m.inspector.isOpen() {
		t.Fatal("precondition: the inspector is already open")
	}
	got := pressCtrlLetter(t, &m, 'b', false)
	if !got.inspector.isOpen() {
		t.Error("Ctrl+B did not open the inspector, which is what its palette row promises")
	}
	if got.railHidden {
		t.Error("Ctrl+B toggled the side rail — the exact drift this test exists to prevent")
	}
}

// pressCtrlLetter sends a real ctrl+<letter> key event, which is what a
// terminal delivers. keyPress() cannot build one: it falls through to
// KeyPressMsg{Code: rune(key[0])} with no Mod, so "ctrl+b" arrived at the model
// as a bare "b" and every ctrl assertion silently tested the wrong key.
func pressCtrlLetter(t *testing.T, m *Model, letter rune, shift bool) Model {
	t.Helper()
	mod := tea.ModCtrl
	if shift {
		mod |= tea.ModShift
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: letter, Mod: mod})
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return got
}

// pressEsc sends a real escape key event.
func pressEsc(t *testing.T, m *Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return got
}

// runActionFor drives an action through the model's dispatcher and returns the
// result as a value, which is what the palette does when it runs a row.
func runActionFor(t *testing.T, m *Model, id ActionID) Model {
	t.Helper()
	updated, _ := m.runAction(id)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("runAction returned %T, want Model", updated)
	}
	return got
}

// TestSideRailWithoutAKeyIsStillReachableFromThePalette pins that relabelling
// the rail row did not make the rail unreachable: it is palette-only now, and
// running it from the palette must still hide/show the rail.
func TestSideRailWithoutAKeyIsStillReachableFromThePalette(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	if !m.railEnabled() {
		t.Fatal("precondition: the rail is not enabled")
	}
	before := m.railHidden
	got := runActionFor(t, &m, ActionSideRail)
	if got.railHidden == before {
		t.Error("the Side rail palette action did not toggle the rail")
	}
}

// TestPaletteKeysAreClaimedByExactlyOneAction sweeps the catalog and asserts the
// invariant behind the two defects above: a key is never advertised by two
// different actions, and Ctrl+B belongs to the inspector.
func TestPaletteKeysAreClaimedByExactlyOneAction(t *testing.T) {
	m := inspectTestModel(t, 160, 40)

	// Ctrl+X is the ONE deliberate exception, and it is not a drift: the two
	// rows are mutually exclusive, and ctrlXID resolves between them from the
	// same context snapshot the footer renders from, so at most one is ever
	// available at a time. That is why Ctrl+B — where both rows WERE
	// simultaneously live and meant different surfaces — is a defect and this
	// is not.
	sharedByDesign := map[string]bool{"Ctrl+X": true}

	seen := map[string]ActionID{}
	for _, a := range resolvedActions(&m) {
		if a.KeyHint == "" {
			continue
		}
		if other, dup := seen[a.KeyHint]; dup && other != a.ID && !sharedByDesign[a.KeyHint] {
			t.Errorf("key %q is advertised by both %q and %q", a.KeyHint, other, a.ID)
		}
		seen[a.KeyHint] = a.ID
	}
	if seen["Ctrl+B"] != ActionToggleInspector {
		t.Errorf("Ctrl+B resolves to %q, want the inspector toggle", seen["Ctrl+B"])
	}
	if seen["Ctrl+Shift+B"] != ActionExpandInspector {
		t.Errorf("Ctrl+Shift+B resolves to %q, want the expand action", seen["Ctrl+Shift+B"])
	}
}

// TestCtrlShiftBChangesOnlyThePlacement pins the difference between the two new
// keys, so Ctrl+Shift+B cannot be wired to the same call as Ctrl+B and pass the
// expand test by accident.
func TestCtrlShiftBChangesOnlyThePlacement(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	before := m.inspector.placement()

	after := pressCtrlLetter(t, &m, 'b', true)
	if after.inspector.placement() == before {
		t.Fatal("Ctrl+Shift+B left the placement unchanged")
	}
	if after.inspector.isOpen() != m.inspector.isOpen() {
		t.Error("Ctrl+Shift+B changed whether the inspector is open; it should only change where it renders")
	}
}

// ---- the expand path is reachable from a key ------------------------------

// TestCtrlShiftBExpandsAndEscRestoresTheInspector pins the Esc level the docs
// advertise. expandBody existed and nothing called it, so "expanded body" was
// unreachable and the first Esc level could not occur.
func TestCtrlShiftBExpandsAndEscRestoresTheInspector(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	if m.inspector.placement() != inspectorSide {
		t.Fatalf("precondition: placement is %v", m.inspector.placement())
	}

	expanded := pressCtrlLetter(t, &m, 'b', true)
	if !expanded.inspector.replacesBodyOnly() {
		t.Fatalf("Ctrl+Shift+B did not expand the inspector (placement = %v)",
			expanded.inspector.placement())
	}

	// Esc returns it to the shared placement rather than closing it: the
	// inspector is still wanted, just not full-body.
	back := pressEsc(t, &expanded)
	if back.inspector.replacesBodyOnly() {
		t.Error("Esc did not leave the body-expanded placement")
	}
	if !back.inspector.isOpen() {
		t.Error("Esc closed the inspector instead of returning it to its shared placement")
	}
}

// TestExpandActionIsUnavailableWhenAlreadyExpanded pins the availability rule
// alongside the binding, so the palette does not offer an action that would do
// nothing.
func TestExpandActionIsUnavailableWhenAlreadyExpanded(t *testing.T) {
	m := inspectTestModel(t, 160, 40)
	m.inspector.open(inspector.TabChanges, m.inspectorSideAvailable())
	m.inspector.expandBody()

	if got := findAction(t, &m, ActionExpandInspector); !got.Disabled {
		t.Errorf("the expand action is offered while the inspector is already expanded (reason %q)",
			got.DisabledReason)
	}
}

// ---- the revision both the selection and the search index key on ----------

// TestBlockRevisionChangesWithContent pins the field both fixed consumers rely
// on. Block.Revision was declared and never populated, so SearchIndex compared
// 0 == 0 and served a stale projection for a block whose text had grown, and
// Selection.MatchesRevision could never detect a change.
func TestBlockRevisionChangesWithContent(t *testing.T) {
	m := findTestModel(t)
	m.state.AddMessageFinal(session.RoleAssistant, "first answer", session.ContentTypeMarkdown)
	m.refreshViewport()

	first := map[string]int{}
	for _, b := range m.conversationDocument().Blocks() {
		first[string(b.ID)] = b.Revision
	}
	if len(first) == 0 {
		t.Fatal("precondition: the document has no blocks")
	}
	for id, rev := range first {
		if rev == 0 {
			t.Errorf("block %q has revision 0 — a revision check that compares it to another 0 "+
				"can never detect a change", id)
		}
	}

	// A second, DIFFERENT answer must get a different revision, or a cache keyed
	// on the revision would serve one block's rendering for another's.
	m.state.AddMessageFinal(session.RoleAssistant, "second and quite different answer", session.ContentTypeMarkdown)
	m.refreshViewport()

	seenChange := false
	for _, b := range m.conversationDocument().Blocks() {
		was, existed := first[string(b.ID)]
		if existed && was != b.Revision {
			t.Errorf("block %q changed revision (%d -> %d) without its content changing",
				b.ID, was, b.Revision)
		}
		if existed && was != b.Revision {
			continue
		}
		if !existed {
			seenChange = true
		}
	}
	if !seenChange {
		t.Error("the new answer's block did not appear in the document")
	}
}

// TestSearchIndexReprojectsAChangedBlock pins the consumer of that field: a block
// whose text grew must be re-projected rather than served from the index. With
// Revision unpopulated this could not happen, so a streaming answer was searched
// through its old text.
func TestSearchIndexReprojectsAChangedBlock(t *testing.T) {
	idx := conversation.NewSearchIndex(0)
	q := conversation.NewFindQuery("beta")

	before := conversation.NewDocument([]conversation.Block{
		{ID: "msg:1", Kind: conversation.BlockMessage, Members: []string{"m1"}, Text: "alpha", Revision: 1},
	})
	if hits := conversation.FindInDocument(before, q, idx); len(hits) != 0 {
		t.Fatalf("precondition: %d hits for a query the block does not contain", len(hits))
	}

	after := conversation.NewDocument([]conversation.Block{
		{ID: "msg:1", Kind: conversation.BlockMessage, Members: []string{"m1"}, Text: "alpha beta", Revision: 2},
	})
	if hits := conversation.FindInDocument(after, q, idx); len(hits) == 0 {
		t.Error("the block's new text was not searchable — the index served its stale projection " +
			"for the same block identity and revision")
	}
}

// TestSearchIndexServesAnUnchangedBlock pins the other side of the contract, so
// the fix cannot be "ignore the cache and always re-project".
func TestSearchIndexServesAnUnchangedBlock(t *testing.T) {
	idx := conversation.NewSearchIndex(0)
	q := conversation.NewFindQuery("beta")
	block := conversation.Block{
		ID: "msg:1", Kind: conversation.BlockMessage, Members: []string{"m1"},
		Text: "alpha beta", Revision: 7,
	}
	doc := conversation.NewDocument([]conversation.Block{block})

	first := conversation.FindInDocument(doc, q, idx)
	if len(first) != 1 {
		t.Fatalf("first lookup found %d hits, want 1", len(first))
	}
	// Same identity, same revision: the answer must be identical.
	second := conversation.FindInDocument(conversation.NewDocument([]conversation.Block{block}), q, idx)
	if len(second) != 1 {
		t.Fatalf("second lookup found %d hits, want 1", len(second))
	}
	if second[0] != first[0] {
		t.Errorf("an unchanged block produced a different match (%+v vs %+v)", first[0], second[0])
	}
}

// ---- the two render paths are one ----------------------------------------

// TestRenderConversationBlockMatchesTheMappedPath is the direct assertion that
// the two layout paths are now one: the same text at the same width must render
// to the same bytes whether it is asked for as a Block or as an answer body.
//
// This is what makes the divergence impossible to reintroduce silently —
// including the three-column indent shift the review measured.
func TestRenderConversationBlockMatchesTheMappedPath(t *testing.T) {
	const width = 80
	m := findTestModel(t)
	contents := []string{
		"A paragraph.",
		"A paragraph.\n\n- a bullet\n- another bullet\n\n> a quote\n",
		"| alpha | beta |\n| one | two |\n",
		strings.Repeat("word ", 40),
	}
	for _, content := range contents {
		mapped, _ := renderMappedMessage(content, width, BlockRenderFull)
		viaBlock := m.renderConversationBlock(
			conversation.Block{ID: "msg:1", Text: content}, width, BlockRenderFull)
		if got, want := strings.TrimSuffix(mapped, "\n"), viaBlock; got != want {
			t.Errorf("the two render paths disagree for %q at width %d:\n mapped: %q\n  block: %q",
				content, width, got, want)
		}
	}
}

// TestTranscriptIndentContractCoversRenderConversationBlock extends the
// indent-contract case list to the renderer that was previously uncovered.
//
// That renderer used to lay out with NO indent while the live path used the
// transcript gutter — two layout paths disagreeing about where column 3 is, one
// of them with no callers. It now delegates to the one shared layout, so it
// satisfies the same contract as every other transcript renderer.
func TestTranscriptIndentContractCoversRenderConversationBlock(t *testing.T) {
	const width = 80
	m := findTestModel(t)
	cases := map[string]conversation.Block{
		"plain paragraph":  {ID: "msg:1", Text: "a short paragraph of prose"},
		"wrapped prose":    {ID: "msg:2", Text: strings.Repeat("word ", 40)},
		"heading":          {ID: "msg:3", Text: "## A heading"},
		"bullet list":      {ID: "msg:4", Text: "- one\n- two"},
		"multi-line quote": {ID: "msg:5", Text: "> line one\n> line two"},
		"table":            {ID: "msg:6", Text: "| alpha | beta |\n| one | two |\n"},
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			got := m.renderConversationBlock(block, width, BlockRenderFull)
			if strings.TrimSpace(stripANSI(got)) == "" {
				t.Fatal("rendered nothing")
			}
			assertGutterContract(t, got)
		})
	}
}
