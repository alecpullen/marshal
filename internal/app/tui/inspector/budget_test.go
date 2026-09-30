package inspector

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/changedfiles"
)

// --- per-tab row-budget property tests ----------------------------------
//
// Every one of the three list tabs must emit no more rows than the height it
// was given, with a detail open, at every height. The bug this pins was NOT a
// single off-by-one: the blank line before the detail body was never counted in
// the hand-written budget of ANY of the three tabs, and the Agents roster's own
// blank line was subtracted only when it should not have been — so Changes and
// Agents emitted a fixed surplus (6 rows at height 1-5, 9 rows at height 1-8)
// and Context emitted one row over at height 14. clipLeftColumn hid the surplus
// in production, so the symptom was content vanishing with no sign that
// anything had been dropped.
//
// The property is asserted across heights 1-30 rather than at a couple of sizes,
// because the surplus appeared in the SMALL-height regime where a tab's fixed
// chrome (header, notes, blanks) is larger than the whole panel — and that is
// exactly the regime a "works at 80x24" test never visits.

// manyChangedFiles builds n changed paths so the Changes list is long enough to
// window at every height under test.
func manyChangedFiles(n int) []string {
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		paths = append(paths, fmt.Sprintf("internal/pkg-%02d/file-%02d.go", i, i))
	}
	return paths
}

// changesModelWithDiff builds a Changes tab with a long list and a diff open,
// which is the state the surplus was measured in.
func changesModelWithDiff(t *testing.T, width, height int) *Model {
	t.Helper()
	m := New()
	m.SetScope("s1")
	m.SetChanges(changesSnapshot(changedfiles.StatusOK, manyChangedFiles(40)...))
	m.Resize(width, height)
	if !m.EnterSelected() {
		t.Fatal("EnterSelected refused a selection that exists")
	}
	req, ok := m.PendingDiffRequest()
	if !ok {
		t.Fatal("no pending diff request")
	}
	m.ApplyDiffLoaded(DiffLoadedMsg{
		Scope: req.Scope, Request: req.Request, Path: req.Path,
		Diff: changedfiles.Diff{Path: req.Path, Patch: strings.Repeat("+added line\n", 200)},
	})
	return m
}

// agentsModelWithDetail builds an Agents tab with a long roster and a child
// detail open.
func agentsModelWithDetail(t *testing.T, width, height int) *Model {
	t.Helper()
	ids := make([]int64, 0, 40)
	for i := int64(1); i <= 40; i++ {
		ids = append(ids, i)
	}
	roster := agentsFixture(ids...)
	// Every agent gets a child so opening one shows a real body rather than the
	// "no child transcript" metadata block.
	for i := range roster {
		roster[i].HasChild = true
		roster[i].ChildBody = strings.Repeat("child line\n", 200)
	}
	m := New()
	m.Resize(width, height)
	m.SetAgents(roster)
	if !m.EnterAgent() {
		t.Fatal("EnterAgent refused a roster that exists")
	}
	return m
}

// contextModelWithDetail builds a Context tab with many sections and one open.
func contextModelWithDetail(t *testing.T, width, height int) *Model {
	t.Helper()
	m := New()
	m.Resize(width, height)
	m.SetContext(manySectionContext())
	if !m.OpenContextRow(0) {
		t.Fatal("OpenContextRow refused row 0")
	}
	return m
}

// manySectionContext is a pack with more sections than any height under test can
// show, and one long enough to give the detail a real body.
func manySectionContext() ContextData {
	d := packFixture()
	d.Pack.Sections[0].ContentTruncated = false
	d.Pack.Sections[0].Content = strings.Repeat("section line\n", 200)
	for i := 0; i < 40; i++ {
		d.Pack.Sections = append(d.Pack.Sections, ContextSection{
			Title:   fmt.Sprintf("internal/pkg-%02d/file-%02d.go", i, i),
			Kind:    "file_snippet",
			Source:  fmt.Sprintf("internal/pkg-%02d/file-%02d.go", i, i),
			Content: "package p\n",
		})
	}
	return d
}

// contextModelWithStaleDetail builds the same model and then moves the snapshot
// on underneath the open row, which is the state that draws the stale note.
func contextModelWithStaleDetail(t *testing.T, width, height int) *Model {
	t.Helper()
	m := contextModelWithDetail(t, width, height)
	updated := manySectionContext()
	updated.Pack.Sections[0].Content = strings.Repeat("a DIFFERENT section line\n", 200)
	m.SetContext(updated)
	if !m.ContextDetailStale() {
		t.Fatal("precondition failed: the open detail did not go stale")
	}
	return m
}

// TestChangesNeverExceedsItsHeight is the Changes arm of the row-budget
// property.
func TestChangesNeverExceedsItsHeight(t *testing.T) {
	for _, width := range []int{30, 40, 80} {
		for height := 1; height <= 30; height++ {
			m := changesModelWithDiff(t, width, height)
			got := renderedRows(m.viewChanges())
			if got > height {
				t.Fatalf("width %d height %d: Changes emitted %d rows:\n%s",
					width, height, got, m.viewChanges())
			}
		}
	}
}

// TestAgentsNeverExceedsItsHeight is the Agents arm of the row-budget property.
func TestAgentsNeverExceedsItsHeight(t *testing.T) {
	for _, width := range []int{30, 40, 80} {
		for height := 1; height <= 30; height++ {
			m := agentsModelWithDetail(t, width, height)
			got := renderedRows(m.viewAgents())
			if got > height {
				t.Fatalf("width %d height %d: Agents emitted %d rows:\n%s",
					width, height, got, m.viewAgents())
			}
		}
	}
}

// TestContextNeverExceedsItsHeight is the Context arm of the row-budget
// property.
func TestContextNeverExceedsItsHeight(t *testing.T) {
	for _, width := range []int{30, 40, 80} {
		for height := 1; height <= 30; height++ {
			m := contextModelWithDetail(t, width, height)
			got := renderedRows(m.viewContext())
			if got > height {
				t.Fatalf("width %d height %d: Context emitted %d rows:\n%s",
					width, height, got, m.viewContext())
			}
		}
	}
}

// TestTabsNeverExceedTheirHeightWithTrailingNotes is the same property with the
// conditional trailing notes visible, because those notes ADD rows and a budget
// that reserved them wrongly would overflow only in that state.
//
// The vanished note is the case that mattered: it is emitted below the list, so
// a budget that forgot it would overflow by two rows on exactly the refresh
// where the reader is being told their file disappeared.
func TestTabsNeverExceedTheirHeightWithTrailingNotes(t *testing.T) {
	for height := 1; height <= 30; height++ {
		// Changes: select past the end so the previous selection vanishes.
		func() {
			m := changesModelWithDiff(t, 60, height)
			m.MoveChangesSelection(39) // last row
			m.SetChanges(changesSnapshot(changedfiles.StatusOK, manyChangedFiles(2)...))
			if !m.SelectionVanished() {
				t.Fatalf("height %d: precondition failed — no vanished selection", height)
			}
			if got := renderedRows(m.viewChanges()); got > height {
				t.Errorf("height %d: Changes with a vanished note emitted %d rows:\n%s",
					height, got, m.viewChanges())
			}
		}()
		// Agents: select past the end so the previous selection vanishes.
		func() {
			m := agentsModelWithDetail(t, 60, height)
			m.MoveAgentSelection(39)
			m.SetAgents(agentsFixture(1, 2))
			if !m.AgentSelectionVanished() {
				t.Fatalf("height %d: precondition failed — no vanished agent", height)
			}
			if got := renderedRows(m.viewAgents()); got > height {
				t.Errorf("height %d: Agents with a vanished note emitted %d rows:\n%s",
					height, got, m.viewAgents())
			}
		}()
		// Context: move the snapshot on under the open row so the stale note
		// is drawn. The note is the Context tab's own trailing note, and it is
		// the one that used to be emitted LAST — after the body — where the
		// body's separator could consume the row it had been reserved.
		func() {
			m := contextModelWithStaleDetail(t, 60, height)
			if got := renderedRows(m.viewContext()); got > height {
				t.Errorf("height %d: Context with a stale note emitted %d rows:\n%s",
					height, got, m.viewContext())
			}
		}()
	}
}

// TestContextStaleNoteIsNotReservedAndThenDropped pins the other half of the
// same property: once the row has been taken from the list for the stale note,
// the note is what gets it.
//
// The note used to be emitted after the body, behind an `rb.left() >= 2` gate
// that the body's own blank separator had already consumed. The reader paid a
// row of list for a sentence they never saw — worse than not reserving at all,
// because the panel looked complete while saying nothing.
//
// The sweep starts at height 7 rather than 1 because the panel's heading is four
// rows plus a separator at this width: at height 6 exactly one row is left, and
// one row cannot hold both a list row and the note. At that size the note is not
// charged at all (the reservation requires two rows to be worth taking), so there
// is nothing to drop and nothing lost.
func TestContextStaleNoteIsNotReservedAndThenDropped(t *testing.T) {
	for height := 7; height <= 30; height++ {
		m := contextModelWithStaleDetail(t, 60, height)
		view := stripANSIForTest(m.viewContext())
		if !strings.Contains(view, "changed since you opened it") {
			t.Errorf("height %d: the stale note was reserved but never drawn:\n%s", height, view)
		}
		if got := renderedRows(m.viewContext()); got > height {
			t.Errorf("height %d: Context emitted %d rows, over budget:\n%s", height, got, view)
		}
	}
}

// --- cell-based width bounds -------------------------------------------

// TestAgentRowTextIsBoundedInCells is the Agents regression for the review's
// width finding. agentRowText used strutil.Truncate, which cuts to N RUNES and
// then appends the ellipsis, so a CJK label was emitted at roughly TWICE its
// budget: the row wrapped in the terminal, which is the overflow the bound
// exists to prevent. The test measures CELLS, not runes, because that is the
// unit the terminal draws in.
func TestAgentRowTextIsBoundedInCells(t *testing.T) {
	wide := strings.Repeat("漢字", 80)
	for _, width := range []int{20, 30, 40, 80} {
		row := agentRowText(Agent{
			ID: 1, Label: wide, Status: AgentFailed,
			Role: wide, Model: wide, Provider: wide, Error: wide,
		}, width)
		if got := ansi.StringWidth(row); got > width-2 {
			t.Errorf("width %d: agentRowText is %d cells wide: %q", width, got, row)
		}
	}
}

// TestChangeRowTextIsBoundedInCells pins that a changed file's path cannot
// produce a row wider than the panel. changeRowText truncated NOTHING before
// this: a 300-cell path overflowing a 40-cell panel wrapped and cost the panel
// its own chrome.
func TestChangeRowTextIsBoundedInCells(t *testing.T) {
	f := changedfiles.File{
		Path:        strings.Repeat("deeply/nested/path/", 40) + "file.go",
		Status:      'M',
		Kind:        changedfiles.FileRenamed,
		OldPath:     strings.Repeat("another/nested/path/", 40) + "old.go",
		Added:       1,
		Removed:     1,
		CountsKnown: true,
	}
	for _, width := range []int{20, 30, 40, 80} {
		row := changeRowText(f, width)
		if got := ansi.StringWidth(row); got > width-2 {
			t.Errorf("width %d: changeRowText is %d cells wide: %q", width, got, row)
		}
	}
	// A wide (CJK) path must be bounded too, which is what rune-counting
	// truncation gets wrong.
	f.Path = strings.Repeat("漢字/", 80)
	for _, width := range []int{20, 40, 80} {
		row := changeRowText(f, width)
		if got := ansi.StringWidth(row); got > width-2 {
			t.Errorf("width %d: CJK changeRowText is %d cells wide: %q", width, got, row)
		}
	}
}

// TestChangesRowsNeverExceedThePanelWidth is the Changes counterpart of
// TestContextRowsNeverExceedThePanelWidth: every rendered row of the tab, list
// and detail together, must fit the recorded width. It is measured in cells.
func TestChangesRowsNeverExceedThePanelWidth(t *testing.T) {
	long := strings.Repeat("a-very-long-unbroken-token/", 40) + "file.go"
	snap := changedfiles.Snapshot{
		Status:  changedfiles.StatusOK,
		BaseRef: "HEAD",
		BaseOID: "abc123",
		Files: []changedfiles.File{
			{Path: long, Status: 'M', Added: 1, Removed: 1, CountsKnown: true},
			{Path: strings.Repeat("漢字/", 80), Status: 'M', CountsKnown: false,
				Kind: changedfiles.FileUntracked},
			{Path: long, Status: 'R', Kind: changedfiles.FileRenamed, OldPath: long,
				Added: 2, Removed: 2, CountsKnown: true},
		},
	}
	for _, width := range []int{30, 31, 40, 80, 120, 200} {
		m := New()
		m.SetScope("s1")
		m.Resize(width, 24)
		m.SetChanges(snap)
		// Open a diff too, so the detail's own lines are measured rather than
		// only the list's.
		if !m.EnterSelected() {
			t.Fatal("EnterSelected refused a selection that exists")
		}
		req, _ := m.PendingDiffRequest()
		m.ApplyDiffLoaded(DiffLoadedMsg{
			Scope: req.Scope, Request: req.Request, Path: req.Path,
			Diff: changedfiles.Diff{Path: req.Path, Patch: "+++ b/x.go\n+" + long + "\n"},
		})

		out := m.viewChanges()
		for i, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("width %d: row %d is %d cells wide: %q", width, i, w, line)
			}
		}
	}
}

// TestAgentsViewRowsNeverExceedThePanelWidth is the end-to-end width property
// for the Agents tab, measured in cells: the per-row helper above bounds one
// roster row, and this bounds the tab's whole render — the header, the roster
// (which is where the wide author content lands) and the detail.
//
// The child body handed in here is short by design. This package does not wrap
// a body the caller rendered (the root renders a child transcript, the diff is
// rendered by diffview, and neither is re-flowed here); what it must bound is
// the chrome IT composes from author text — the header and the roster rows.
func TestAgentsViewRowsNeverExceedThePanelWidth(t *testing.T) {
	wide := strings.Repeat("漢字", 80)
	for _, width := range []int{30, 40, 60, 80} {
		m := New()
		m.Resize(width, 20)
		m.SetAgents([]Agent{{
			ID: 1, Label: wide, Status: AgentRunning, CurrentTool: wide,
			Role: wide, Model: wide, Provider: wide, HasChild: true,
			ChildBody: "child says hello\n",
		}})
		if !m.EnterAgent() {
			t.Fatal("EnterAgent refused the agent")
		}
		out := m.viewAgents()
		for i, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("width %d: row %d is %d cells wide: %q", width, i, w, line)
			}
		}
	}
}
