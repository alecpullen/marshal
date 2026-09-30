package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
)

// copyChipRegionFor returns the copy click region the renderer produced, if
// any. A copy region is one whose target carries a copy source.
func copyChipRegionFor(m Model) (clickRegion, bool) {
	for _, r := range m.clickRegions {
		if r.target.copySource != nil {
			return r, true
		}
	}
	return clickRegion{}, false
}

// blockRegionFor returns the ordinary (toggle) region for an item key.
func blockRegionFor(m Model, key itemKey) (clickRegion, bool) {
	for _, r := range m.clickRegions {
		if r.target.copySource == nil && r.target.key == key && !r.target.isActiveTool {
			return r, true
		}
	}
	return clickRegion{}, false
}

// messageItem returns the transcript item for the nth message.
func copyChipMessageItem(t *testing.T, m Model, n int) session.TranscriptItem {
	t.Helper()
	seen := 0
	for _, item := range m.state.Transcript() {
		if item.Kind != session.KindMessage {
			continue
		}
		if seen == n {
			return item
		}
		seen++
	}
	t.Fatalf("no message item at index %d", n)
	return session.TranscriptItem{}
}

// TestCopyChipRendersOnItsOwnIndentedLine pins the renderer half: the chip is
// its own content line, carries the copy glyph, and starts inside the gutter
// rather than in column 0 (column 0 is what makes the transcript staircase).
func TestCopyChipRendersOnItsOwnIndentedLine(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessageFinal(session.RoleAssistant, "the answer body", session.ContentTypeMarkdown)
	item := copyChipMessageItem(t, m, 0)

	rendered := renderTranscriptItem(item, false, "", regionView{}, nil, 80)
	plain := stripANSI(rendered)

	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("rendered %d lines, want at least 2 (body + chip):\n%s", len(lines), plain)
	}
	chip := lines[1]
	if !strings.Contains(chip, glyph.Copy) {
		t.Fatalf("second line %q does not carry the copy glyph %q:\n%s", chip, glyph.Copy, plain)
	}
	if !strings.Contains(strings.ToLower(chip), "copy") {
		t.Fatalf("chip line %q does not say what it does:\n%s", chip, plain)
	}
	if strings.HasPrefix(chip, glyph.Copy) {
		t.Fatalf("chip starts in column 0, breaking the gutter contract: %q", chip)
	}
	// The body must survive unchanged and above the chip.
	if !strings.Contains(lines[0], "the answer body") {
		t.Fatalf("first line %q does not hold the answer body:\n%s", lines[0], plain)
	}
}

// TestCopyChipRegionExistsOnlyForCopyableAnswers pins the model half: a chip
// region appears exactly where a copy is possible, so a user never clicks an
// affordance that cannot do anything.
func TestCopyChipRegionExistsOnlyForCopyableAnswers(t *testing.T) {
	t.Run("assistant final answer", func(t *testing.T) {
		m := newTestModel(t)
		m.state.AddMessageFinal(session.RoleAssistant, "copy me", session.ContentTypeMarkdown)
		m.refreshViewport()

		if _, ok := copyChipRegionFor(m); !ok {
			t.Fatal("no copy chip region for an assistant final answer")
		}
	})

	t.Run("user message", func(t *testing.T) {
		m := newTestModel(t)
		m.state.AddMessage(session.RoleUser, "what I typed", session.ContentTypePlain)
		m.refreshViewport()

		if r, ok := copyChipRegionFor(m); ok {
			t.Fatalf("a user message offered a copy chip at %+v; copying the user's own words back is noise", r)
		}
	})

	t.Run("empty assistant answer", func(t *testing.T) {
		m := newTestModel(t)
		m.state.AddMessageFinal(session.RoleAssistant, "", session.ContentTypeMarkdown)
		m.refreshViewport()

		if r, ok := copyChipRegionFor(m); ok {
			t.Fatalf("an empty answer offered a copy chip at %+v", r)
		}
	})
}

// TestCopyChipRegionIsNarrowerThanItsBlock is the test that proves the chip is
// a chip and not a block-wide button: its line range must sit strictly inside
// the block's own range, so clicking the answer body still means "expand".
func TestCopyChipRegionIsNarrowerThanItsBlock(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessageFinal(session.RoleAssistant, "a body\n\nwith several\n\nlines of prose", session.ContentTypeMarkdown)
	m.refreshViewport()

	chip, ok := copyChipRegionFor(m)
	if !ok {
		t.Fatal("no copy chip region")
	}
	key := itemKeyFor(func() *session.TranscriptItem {
		item := copyChipMessageItem(t, m, 0)
		return &item
	}())
	block, ok := blockRegionFor(m, key)
	if !ok {
		t.Fatal("no block region for the answer")
	}

	// The chip is its own line, and the body region ENDS where the chip's
	// begins: the two ranges are disjoint and jointly cover the block, which
	// is what makes one line copy while the rest still expands.
	if chip.endLine-chip.startLine != 1 {
		t.Fatalf("chip spans %d lines, want exactly 1", chip.endLine-chip.startLine)
	}
	if chip.startLine != block.endLine {
		t.Fatalf("chip starts at %d, want it adjacent to the body region that ends at %d", chip.startLine, block.endLine)
	}
	if chip.startLine <= block.startLine {
		t.Fatalf("chip starts at %d, at or before the body start %d: it is covering the body", chip.startLine, block.startLine)
	}
	rendered := renderTranscriptItem(copyChipMessageItem(t, m, 0), false, "", regionView{}, nil, 80)
	if covered, total := (block.endLine-block.startLine)+(chip.endLine-chip.startLine), strings.Count(rendered, "\n"); covered != total {
		t.Fatalf("regions cover %d content lines, want all %d of the rendered block", covered, total)
	}
}

// TestClickOnCopyChipCopiesWithoutExpanding pins the routing rule: a click on
// the chip copies and leaves the block's expansion alone. Reusing the block
// toggle here is how one click would both copy and silently change what is on
// screen.
func TestClickOnCopyChipCopiesWithoutExpanding(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m.state.AddMessageFinal(session.RoleAssistant, "copy this body", session.ContentTypeMarkdown)
	m.refreshViewport()

	chip, ok := copyChipRegionFor(m)
	if !ok {
		t.Fatal("no copy chip region")
	}
	item := copyChipMessageItem(t, m, 0)
	key := itemKeyFor(&item)
	before := m.isExpanded(key)

	mm, cmd := m.Update(tea.MouseClickMsg{
		X:      1,
		Y:      m.scrollHintRows() + chip.startLine - m.viewport.YOffset(),
		Button: tea.MouseLeft,
	})
	got := asModel(t, mm)
	// Update RETURNS the copy command; it does not run it. Driving it is the
	// difference between testing the routing and testing nothing.
	if cmd == nil {
		t.Fatal("the chip click produced no command")
	}
	applied, _ := got.Update(cmd())
	got = asModel(t, applied)

	if len(w.written) != 1 {
		t.Fatalf("writer called %d times, want 1", len(w.written))
	}
	if w.written[0] != "copy this body" {
		t.Fatalf("wrote %q, want the answer body", w.written[0])
	}
	if got.isExpanded(key) != before {
		t.Fatalf("expansion changed from %v to %v on a copy click", before, got.isExpanded(key))
	}
}

// TestClickOnBlockBodyStillExpands pins the other half: the chip took nothing
// A click on a block's BODY begins a selection; a click on its HEADER toggles it.
//
// This test used to assert the opposite — that a body click toggled expansion —
// and that contract is the reason dragging never worked: the press was consumed
// by the toggle before a drag could exist, so a reader trying to select a phrase
// in a collapsed block opened the block instead.
//
// The replacement keeps BOTH behaviours, each on the target it belongs to: the
// header is the disclosure control and still opens the block, and the body is
// text and becomes selectable. Asserting only one of them would let the other
// regress silently.
func TestClickOnBlockBodyBeginsASelectionWhileTheHeaderStillToggles(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m.state.AddMessageFinal(session.RoleAssistant, "a body\n\nwith a second paragraph", session.ContentTypeMarkdown)
	m.refreshViewport()

	item := copyChipMessageItem(t, m, 0)
	key := itemKeyFor(&item)
	block, ok := blockRegionFor(m, key)
	if !ok {
		t.Fatal("no block region for the answer")
	}

	// A line in the block that is NOT the header and NOT the chip.
	bodyLine := block.startLine + 1
	if chip, ok := copyChipRegionFor(m); ok && chip.startLine == bodyLine {
		bodyLine++
	}

	clickAt := func(line int) Model {
		t.Helper()
		mm, cmd := m.Update(tea.MouseClickMsg{
			X:      m.frameRect().Transcript.X + 1,
			Y:      m.scrollHintRows() + line - m.viewport.YOffset(),
			Button: tea.MouseLeft,
		})
		got := asModel(t, mm)
		if cmd != nil {
			applied, _ := got.Update(cmd())
			got = asModel(t, applied)
		}
		return got
	}

	before := m.isExpanded(key)

	// The body: a selection begins, nothing is copied, and the block does NOT
	// toggle.
	body := clickAt(bodyLine)
	if len(w.written) != 0 {
		t.Fatalf("clicking the block body copied %d times; the body is not a copy button", len(w.written))
	}
	if body.isExpanded(key) != before {
		t.Fatalf("clicking the body toggled expansion to %v; it must only begin a selection",
			!before)
	}
	if !body.selectionActive() {
		t.Fatal("clicking the block body began no selection")
	}

	// The header: still the disclosure control. A selection left over from the
	// body click must not stop it.
	header := clickAt(block.startLine)
	if header.isExpanded(key) == before {
		t.Fatalf("expansion stayed %v; clicking the block HEADER must still toggle it", before)
	}
	if len(w.written) != 0 {
		t.Fatalf("clicking the header copied %d times", len(w.written))
	}
}
