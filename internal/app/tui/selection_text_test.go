package tui

import (
	"strings"
	"testing"
)

// rowsOf builds selRows for one segment from plain lines.
func rowsOf(segment int, lines ...string) []selRow {
	rows := make([]selRow, len(lines))
	for i, l := range lines {
		rows[i] = selRow{text: l, segment: segment}
	}
	return rows
}

// all selects every cell of rows.
func all(rows []selRow, width int) string {
	return selectionText(rows, selPos{0, 0}, selPos{len(rows) - 1, width}, width)
}

func TestSpanOfStripsTheGutterGlyph(t *testing.T) {
	sp := spanOf(selRow{text: " ❯ fix the parser"}, 80)
	if sp.bare || sp.start != 3 || sp.text != 3 || sp.end != 17 {
		t.Fatalf("span = %+v, want a glyph row copying columns [3,17)", sp)
	}
}

func TestSpanOfBareRowLeavesIndentToTheDedent(t *testing.T) {
	sp := spanOf(selRow{text: "     the fixture setup"}, 80)
	if !sp.bare || sp.start != 0 || sp.text != 5 {
		t.Fatalf("span = %+v, want a bare row starting at 0 with text at 5", sp)
	}
}

func TestSpanOfFindsANestedRail(t *testing.T) {
	sp := spanOf(selRow{text: "       ▍     return"}, 80)
	if sp.bare || sp.start != 9 || sp.text != 13 {
		t.Fatalf("span = %+v, want the rail and its space stripped, code indent kept", sp)
	}
}

func TestSpanOfDropsTheDisclosureMarker(t *testing.T) {
	r := selRow{text: "     ≡ Read file · a.go (5 lines) ▹"}
	sp := spanOf(r, 80)
	if got := cutCols(r.text, sp.start, sp.end); got != "Read file · a.go (5 lines)" {
		t.Fatalf("copied %q, want the head without its marker", got)
	}
}

func TestSpanOfCutsRightAlignedMetaOnlyOnMetaHeads(t *testing.T) {
	text := " ✓ Reviewing the change." + strings.Repeat(" ", 45) + "reviewer #1"
	if w := len([]rune(text)); w != 80 {
		t.Fatalf("fixture width %d, want 80", w)
	}
	head := spanOf(selRow{text: text, metaHead: true}, 80)
	if got := cutCols(text, head.start, head.end); strings.TrimRight(got, " ") != "Reviewing the change." {
		t.Fatalf("meta head copied %q, want the headline alone", got)
	}
	plain := spanOf(selRow{text: text}, 80)
	if plain.end != 80 {
		t.Fatalf("a row that is not a meta head kept end %d, want 80", plain.end)
	}
}

func TestSpanOfKeepsATruncatedRowWhole(t *testing.T) {
	text := "     › go test ./internal/hooks/...  -run TestPreToolUseBlockDecision -count=20…"
	if w := len([]rune(text)); w != 80 {
		t.Fatalf("fixture width %d, want 80", w)
	}
	sp := spanOf(selRow{text: text, metaHead: true}, 80)
	if sp.end != 80 {
		t.Fatalf("end = %d, want 80: an ellipsis row has no meta to cut", sp.end)
	}
}

func TestSpanOfStepMetaRowIsEmpty(t *testing.T) {
	text := strings.Repeat(" ", 64) + "reviewer · 12s"
	if sp := spanOf(selRow{text: text, inStep: true}, 78); sp != (rowSpan{}) {
		t.Fatalf("span = %+v, want an empty span for a meta-only row", sp)
	}
}

func TestSelectionTextDedentsBareRowsAndKeepsCodeIndent(t *testing.T) {
	rows := []selRow{
		{text: " ✗ Looking at the failing test. Then:", segment: 0},
		{text: "", segment: 0},
		{text: "     • check the fixture", segment: 0},
		{text: "", segment: 0},
		{text: "       func f() {", segment: 0},
		{text: "           return", segment: 0},
		{text: "       }", segment: 0},
	}
	want := "Looking at the failing test. Then:\n\n• check the fixture\n\n  func f() {\n      return\n  }"
	if got := all(rows, 80); got != want {
		t.Fatalf("copied:\n%s\nwant:\n%s", got, want)
	}
}

func TestSelectionTextDedentsASelectionOfCodeRows(t *testing.T) {
	rows := rowsOf(0,
		" ▍ Summary:",
		" ▍   func g() {",
		" ▍           if x {",
		" ▍           }",
		" ▍   }",
	)
	got := selectionText(rows, selPos{1, 0}, selPos{4, 80}, 80)
	want := "func g() {\n        if x {\n        }\n}"
	if got != want {
		t.Fatalf("copied:\n%s\nwant:\n%s", got, want)
	}
}

func TestSelectionTextUsesFullTextOnlyForAFullyCoveredRow(t *testing.T) {
	row := selRow{
		text:     "     › go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 …",
		metaHead: true,
		full:     "go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v",
	}
	rows := []selRow{row}
	if got := all(rows, 80); got != row.full {
		t.Fatalf("fully selected row copied %q, want the full text", got)
	}
	if got := selectionText(rows, selPos{0, 7}, selPos{0, 13}, 80); got != "go test" {
		t.Fatalf("partly selected row copied %q, want the visible cells", got)
	}
}

func TestSelectionTextDropsRulesAndCollapsesGaps(t *testing.T) {
	rows := []selRow{
		{text: " · second notice line", segment: 0},
		{text: "", segment: -1},
		{text: "   " + strings.Repeat("─", 40), segment: 1},
		{text: "", segment: -1},
		{text: " ❯ next turn", segment: 2},
		{text: "", segment: -1},
		{text: "   ── 1/3 write the parser ────────", segment: 3},
	}
	want := "second notice line\n\nnext turn\n\n1/3 write the parser"
	if got := all(rows, 80); got != want {
		t.Fatalf("copied:\n%q\nwant:\n%q", got, want)
	}
}

func TestSelectionTextPartialFirstRowIsNotDedented(t *testing.T) {
	rows := rowsOf(0,
		" ❯ first line of the prompt",
		"   second line, indented below",
	)
	got := selectionText(rows, selPos{0, 9}, selPos{1, 80}, 80)
	if want := "line of the prompt\nsecond line, indented below"; got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
}

func TestSelectionTextOrdersReversedEndpoints(t *testing.T) {
	rows := rowsOf(0, " ❯ alpha", "   beta")
	if got, want := selectionText(rows, selPos{1, 80}, selPos{0, 0}, 80), "alpha\nbeta"; got != want {
		t.Fatalf("copied %q, want %q", got, want)
	}
}

// cutCols returns display columns [lo, hi) of s.
func cutCols(s string, lo, hi int) string {
	r := []rune(s)
	if hi > len(r) {
		hi = len(r)
	}
	if lo >= hi {
		return ""
	}
	return string(r[lo:hi])
}
