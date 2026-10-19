# P0 text selection — implementation plan

Date: 2026-10-19. Status: executed.
Base: `907c67ff1ca05e1dbce21b185628a52d388cf7bb` (`main`).
Approved design: [p0-text-selection.md](../p0-text-selection.md), parent [design.md](../design.md) §5.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**Goal:** drag across the transcript to select text while it scrolls and streams, and get cleaned text on the clipboard, without giving up wheel scrolling.

**Non-goals:** soft-wrap rejoining, double/triple-click, unwrapped code blocks, selection outside the transcript viewport, node-and-offset anchors that survive re-layout, keyboard selection (spec F1–F6). No session, agent, database or protocol change. `tui.mouse_capture` and Ctrl+S keep their meaning.

**Assumptions:**
- Bubble Tea v2 (`charm.land/bubbletea/v2` v2.0.8): cell-motion mouse mode reports drag motion while a button is held, and `tea.Mouse` carries `X`, `Y`, `Button` and `Mod`.
- `lipgloss.StyleRanges` (lipgloss v2.0.5) restyles a cell range of an ANSI string without changing its width.
- `TestStatusLineUntrustedIsWarningColored` fails on this machine at the base commit, unrelated to this work (confirmed on `main` while authoring). Package-wide verify commands skip it by name.

**How this plan was checked:** every task below was replayed in order on a fresh worktree at the base commit. After each task its Verify commands passed, and `gofmt -l` was clean. Every new behavioural test was mutation-checked: removing the behaviour it guards makes it fail. The code blocks are the replayed code, not sketches.

Execute tasks in order. Run each task's Verify and commit it before starting the next, as `p0-text-selection: task N — <title>`. Tasks 1 and 2 are parallel-safe with each other. Task 6 is parallel-safe with Tasks 1–5. All other tasks are sequential. Never commit unrelated working-tree changes. The spec and plan travel with the work (Task 7).

**Encoding:** new files are given in full. Edits to existing files are given as unified diffs against the state after the previous tasks. Apply them exactly, by hand or with `git apply`. Every hunk's context was checked against the base.

## Contracts

Declarations that more than one task uses. Each is created by the task named, verbatim as shown in that task's file.

**Task 1, `selection_text.go`: the copier (pure functions over plain rows).**

```go
const maxGutterCols = 12

// selPos is a position in transcript content: a content line and a display column.
type selPos struct{ line, col int }
func (p selPos) before(q selPos) bool

// selRow is one content line as the copier sees it.
type selRow struct {
	text     string // the line with ANSI escapes stripped
	segment  int    // dedent group: a sub-region, else a block, else -1 between blocks
	metaHead bool   // may end in right-aligned meta
	inStep   bool   // a row of a step block
	full     string // the untruncated text of a collapsed tool row, or ""
}

// rowSpan is the copyable part of a row in display columns.
type rowSpan struct {
	start int  // where copied text begins
	text  int  // first text column, where the highlight begins
	end   int  // one past the last copyable column
	bare  bool // no gutter glyph
}

func isGutterGlyph(s string) bool
func spanOf(r selRow, width int) rowSpan
func selectionText(rows []selRow, from, to selPos, width int) string
```

**Task 2, `transcript.go` / `toolgroup.go`: untruncated heads.**

```go
func toolRowHead(event registry.AuditEvent, workdir string) string
func toolGroupHead(events []registry.AuditEvent) string
```

**Task 3, `selection.go`: selection state, anchoring, revalidation, painting.**

```go
type blockKey struct {
	node viewmodel.NodeID
	name string
}
type blockSpan struct {
	key        blockKey
	start, end int
}
type selAnchor struct {
	block blockKey
	row   int
	col   int
}
type selection struct {
	anchor, head selAnchor
	dragging     bool
	copied       string
}
type dragState struct {
	pointer tea.Mouse
	gen     int
	ticking bool
}
var selectionStyle = lipgloss.NewStyle().Reverse(true)

func (m *Model) selectionEnabled() bool
func (m *Model) dragging() bool
func (m *Model) clearSelection()
func (m *Model) spanIndexAt(line int) int
func (m *Model) anchorAt(p selPos) (selAnchor, bool)
func (m *Model) resolve(a selAnchor) (selPos, bool)
func (m *Model) selRange() (from, to selPos, ok bool)
func (m *Model) selRowAt(l int) selRow
func (m *Model) fullToolHead(id viewmodel.NodeID, text string) string
func (m *Model) selectedText(from, to selPos) string
func (m *Model) revalidateSelection()
func (m *Model) paintSelection(view string) string
```

New `Model` fields (Task 3, after `nodeRegions`):

```go
	blockSpans   []blockSpan
	contentLines []string
	sel          *selection
	press        *tea.Mouse
	drag         dragState
```

**Task 4, `selection_input.go`: mouse input.**

```go
type selScrollTickMsg struct{ gen int }
const selScrollInterval = 50 * time.Millisecond

func (m *Model) handleTranscriptMouse(msg tea.Msg) (tea.Cmd, bool)
func (m *Model) handleSelScrollTick(msg selScrollTickMsg) tea.Cmd
// and, changed signatures in click.go:
func (m *Model) handleTranscriptClick(msg tea.Mouse) (tea.Cmd, bool)
func (m *Model) handleNowBarClick(msg tea.Mouse) (tea.Cmd, bool)
```

**Shared test helpers.**

```go
// Task 3, selection_test.go
func selectionFixture(t *testing.T) (Model, *fakeClipboard)
// selectionFixtureCopy is the pinned copy of the whole fixture; its full
// text is in Task 3's selection_test.go.
func lineOf(t *testing.T, m *Model, sub string) int
func selectRows(t *testing.T, m *Model, from, to selPos)
func thinkKeyOf(t *testing.T, m *Model) viewmodel.NodeID

// Task 4, click_test.go and selection_input_test.go
func clickAt(t *testing.T, m tea.Model, x, y int) tea.Model
func screenY(m *Model, l int) int
func dragTo(t *testing.T, m Model, x0, y0, x1, y1 int) Model
```

Existing helpers these rely on: `newTestModel`, `newChildState`, `stepFixture`, `readEvent` and `asModel`; `fakeClipboard` in `copy_clipboard_test.go`; `flattenCmd` and `pressKeys` in `browse_test.go`; `stripANSI` in `view.go`.

## Task 1: Copier for selected text

**Goal:** a pure function turns selected transcript rows into the cleaned text of spec B6.

**Files:** `internal/app/tui/selection_text.go` (new), `internal/app/tui/selection_text_test.go` (new).

Parallel-safe with: Task 2.

**Steps:**

1. Create `internal/app/tui/selection_text.go` with exactly this content:

```go
package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// maxGutterCols bounds how far into a row the gutter scan looks. The deepest
// gutter the transcript draws is a tool body's rail at column 7.
const maxGutterCols = 12

// selPos is a position in transcript content: a content line and a display
// column.
type selPos struct{ line, col int }

// before reports whether p comes before q in reading order.
func (p selPos) before(q selPos) bool {
	return p.line < q.line || (p.line == q.line && p.col < q.col)
}

// selRow is one content line as the copier sees it.
type selRow struct {
	// text is the line with ANSI escapes stripped.
	text string
	// segment groups rows whose leading whitespace is dedented together: one
	// per sub-region, one per block outside its sub-regions, -1 for the blank
	// line between blocks.
	segment int
	// metaHead marks a row that may end in right-aligned meta: the first
	// glyph row of a step block, and the first row of every sub-region.
	metaHead bool
	// inStep marks a row of a step block, where a deeply indented row that
	// ends at the frame edge is the step's meta on a row of its own.
	inStep bool
	// full is the untruncated text of a collapsed tool row, or "".
	full string
}

// rowSpan is the copyable part of a row in display columns.
type rowSpan struct {
	// start is where copied text begins: after the gutter glyph and its
	// space, or 0 on a bare row, whose indentation the dedent removes.
	start int
	// text is the first column of text, where the highlight begins.
	text int
	// end is one past the last copyable column.
	end int
	// bare reports a row with no gutter glyph.
	bare bool
}

// isGutterGlyph reports whether s is a glyph the transcript draws in a
// gutter, including the spinner's frames.
func isGutterGlyph(s string) bool {
	switch s {
	case glyph.Rail, glyph.User, glyph.Running, glyph.Ambient, glyph.OK,
		glyph.Error, glyph.Warning, glyph.Question, glyph.Thinking, glyph.Edit,
		glyph.File, glyph.Shell, glyph.Search, glyph.Agent, glyph.Web,
		glyph.Brand, glyph.CustomAgent, glyph.Job, glyph.Watch:
		return true
	}
	return slices.Contains(NewSpinner().frames, s)
}

// spanOf finds the copyable span of one row rendered at width.
func spanOf(r selRow, width int) rowSpan {
	plain := strings.TrimRight(r.text, " ")
	if plain == "" {
		return rowSpan{}
	}
	runes := []rune(plain)
	i, col := 0, 0
	for i < len(runes) && runes[i] == ' ' {
		i++
		col++
	}
	sp := rowSpan{bare: true}
	if i < len(runes) && col < maxGutterCols && isGutterGlyph(string(runes[i])) &&
		(i+1 == len(runes) || runes[i+1] == ' ') {
		col += ansi.StringWidth(string(runes[i]))
		i++
		if i < len(runes) {
			i++ // the space after the glyph
			col++
		}
		sp.bare = false
		sp.start = col
	}
	sp.text = col
	for _, rr := range runes[i:] {
		if rr != ' ' {
			break
		}
		sp.text++
	}
	full := ansi.StringWidth(plain)
	sp.end = full
	switch {
	case strings.HasSuffix(plain, " "+glyph.DisclosureCollapsed),
		strings.HasSuffix(plain, " "+glyph.DisclosureExpanded):
		sp.end = full - 2
	case full == width && r.inStep && sp.bare && sp.text >= maxGutterCols:
		// The step's meta on a row of its own: decoration only.
		return rowSpan{}
	case full == width && r.metaHead && !strings.HasSuffix(plain, "…"):
		// Right-aligned meta: the segment after the last gap of two or
		// more spaces, when that gap sits inside the text.
		if gap := strings.LastIndex(plain, "  "); gap >= 0 {
			if gapCol := ansi.StringWidth(plain[:gap]); gapCol > sp.text {
				sp.end = gapCol
			}
		}
	}
	if sp.end < sp.text {
		sp.end = sp.text
	}
	return sp
}

// copyPiece is one row's contribution to copied text.
type copyPiece struct {
	text    string
	segment int
	bare    bool
	// dedent marks a piece that starts at its row's copy start, so its
	// leading whitespace is indentation the dedent may remove.
	dedent bool
	// drop marks a row that is decoration only (a rule or a meta row): it
	// contributes no line at all.
	drop bool
	// gap marks a row with nothing on it: layout spacing, not content.
	gap bool
}

// selectionText returns the cleaned text the selection from..to covers,
// inclusive of the cell at to. rows are the transcript's content lines and
// width is the width they were rendered at.
func selectionText(rows []selRow, from, to selPos, width int) string {
	if to.before(from) {
		from, to = to, from
	}
	var pieces []copyPiece
	for l := max(from.line, 0); l <= to.line && l < len(rows); l++ {
		r := rows[l]
		sp := spanOf(r, width)
		lo, hi := sp.start, sp.end
		if l == from.line {
			lo = max(lo, from.col)
		}
		if l == to.line {
			hi = min(hi, to.col+1)
		}
		p := copyPiece{segment: r.segment, bare: sp.bare, gap: strings.TrimSpace(r.text) == ""}
		if sp.end == 0 && !p.gap {
			p.drop = true // a meta row: nothing in it is copyable
		}
		if lo < hi {
			stripped := strings.TrimRight(r.text, " ")
			p.text = strings.TrimRight(ansi.Cut(stripped, lo, hi), " ")
			p.dedent = lo == sp.start
			if r.full != "" && lo <= sp.text && hi >= sp.end {
				p.text, p.dedent = r.full, false
			}
			if t, ok := trimRules(p.text); ok {
				p.text, p.drop = t, t == ""
			}
		}
		pieces = append(pieces, p)
	}
	dedentPieces(pieces)
	var out []string
	for _, p := range pieces {
		if p.drop {
			continue
		}
		// Layout spacing adds nothing after a line that is already blank.
		if p.gap && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, p.text)
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// trimRules drops box-drawing rules at either end of a row's text, so a
// task divider copies as its label and a turn separator as nothing. ok is
// false, and s is returned unchanged, for a row with no rule.
func trimRules(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "─") && !strings.HasSuffix(t, "─") {
		return s, false
	}
	return strings.TrimSpace(strings.Trim(t, "─")), true
}

// dedentPieces removes, per segment and per kind (bare or glyph rows), the
// leading whitespace every dedentable piece shares. A bare row's
// indentation is mostly hanging indent; a glyph row's is content, so the two
// kinds are measured separately.
func dedentPieces(pieces []copyPiece) {
	type key struct {
		segment int
		bare    bool
	}
	common := map[key]int{}
	for _, p := range pieces {
		if !p.dedent || strings.TrimSpace(p.text) == "" {
			continue
		}
		k := key{p.segment, p.bare}
		n := len(p.text) - len(strings.TrimLeft(p.text, " "))
		if cur, ok := common[k]; !ok || n < cur {
			common[k] = n
		}
	}
	for i, p := range pieces {
		if !p.dedent {
			continue
		}
		if n, ok := common[key{p.segment, p.bare}]; ok && len(p.text) >= n {
			pieces[i].text = p.text[n:]
		}
	}
}
```

2. Create `internal/app/tui/selection_text_test.go` with exactly this content:

```go
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
```

The tests, one behaviour each:

| Test | Asserts |
|---|---|
| `TestSpanOfStripsTheGutterGlyph` | ` ❯ text` copies from column 3 |
| `TestSpanOfBareRowLeavesIndentToTheDedent` | a glyph-less row starts at 0, text at its indent |
| `TestSpanOfFindsANestedRail` | a rail at column 7 is stripped, code indent after it kept |
| `TestSpanOfDropsTheDisclosureMarker` | trailing ` ▹` is not copied |
| `TestSpanOfCutsRightAlignedMetaOnlyOnMetaHeads` | owner meta at the frame edge is cut on a meta head only |
| `TestSpanOfKeepsATruncatedRowWhole` | an `…` row is never meta-cut |
| `TestSpanOfStepMetaRowIsEmpty` | a step's meta-only row copies nothing |
| `TestSelectionTextDedentsBareRowsAndKeepsCodeIndent` | narration list and code keep relative indentation |
| `TestSelectionTextDedentsASelectionOfCodeRows` | selecting only code rows removes their shared margin |
| `TestSelectionTextUsesFullTextOnlyForAFullyCoveredRow` | full text when fully covered, visible cells when partial |
| `TestSelectionTextDropsRulesAndCollapsesGaps` | separators vanish, task dividers keep their label, blank runs collapse |
| `TestSelectionTextPartialFirstRowIsNotDedented` | a mid-row start copies from that column |
| `TestSelectionTextOrdersReversedEndpoints` | dragging upwards copies the same text |

**Verify:**

```bash
gofmt -l internal/app/tui/                      # no output
go test ./internal/app/tui/ -run 'TestSpanOf|TestSelectionText' -count=1   # ok
```

## Task 2: Untruncated tool heads

**Goal:** a collapsed tool row's and a tool group's head before truncation are available as functions. The renderers use them, and later the copier will too.

**Files:** `internal/app/tui/transcript.go`, `internal/app/tui/toolgroup.go`, `internal/app/tui/tool_head_test.go` (new).

Parallel-safe with: Task 1.

**Steps:**

1. In `transcript.go`, move the head-building block of `renderCompletedToolCallIn` into a new function `toolRowHead`, placed directly above `renderCompletedToolCallIn`'s doc comment. The block runs from `head := DisplayToolName(event.ToolName)` through the closing brace of the `else if s := viewmodel.DisplaySummary(event)` branch. Its locals `shellRow` and `summaryDupesCommand` are used only inside it. In its place, `renderCompletedToolCallIn` calls `head := toolRowHead(event, workdir)`. The disclosure-marker branch stays in `renderCompletedToolCallIn`. Rendering is unchanged.

Apply this change to `internal/app/tui/transcript.go`:

```diff
diff --git a/internal/app/tui/transcript.go b/internal/app/tui/transcript.go
index 1ef44b2e..d8a1210f 100644
--- a/internal/app/tui/transcript.go
+++ b/internal/app/tui/transcript.go
@@ -974,22 +974,11 @@ func renderCompletedToolCall(event registry.AuditEvent, expanded bool, callers [
 	return renderCompletedToolCallIn(event, expanded, callers, width, "")
 }
 
-// renderCompletedToolCallIn is renderCompletedToolCall with the session's
-// active root, which elides a leading cd into it from shell rows.
-func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers []string, width int, workdir string) string {
-	g := toolCategoryGlyph(event.ToolName)
-	style := statusOkStyle()
-	if event.Error != "" {
-		g = glyph.Error
-		style = errorStyle()
-	}
-	// Glyph and label share the state colour in both directions; success used
-	// to pair a muted glyph with a green label while errors made both red.
-	gutterColor := theme.Current().FGMuted
-	if event.Error != "" {
-		gutterColor = theme.Current().StatusError
-	}
-	gutter := gutterPrefix(g, gutterColor)
+// toolRowHead is a completed call's head without its disclosure marker: the
+// text a collapsed row shows before it is truncated to the row. Copying a
+// fully selected truncated row uses it, so the clipboard gets the whole
+// command or path rather than the row's ellipsis.
+func toolRowHead(event registry.AuditEvent, workdir string) string {
 	head := DisplayToolName(event.ToolName)
 	shellRow := viewmodel.IsShellFamily(event.ToolName)
 	// summaryDupesCommand tracks whether the head already carries the
@@ -1038,6 +1027,26 @@ func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers
 	} else if s := viewmodel.DisplaySummary(event); s != "" && !summaryDupesCommand {
 		head += dimSeparator + s
 	}
+	return head
+}
+
+// renderCompletedToolCallIn is renderCompletedToolCall with the session's
+// active root, which elides a leading cd into it from shell rows.
+func renderCompletedToolCallIn(event registry.AuditEvent, expanded bool, callers []string, width int, workdir string) string {
+	g := toolCategoryGlyph(event.ToolName)
+	style := statusOkStyle()
+	if event.Error != "" {
+		g = glyph.Error
+		style = errorStyle()
+	}
+	// Glyph and label share the state colour in both directions; success used
+	// to pair a muted glyph with a green label while errors made both red.
+	gutterColor := theme.Current().FGMuted
+	if event.Error != "" {
+		gutterColor = theme.Current().StatusError
+	}
+	gutter := gutterPrefix(g, gutterColor)
+	head := toolRowHead(event, workdir)
 	if event.ResultContent != "" || event.Error != "" {
 		if expanded {
 			head += " " + glyph.DisclosureExpanded
```

2. In `toolgroup.go`, extract the collapsed branch's head (plural name, count, targets) into `toolGroupHead`. The collapsed branch truncates `toolGroupHead(events)`; the expanded branch keeps its own targetless `head`.

Apply this change to `internal/app/tui/toolgroup.go`:

```diff
diff --git a/internal/app/tui/toolgroup.go b/internal/app/tui/toolgroup.go
index 01cacd57..adfbccca 100644
--- a/internal/app/tui/toolgroup.go
+++ b/internal/app/tui/toolgroup.go
@@ -21,6 +21,22 @@ const bulletIndent = 4
 // path it read, the command it ran, the query it searched for.
 func toolTarget(event registry.AuditEvent) string { return viewmodel.ToolTarget(event) }
 
+// toolGroupHead is a collapsed group's head before truncation: the plural
+// tool name, the count and every target.
+func toolGroupHead(events []registry.AuditEvent) string {
+	head := fmt.Sprintf("%s: ×%d", pluralizeToolName(events[0].ToolName), len(events))
+	targets := make([]string, 0, len(events))
+	for _, ev := range events {
+		if t := toolTarget(ev); t != "" {
+			targets = append(targets, t)
+		}
+	}
+	if len(targets) > 0 {
+		head += dimSeparator + strings.Join(targets, ", ")
+	}
+	return head
+}
+
 // renderToolGroup renders a run of same-tool audit events. Collapsed it is
 // one line, the plural tool name, the count and as many targets as fit: a run
 // that grows while a step is live then only ever changes one row, instead of
@@ -32,17 +48,8 @@ func renderToolGroup(events []registry.AuditEvent, expanded bool, width int) str
 	var b strings.Builder
 
 	if !expanded {
-		targets := make([]string, 0, len(events))
-		for _, ev := range events {
-			if t := toolTarget(ev); t != "" {
-				targets = append(targets, t)
-			}
-		}
-		if len(targets) > 0 {
-			head += dimSeparator + strings.Join(targets, ", ")
-		}
 		b.WriteString(gutter)
-		b.WriteString(statusOkStyle().Render(ansi.Truncate(head, max(width-gutterWidth, 1), "…")))
+		b.WriteString(statusOkStyle().Render(ansi.Truncate(toolGroupHead(events), max(width-gutterWidth, 1), "…")))
 		b.WriteString("\n")
 		return b.String()
 	}
```

3. Create `internal/app/tui/tool_head_test.go` with exactly this content:

```go
package tui

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestToolRowHeadIsTheUntruncatedCollapsedRow(t *testing.T) {
	ev := registry.AuditEvent{ToolName: "shell.run", Args: []byte(`{"command":"go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v"}`), ResultSummary: "exit 0"}
	head := toolRowHead(ev, "")
	if wide := stripANSI(renderCompletedToolCallIn(ev, false, nil, 400, "")); !strings.Contains(wide, head) {
		t.Fatalf("a wide row %q does not show the head %q", wide, head)
	}
	narrow := strings.TrimRight(stripANSI(renderCompletedToolCallIn(ev, false, nil, 40, "")), " \n")
	if !strings.HasSuffix(narrow, "…") || strings.Contains(narrow, head) {
		t.Fatalf("a narrow row %q should be the head truncated", narrow)
	}
}

func TestToolGroupHeadIsTheUntruncatedCollapsedRow(t *testing.T) {
	evs := []registry.AuditEvent{
		{ToolName: "search.text", Args: []byte(`{"query":"TestPreToolUseBlockDecision"}`)},
		{ToolName: "search.text", Args: []byte(`{"query":"testHookTimeoutMS"}`)},
	}
	head := toolGroupHead(evs)
	if wide := stripANSI(renderToolGroup(evs, false, 400)); !strings.Contains(wide, head) {
		t.Fatalf("a wide group row %q does not show the head %q", wide, head)
	}
	narrow := strings.TrimRight(stripANSI(renderToolGroup(evs, false, 30)), " \n")
	if !strings.HasSuffix(narrow, "…") || strings.Contains(narrow, head) {
		t.Fatalf("a narrow group row %q should be the head truncated", narrow)
	}
}
```

| Test | Asserts |
|---|---|
| `TestToolRowHeadIsTheUntruncatedCollapsedRow` | a wide row shows `toolRowHead`; a narrow one is it truncated with `…` |
| `TestToolGroupHeadIsTheUntruncatedCollapsedRow` | the same for a merged group row |

**Verify:**

```bash
gofmt -l internal/app/tui/                      # no output
go vet ./internal/app/tui/
go test ./internal/app/tui/ -run 'ToolRowHead|ToolGroupHead|Tool|Step|Transcript|Group' -count=1   # ok
```

## Task 3: Selection state, anchoring and painting

**Goal:** the model can hold a content-anchored selection. It follows its text across refreshes, clears when that text changes, and is painted in reverse video on the visible rows. There is no mouse input yet; tests create selections directly.

**Files:** `internal/app/tui/selection.go` (new), `internal/app/tui/model.go`, `internal/app/tui/viewport_render.go`, `internal/app/tui/view.go`, `internal/app/tui/selection_test.go` (new), `internal/app/tui/frame_invariants_test.go`.

Depends on: Tasks 1 and 2.

**Steps:**

1. Create `internal/app/tui/selection.go` with exactly this content:

```go
package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/viewmodel"
)

// blockKey names a transcript block across refreshes: the node it renders,
// or a name for a block that renders no node (the banner, a turn
// separator, the notice, the queued messages, the reconnect line).
type blockKey struct {
	node viewmodel.NodeID
	name string
}

// blockSpan is one block's half-open range of content lines.
type blockSpan struct {
	key        blockKey
	start, end int
}

// selAnchor is a selection endpoint pinned to content rather than to the
// screen: a block, a row inside it and a display column. Rows added or
// removed in other blocks leave it on the same text.
type selAnchor struct {
	block blockKey
	row   int
	col   int
}

// selection is the transcript text selection.
type selection struct {
	anchor, head selAnchor
	// dragging is true from the first drag motion to the release.
	dragging bool
	// copied is the text put on the clipboard at release. A refresh that
	// changes the covered text clears the selection.
	copied string
}

// dragState is the pointer state of a drag in progress.
type dragState struct {
	// pointer is the last pointer position, read by the auto-scroll tick.
	pointer tea.Mouse
	// gen identifies the drag; a tick from an earlier drag is ignored.
	gen int
	// ticking is true while an auto-scroll tick is armed.
	ticking bool
}

// selectionStyle paints selected text. Reverse video needs no palette slot,
// reads at every colour tier and under NO_COLOR, and stays distinct from the
// browse cursor's BGSelection band.
var selectionStyle = lipgloss.NewStyle().Reverse(true)

// selectionEnabled reports whether the app owns the mouse. When the
// terminal does (Ctrl+S, tui.mouse_capture = false) there is no app
// selection.
func (m *Model) selectionEnabled() bool {
	return !m.mouseReleased && m.state != nil && m.state.Config.TUI.MouseCapture
}

// dragging reports whether a selection drag is in progress.
func (m *Model) dragging() bool { return m.sel != nil && m.sel.dragging }

// clearSelection drops the selection and any pending press.
func (m *Model) clearSelection() {
	m.sel = nil
	m.press = nil
	m.drag.ticking = false
}

// spanIndexAt returns the index of the last block starting at or before
// line, or -1.
func (m *Model) spanIndexAt(line int) int {
	i := sort.Search(len(m.blockSpans), func(i int) bool { return m.blockSpans[i].start > line })
	return i - 1
}

// anchorAt pins a content position to the block that holds it. A blank line
// between two blocks pins to the block before it, one row past its end.
func (m *Model) anchorAt(p selPos) (selAnchor, bool) {
	i := m.spanIndexAt(p.line)
	if i < 0 {
		return selAnchor{}, false
	}
	sp := m.blockSpans[i]
	return selAnchor{block: sp.key, row: p.line - sp.start, col: p.col}, true
}

// resolve turns an anchor back into a content position, clamped to its
// block and the blank line after it. ok is false when the block is gone.
func (m *Model) resolve(a selAnchor) (selPos, bool) {
	for _, sp := range m.blockSpans {
		if sp.key == a.block {
			return selPos{line: sp.start + min(a.row, sp.end-sp.start), col: a.col}, true
		}
	}
	return selPos{}, false
}

// selRange returns the selection's endpoints in reading order.
func (m *Model) selRange() (from, to selPos, ok bool) {
	if m.sel == nil {
		return selPos{}, selPos{}, false
	}
	a, ok1 := m.resolve(m.sel.anchor)
	h, ok2 := m.resolve(m.sel.head)
	if !ok1 || !ok2 {
		return selPos{}, selPos{}, false
	}
	if h.before(a) {
		return h, a, true
	}
	return a, h, true
}

// selRowAt describes content line l for the copier.
func (m *Model) selRowAt(l int) selRow {
	if l < 0 || l >= len(m.contentLines) {
		return selRow{segment: -1}
	}
	r := selRow{text: ansi.Strip(m.contentLines[l]), segment: -1}
	bi := m.spanIndexAt(l)
	if bi < 0 || l >= m.blockSpans[bi].end {
		return r // the blank line between blocks
	}
	sp := m.blockSpans[bi]
	r.segment = bi
	r.inStep = sp.key.node.Kind == viewmodel.KindStep
	// The narrowest region holding the line that is not the block's own
	// region is a sub-region (a tool row, a card, a thinking row).
	sub := -1
	for j, nr := range m.nodeRegions {
		if l < nr.startLine || l >= nr.endLine || nr.target.node == sp.key.node {
			continue
		}
		if sub < 0 || nr.endLine-nr.startLine <= m.nodeRegions[sub].endLine-m.nodeRegions[sub].startLine {
			sub = j
		}
	}
	if sub >= 0 {
		nr := m.nodeRegions[sub]
		r.segment = len(m.blockSpans) + sub
		if l == nr.startLine {
			r.metaHead = true
			r.full = m.fullToolHead(nr.target.node, r.text)
		}
		return r
	}
	if r.inStep {
		// The first glyph row of a step is its headline.
		for k := sp.start; k < l; k++ {
			if !spanOf(selRow{text: ansi.Strip(m.contentLines[k])}, m.viewport.Width()).bare {
				return r
			}
		}
		r.metaHead = !spanOf(r, m.viewport.Width()).bare
	}
	return r
}

// fullToolHead is the untruncated text of a collapsed tool row whose text
// ends in the truncation ellipsis, or "".
func (m *Model) fullToolHead(id viewmodel.NodeID, text string) string {
	if id.Kind != viewmodel.KindTool || !strings.HasSuffix(strings.TrimRight(text, " "), "…") {
		return ""
	}
	n := m.browseTree[id]
	if n == nil {
		return ""
	}
	switch {
	case len(n.Tools) == 1:
		state, _ := m.transcriptSource()
		return oneLine(toolRowHead(n.Tools[0], state.Workspace().ActiveRoot))
	case len(n.Tools) > 1:
		return toolGroupHead(n.Tools)
	}
	return ""
}

// selectedText is the cleaned text between two content positions.
func (m *Model) selectedText(from, to selPos) string {
	rows := make([]selRow, 0, to.line-from.line+1)
	for l := from.line; l <= to.line; l++ {
		rows = append(rows, m.selRowAt(l))
	}
	return selectionText(rows, selPos{0, from.col}, selPos{to.line - from.line, to.col}, m.viewport.Width())
}

// revalidateSelection runs after every content rebuild. During a drag the
// anchors simply re-resolve; after release a selection whose block is gone,
// or whose covered text is no longer what was copied, is cleared.
func (m *Model) revalidateSelection() {
	if m.sel == nil {
		return
	}
	if !m.selectionEnabled() {
		m.clearSelection()
		return
	}
	from, to, ok := m.selRange()
	if !ok {
		m.clearSelection()
		return
	}
	if !m.sel.dragging && m.selectedText(from, to) != m.sel.copied {
		m.clearSelection()
	}
}

// paintSelection highlights the selected cells of the viewport's visible
// rows. Decoration (gutters, rails, right-aligned meta) is never lit, so
// what is highlighted is what was copied.
func (m *Model) paintSelection(view string) string {
	from, to, ok := m.selRange()
	if !ok {
		return view
	}
	lines := strings.Split(view, "\n")
	top := m.viewport.YOffset()
	for i := range lines {
		l := top + i
		if l < from.line || l > to.line {
			continue
		}
		sp := spanOf(m.selRowAt(l), m.viewport.Width())
		lo, hi := sp.text, sp.end
		if l == from.line {
			lo = max(lo, from.col)
		}
		if l == to.line {
			hi = min(hi, to.col+1)
		}
		if lo < hi {
			lines[i] = lipgloss.StyleRanges(lines[i], lipgloss.NewRange(lo, hi, selectionStyle))
		}
	}
	return strings.Join(lines, "\n")
}
```

2. Add the selection fields to `Model`, directly after `nodeRegions`.

Apply this change to `internal/app/tui/model.go`:

```diff
diff --git a/internal/app/tui/model.go b/internal/app/tui/model.go
index bbd3345e..cd77fd13 100644
--- a/internal/app/tui/model.go
+++ b/internal/app/tui/model.go
@@ -666,6 +666,17 @@ type Model struct {
 	// blocks. Rows inside a step record their own narrower ranges. See
 	// click.go.
 	nodeRegions []nodeRegion
+	// blockSpans are the content-line ranges of every transcript block, in
+	// order, under the keys a text selection anchors to. contentLines is the
+	// same content split into lines. Both are rebuilt with nodeRegions.
+	blockSpans   []blockSpan
+	contentLines []string
+	// sel is the transcript text selection, nil when there is none; press
+	// is a left press waiting for its release, and drag the pointer state
+	// of a drag in progress. See selection.go.
+	sel   *selection
+	press *tea.Mouse
+	drag  dragState
 	// viewStack is the subagent drill-down stack: when non-empty, the
 	// transcript viewport renders the top subagent's child session instead
 	// of the orchestrator's. Pushed by clicking a subagent card (see
```

3. In `refreshViewport`:
   - `addBlock` takes a `blockKey` and records a `blockSpan` for every block, not only clickable ones. Node blocks use `blockKey{node: node.ID}`; the others use the names `banner`, `sep:<turn key>`, `reconnect`, `notice` and `queued`.
   - Store `blockSpans` with `nodeRegions`.
   - Keep `contentLines`.
   - Skip `GotoBottom` while dragging. This is a separate check from `viewportFollow`, so the scroll-hint row never appears because of a drag.
   - Revalidate the selection last.

Apply this change to `internal/app/tui/viewport_render.go`:

```diff
diff --git a/internal/app/tui/viewport_render.go b/internal/app/tui/viewport_render.go
index 1895637e..bdbd0f30 100644
--- a/internal/app/tui/viewport_render.go
+++ b/internal/app/tui/viewport_render.go
@@ -123,13 +123,14 @@ func (m *Model) refreshViewport() {
 	var tight []bool
 	nextTight := false
 	var regions []nodeRegion
+	var spans []blockSpan
 	lineCursor := 0
 	// addBlock appends s to blocks (if non-empty) and records the
 	// content-line range it occupies so a later click can find it (see
 	// click.go). strings.Count is exact regardless of a block's internal
 	// formatting, because it counts the same "\n" characters strings.Join
 	// below will actually lay out on screen.
-	addBlock := func(s string, target *clickTarget, subs []subRegion) {
+	addBlock := func(key blockKey, s string, target *clickTarget, subs []subRegion) {
 		if s == "" {
 			return
 		}
@@ -137,6 +138,7 @@ func (m *Model) refreshViewport() {
 		tight = append(tight, nextTight)
 		nextTight = false
 		n := strings.Count(s, "\n")
+		spans = append(spans, blockSpan{key: key, start: lineCursor, end: lineCursor + n})
 		if target != nil {
 			regions = append(regions, nodeRegion{startLine: lineCursor, endLine: lineCursor + n, target: *target})
 		}
@@ -151,7 +153,7 @@ func (m *Model) refreshViewport() {
 	}
 
 	if !hasConversationTurns(items) {
-		addBlock(renderWelcomeBanner(width), nil, nil)
+		addBlock(blockKey{name: "banner"}, renderWelcomeBanner(width), nil, nil)
 	}
 	seen := map[viewmodel.NodeID]bool{}
 	tree := map[viewmodel.NodeID]*viewmodel.Node{}
@@ -162,7 +164,7 @@ func (m *Model) refreshViewport() {
 		// always reads as "a new turn starts here" rather than as a header.
 		if turn.ID.Key != "turn:pre" {
 			if !firstTurn {
-				addBlock(renderTurnSeparator(width), nil, nil)
+				addBlock(blockKey{name: "sep:" + turn.ID.Key}, renderTurnSeparator(width), nil, nil)
 			}
 			firstTurn = false
 		}
@@ -184,22 +186,23 @@ func (m *Model) refreshViewport() {
 				bitems = collectBrowse(bitems, node, out, subs, lineCursor, turnFirst)
 				turnFirst = false
 			}
-			addBlock(out, m.blockTarget(node), subs)
+			addBlock(blockKey{node: node.ID}, out, m.blockTarget(node), subs)
 			nextTight = false // never leaks onto a later block, even if this one was empty
 		}
 	}
 	if reconnect != "" {
-		addBlock(renderReconnectNotice(reconnect, m.activeSpinnerFrame(session.ActivityReconnecting), width), nil, nil)
+		addBlock(blockKey{name: "reconnect"}, renderReconnectNotice(reconnect, m.activeSpinnerFrame(session.ActivityReconnecting), width), nil, nil)
 	}
 	if noticeUp {
-		addBlock(renderNotice(notice, width), nil, nil)
+		addBlock(blockKey{name: "notice"}, renderNotice(notice, width), nil, nil)
 	}
 	if len(queued) > 0 {
-		addBlock(renderQueuedMessages(queued, width), nil, nil)
+		addBlock(blockKey{name: "queued"}, renderQueuedMessages(queued, width), nil, nil)
 	}
 
 	m.pruneRenderState(seen)
 	m.nodeRegions = regions
+	m.blockSpans = spans
 	m.setBrowseItems(bitems, tree)
 	m.taskStats = viewmodel.TaskStats(turns)
 	// Every block ends with exactly one newline; separation between blocks
@@ -216,9 +219,13 @@ func (m *Model) refreshViewport() {
 		content = m.paintCursor(content)
 	}
 	m.viewport.SetContent(content)
-	if m.viewportFollow {
+	m.contentLines = strings.Split(content, "\n")
+	// A drag holds the view still: following new output would move the
+	// text under the pointer.
+	if m.viewportFollow && !m.dragging() {
 		m.viewport.GotoBottom()
 	}
+	m.revalidateSelection()
 }
 
 // narrationOnly is a step that said something and ran nothing: the next step
```

4. In `renderTranscriptFrame`, paint the selection onto the viewport's visible rows.

Apply this change to `internal/app/tui/view.go`:

```diff
diff --git a/internal/app/tui/view.go b/internal/app/tui/view.go
index c2571803..05065945 100644
--- a/internal/app/tui/view.go
+++ b/internal/app/tui/view.go
@@ -176,7 +176,7 @@ func clipLeftColumn(s string, maxRows int) string {
 func (m Model) renderTranscriptFrame() string {
 	vpWidth := max(m.leftWidth, 1)
 	vpHeight := max(m.viewport.Height(), 1)
-	content := lipgloss.NewStyle().Width(vpWidth).Height(vpHeight).Render(m.viewport.View())
+	content := lipgloss.NewStyle().Width(vpWidth).Height(vpHeight).Render(m.paintSelection(m.viewport.View()))
 	if m.scrollHintRows() > 0 {
 		hint := mutedStyle().Render("↑ scrolled — End to follow")
 		content = lipgloss.JoinVertical(lipgloss.Left, hint, content)
```

5. Create `internal/app/tui/selection_test.go` with exactly this content:

```go
package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// selectionFixture is a two-turn transcript with every row shape the copier
// handles: a wrapped prompt, a markdown answer with a list and code, a turn
// separator, a narrated step with thinking, file, group and failed shell
// rows, a subagent card and a notice.
func selectionFixture(t *testing.T) (Model, *fakeClipboard) {
	t.Helper()
	m := newTestModel(t)
	m.resize(80, 80)
	clip := &fakeClipboard{available: true}
	m.copyWriter = clip
	m.copyRemote = func() bool { return false }
	m.state.AddMessage(session.RoleUser, "first line of the prompt\nsecond line, indented below", session.ContentTypePlain)
	m.state.AddMessageFinal(session.RoleAssistant, "Summary:\n\n1. raised the timeout\n   - nested item\n\n```go\nfunc g() {\n\tif x {\n\t\treturn\n\t}\n}\n```", session.ContentTypeMarkdown)
	m.state.AddMessage(session.RoleUser, "next turn", session.ContentTypePlain)
	ts := time.Now()
	id := m.state.BeginStep(session.Actor{})
	m.state.AddNarration(id, "Looking at the failing test. Then:\n\n- check the fixture\n\n```go\nfunc f() {\n    return\n}\n```")
	m.state.LogThinking(session.ThinkingEntry{Text: "private reasoning text", Duration: 2 * time.Second, StartedAt: ts, StepID: id})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", StepID: id, ToolCallID: "c1", Timestamp: ts.Add(1 * time.Millisecond), Args: []byte(`{"path":"a.go"}`), ResultContent: "package a", ResultSummary: "a.go (5 lines)", Approval: registry.ApprovalNotRequired})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "search.text", StepID: id, ToolCallID: "c2", Timestamp: ts.Add(2 * time.Millisecond), Args: []byte(`{"query":"Timeout"}`), ResultSummary: "3 matches"})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "search.text", StepID: id, ToolCallID: "c3", Timestamp: ts.Add(3 * time.Millisecond), Args: []byte(`{"query":"hookTimeout"}`), ResultSummary: "1 match"})
	code := 1
	m.state.LogToolCall(registry.AuditEvent{ToolName: "shell.run", StepID: id, ToolCallID: "c4", Timestamp: ts.Add(4 * time.Millisecond), Args: []byte(`{"command":"go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v -timeout 30s"}`), CommandExitCode: &code, Error: "exit status 1", ResultContent: "--- FAIL: TestX (0.01s)\n    hooks_test.go:12: timeout\nFAIL"})
	child := newChildState(t)
	child.AddNarration(child.BeginStep(session.Actor{}), "Tracing the config loader. It has three layers.")
	m.state.RegisterSubagent("reviewer", child)
	m.state.EndStep(id)
	m.state.AddMessage(session.RoleSystem, "Model switched to gpt-x\nsecond notice line", session.ContentTypePlain)
	m.invalidateTranscript()
	m.refreshViewport()
	return m, clip
}

// selectionFixtureCopy is what selecting the whole fixture copies.
const selectionFixtureCopy = `first line of the prompt
second line, indented below

Summary:

1. raised the timeout
  • nested item


  func g() {
          if x {
                  return
          }
  }

next turn

Looking at the failing test. Then:

• check the fixture

  func f() {
      return
  }

thought for 2s
Read file · a.go (5 lines)
Search texts: ×2 · Timeout, hookTimeout

go test ./internal/hooks/... -run TestPreToolUseBlockDecision -count=20 -v -timeout 30s · exit 1 · exit status 1
--- FAIL: TestX (0.01s)
    hooks_test.go:12: timeout
FAIL

reviewer
Tracing the config loader.

Model switched to gpt-x
second notice line`

// lineOf returns the first content line containing sub.
func lineOf(t *testing.T, m *Model, sub string) int {
	t.Helper()
	for i, l := range m.contentLines {
		if strings.Contains(stripANSI(l), sub) {
			return i
		}
	}
	t.Fatalf("no content line contains %q", sub)
	return -1
}

// selectRows selects from..to as a finished drag would: anchored, with the
// text it covers recorded as copied.
func selectRows(t *testing.T, m *Model, from, to selPos) {
	t.Helper()
	a, okA := m.anchorAt(from)
	h, okH := m.anchorAt(to)
	if !okA || !okH {
		t.Fatalf("no anchors for %+v..%+v", from, to)
	}
	m.sel = &selection{anchor: a, head: h}
	f, l, _ := m.selRange()
	m.sel.copied = m.selectedText(f, l)
}

// thinkKeyOf returns the node of the fixture's thinking row.
func thinkKeyOf(t *testing.T, m *Model) viewmodel.NodeID {
	t.Helper()
	l := lineOf(t, m, "thought for 2s")
	target, ok := m.regionAt(l)
	if !ok {
		t.Fatal("no region on the thinking row")
	}
	return target.node
}

func TestSelectedTextOfTheRenderedTranscript(t *testing.T) {
	m, _ := selectionFixture(t)
	selectRows(t, &m, selPos{0, 0}, selPos{len(m.contentLines) - 1, 79})
	if got := m.sel.copied; got != selectionFixtureCopy {
		t.Fatalf("copied:\n%s\n--- want:\n%s", got, selectionFixtureCopy)
	}
}

func TestPaintHighlightsOnlyCopiedCells(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	painted := strings.Split(m.paintSelection(m.viewport.View()), "\n")[l-m.viewport.YOffset()]
	if !strings.Contains(painted, "\x1b[7m") {
		t.Fatalf("selected row has no reverse-video run: %q", painted)
	}
	// The gutter glyph sits before the highlight.
	if i, j := strings.Index(painted, "·"), strings.Index(painted, "\x1b[7m"); i < 0 || i > j {
		t.Fatalf("gutter glyph is inside the highlight: %q", painted)
	}
}

func TestSelectionFollowsItsTextWhenRowsChangeAbove(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	copied := m.sel.copied
	// Expanding the thinking row adds rows above the selection.
	m.toggleExpanded(thinkKeyOf(t, &m))
	m.invalidateTranscript()
	m.refreshViewport()
	if m.sel == nil {
		t.Fatal("rows changing above cleared the selection")
	}
	from, to, _ := m.selRange()
	if got := m.selectedText(from, to); got != copied {
		t.Fatalf("selection now covers %q, want %q", got, copied)
	}
}

func TestSelectionClearsWhenItsTextChanges(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "thought for 2s")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m.toggleExpanded(thinkKeyOf(t, &m))
	m.invalidateTranscript()
	m.refreshViewport()
	if m.sel != nil {
		t.Fatal("expanding the selected row must clear the selection")
	}
}

func TestSelectionClearsWhenMouseCaptureTurnsOff(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m.state.Config.TUI.MouseCapture = false
	m.invalidateTranscript()
	m.refreshViewport()
	if m.sel != nil {
		t.Fatal("a selection must not outlive mouse capture")
	}
}
```

| Test | Asserts |
|---|---|
| `TestSelectedTextOfTheRenderedTranscript` | selecting the whole real fixture copies exactly `selectionFixtureCopy` |
| `TestPaintHighlightsOnlyCopiedCells` | a selected row carries reverse video, and the gutter glyph sits outside it |
| `TestSelectionFollowsItsTextWhenRowsChangeAbove` | expanding a row above keeps the selection on the same text |
| `TestSelectionClearsWhenItsTextChanges` | expanding the selected row clears it |
| `TestSelectionClearsWhenMouseCaptureTurnsOff` | a refresh with capture off clears it |

6. Add a `selection` variant to `TestFrameInvariants`. The painted selection must keep every row within the frame width and height.

Apply this change to `internal/app/tui/frame_invariants_test.go`:

```diff
diff --git a/internal/app/tui/frame_invariants_test.go b/internal/app/tui/frame_invariants_test.go
index b5403dc5..3f92b19f 100644
--- a/internal/app/tui/frame_invariants_test.go
+++ b/internal/app/tui/frame_invariants_test.go
@@ -67,6 +67,13 @@ func TestFrameInvariants(t *testing.T) {
 			m.busy = true
 			m.turnStartedAt = time.Now()
 		}},
+		{"selection", func(t *testing.T, m *Model) {
+			stepFixture(t, m, session.Actor{}, "Reading the parser to find the guard.", readEvent("a.go"), readEvent("b.go"))
+			m.state.AddMessageFinal(session.RoleAssistant, "Done. The guard is in the lexer.", session.ContentTypeMarkdown)
+			m.invalidateTranscript()
+			m.refreshViewport()
+			selectRows(t, m, selPos{0, 0}, selPos{len(m.contentLines) - 1, m.viewport.Width()})
+		}},
 		{"tasks+browsing", func(t *testing.T, m *Model) {
 			scriptedTasks(t, m, 3, 2, false)
 			m.refreshViewport()
```

**Verify:**

```bash
gofmt -l internal/app/tui/                      # no output
go vet ./internal/app/tui/
go test ./internal/app/tui/ -run 'Selection|Selected|Paint|FrameInvariants' -count=1   # ok
```

## Task 4: Mouse input — press, drag, release, wheel, auto-scroll

**Goal:**
- A left press records the press. A drag selects; release copies and keeps the highlight.
- A click without motion acts on release, as before.
- The wheel and edge auto-scroll extend a drag.
- Shift+click extends a selection.
- A press during a drag abandons it.

**Files:** `internal/app/tui/selection_input.go` (new), `internal/app/tui/model.go`, `internal/app/tui/click.go`, `internal/app/tui/click_test.go`, `internal/app/tui/drill_test.go`, `internal/app/tui/model_test.go`, `internal/app/tui/nowbar_test.go`, `internal/app/tui/steps_test.go`, `internal/app/tui/selection_input_test.go` (new).

Depends on: Task 3.

**Steps:**

1. Create `internal/app/tui/selection_input.go` with exactly this content:

```go
package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// selScrollTickMsg advances edge auto-scroll by one row.
type selScrollTickMsg struct{ gen int }

// selScrollInterval is how often edge auto-scroll moves the view one row.
const selScrollInterval = 50 * time.Millisecond

// contentPosAt maps a screen cell to a content position, clamping cells
// outside the viewport to its nearest edge.
func (m *Model) contentPosAt(x, y int) selPos {
	top := m.scrollHintRows() + m.breadcrumbRows()
	h := max(m.viewport.Height(), 1)
	y = min(max(y, top), top+h-1)
	x = min(max(x, 0), max(m.leftWidth-1, 0))
	return selPos{line: m.viewport.YOffset() + y - top, col: x}
}

// inViewport reports whether a screen cell is inside the transcript
// viewport.
func (m *Model) inViewport(x, y int) bool {
	_, ok := m.contentLineForClick(x, y)
	return ok
}

// edgeDir reports which way a pointer at screen row y should auto-scroll the
// transcript: -1 up, 1 down, 0 not at all. When nothing sits above the
// viewport, its first row is the top edge.
func (m *Model) edgeDir(y int) int {
	top := m.scrollHintRows() + m.breadcrumbRows()
	switch {
	case y < top || (top == 0 && y == 0):
		return -1
	case y >= top+m.viewport.Height():
		return 1
	}
	return 0
}

// handleTranscriptMouse owns every mouse event aimed at the transcript:
// presses, drags, releases and the wheel. A left click acts on release, so a
// press can still turn into a drag. handled is false for events it leaves to
// the caller.
func (m *Model) handleTranscriptMouse(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		return m.handleWheel(msg), true
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil, false
		}
		mouse := tea.Mouse(msg)
		if msg.Mod&tea.ModShift != 0 && m.sel != nil && !m.sel.dragging && m.inViewport(msg.X, msg.Y) {
			if h, ok := m.anchorAt(m.contentPosAt(msg.X, msg.Y)); ok {
				m.sel.head = h
				return m.finishSelection(), true
			}
		}
		if m.dragging() {
			// The release of the previous drag never arrived.
			m.clearSelection()
		}
		m.press = &mouse
		return nil, true
	case tea.MouseMotionMsg:
		if m.press == nil || !m.selectionEnabled() {
			return nil, false
		}
		m.drag.pointer = tea.Mouse(msg)
		if !m.dragging() {
			if (msg.X == m.press.X && msg.Y == m.press.Y) || !m.inViewport(m.press.X, m.press.Y) {
				return nil, true
			}
			a, ok := m.anchorAt(m.contentPosAt(m.press.X, m.press.Y))
			if !ok {
				return nil, true
			}
			m.drag.gen++
			m.drag.ticking = false
			m.sel = &selection{anchor: a, head: a, dragging: true}
		}
		if h, ok := m.anchorAt(m.contentPosAt(msg.X, msg.Y)); ok {
			m.sel.head = h
		}
		if m.edgeDir(msg.Y) != 0 && !m.drag.ticking {
			m.drag.ticking = true
			return m.selScrollTick(), true
		}
		return nil, true
	case tea.MouseReleaseMsg:
		if m.press == nil {
			return nil, false
		}
		press := *m.press
		m.press = nil
		if m.dragging() {
			m.sel.dragging = false
			m.drag.ticking = false
			if m.viewportFollow {
				m.viewport.GotoBottom()
			}
			return m.finishSelection(), true
		}
		m.sel = nil
		if cmd, handled := m.handleTranscriptClick(press); handled {
			return cmd, true
		}
		return m.handleNowBarClick(press)
	}
	return nil, false
}

// finishSelection copies the selection and remembers what was copied.
func (m *Model) finishSelection() tea.Cmd {
	from, to, ok := m.selRange()
	if !ok {
		m.clearSelection()
		return nil
	}
	m.sel.copied = m.selectedText(from, to)
	return m.copyText(m.sel.copied)
}

// handleWheel scrolls the transcript, or the bounded live region under the
// pointer. During a drag the selection's head follows the pointer to the
// text now under it.
func (m *Model) handleWheel(msg tea.MouseWheelMsg) tea.Cmd {
	// Vertical-only transcript: sideways trackpad pans and diagonal scrolls
	// must not shift the view horizontally.
	if msg.Button == tea.MouseWheelLeft || msg.Button == tea.MouseWheelRight {
		return nil
	}
	// A bounded live region under the cursor scrolls its own body instead of
	// the transcript. This must stay below the horizontal guard: above it, a
	// sideways pan would be consumed here and the vertical-only invariant
	// would be lost.
	if m.scrollLiveRegionAt(msg) {
		return nil
	}
	// Only delivered when tui.mouse_capture is on (see View). AltScreen
	// leaves no terminal scrollback, so without this the wheel does nothing
	// at all.
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	switch msg.Button {
	case tea.MouseWheelUp:
		m.viewportFollow = false
	case tea.MouseWheelDown:
		if m.viewport.AtBottom() {
			m.viewportFollow = true
		}
	}
	if m.dragging() {
		if h, ok := m.anchorAt(m.contentPosAt(msg.X, msg.Y)); ok {
			m.sel.head = h
		}
	}
	return vpCmd
}

// selScrollTick arms the next edge auto-scroll step for the current drag.
func (m *Model) selScrollTick() tea.Cmd {
	gen := m.drag.gen
	return tea.Tick(selScrollInterval, func(time.Time) tea.Msg { return selScrollTickMsg{gen: gen} })
}

// handleSelScrollTick scrolls one row toward the edge the pointer is held
// past and extends the selection, then re-arms while the pointer stays
// there.
func (m *Model) handleSelScrollTick(msg selScrollTickMsg) tea.Cmd {
	if msg.gen != m.drag.gen || !m.dragging() {
		return nil
	}
	dir := m.edgeDir(m.drag.pointer.Y)
	switch {
	case dir < 0 && !m.viewport.AtTop():
		m.viewport.ScrollUp(1)
		m.viewportFollow = false
	case dir > 0 && !m.viewport.AtBottom():
		m.viewport.ScrollDown(1)
	default:
		m.drag.ticking = false
		return nil
	}
	if h, ok := m.anchorAt(m.contentPosAt(m.drag.pointer.X, m.drag.pointer.Y)); ok {
		m.sel.head = h
	}
	return m.selScrollTick()
}
```

`handleWheel` replaces the two identical wheel handlers that `update` and `scrollTranscript` carry today, keeping their comments and behaviour.

2. In `model.go`:
   - Route `selScrollTickMsg` in `update`.
   - Replace `update`'s wheel and click cases with one case for all four mouse message types, calling `handleTranscriptMouse`.
   - Make the same replacement in `scrollTranscript`, so selection also works while an approval, question, sudo or skill-gate form is open. Its `handled` result passes through, so an unhandled release still reaches the form.

Apply this change to `internal/app/tui/model.go`:

```diff
diff --git a/internal/app/tui/model.go b/internal/app/tui/model.go
index cd77fd13..3d102730 100644
--- a/internal/app/tui/model.go
+++ b/internal/app/tui/model.go
@@ -2550,6 +2550,8 @@ func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 	case sessionsheet.RunCommandMsg:
 		m.closeSessionSheet()
 		return m.dispatchCommand("/" + msg.Command)
+	case selScrollTickMsg:
+		return m, m.handleSelScrollTick(msg)
 	case copyDoneMsg:
 		cmd := m.handleCopyDone(msg)
 		return m, cmd
@@ -3027,41 +3029,10 @@ func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
 	}
 
 	switch msg := msg.(type) {
-	case tea.MouseWheelMsg:
-		// Vertical-only transcript: sideways trackpad pans and diagonal
-		// scrolls must not shift the view horizontally.
-		if msg.Button == tea.MouseWheelLeft || msg.Button == tea.MouseWheelRight {
-			return m, nil
-		}
-		// A bounded live region under the cursor scrolls its own body
-		// instead of the transcript. This must stay below the horizontal
-		// guard: above it, a sideways pan would be consumed here and the
-		// vertical-only invariant would be lost.
-		if m.scrollLiveRegionAt(msg) {
-			return m, nil
-		}
-		// Only delivered when tui.mouse_capture is on (see View). AltScreen
-		// leaves no terminal scrollback, so without this the wheel does
-		// nothing at all.
-		var vpCmd tea.Cmd
-		m.viewport, vpCmd = m.viewport.Update(msg)
-		switch msg.Button {
-		case tea.MouseWheelUp:
-			m.viewportFollow = false
-		case tea.MouseWheelDown:
-			if m.viewport.AtBottom() {
-				m.viewportFollow = true
-			}
-		}
-		return m, vpCmd
-	case tea.MouseClickMsg:
-		if cmd, handled := m.handleTranscriptClick(msg); handled {
-			return m, cmd
-		}
-		if cmd, handled := m.handleNowBarClick(msg); handled {
-			return m, cmd
-		}
-		return m, nil
+	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
+		// Wheel, click and drag all go to the transcript (selection_input.go).
+		cmd, _ := m.handleTranscriptMouse(msg)
+		return m, cmd
 	case tea.PasteMsg:
 		if shouldCondensePaste(msg.Content) {
 			m.addPaste(msg.Content)
@@ -3710,37 +3681,10 @@ func (m Model) handleSudoAuth(msg tea.Msg, sa *session.PendingSudoAuth) (tea.Mod
 // viewport so the user can review history while an approval or question
 // panel is open. It returns (model, cmd, true) when the message was handled.
 func (m *Model) scrollTranscript(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
-	switch msg := msg.(type) {
-	case tea.MouseWheelMsg:
-		// Vertical-only transcript: swallow horizontal wheel pans so they
-		// never shift the view sideways.
-		if msg.Button == tea.MouseWheelLeft || msg.Button == tea.MouseWheelRight {
-			return *m, nil, true
-		}
-		// A bounded live region under the cursor scrolls its own body
-		// instead of the transcript. Must stay below the horizontal guard.
-		if m.scrollLiveRegionAt(msg) {
-			return *m, nil, true
-		}
-		var vpCmd tea.Cmd
-		m.viewport, vpCmd = m.viewport.Update(msg)
-		switch msg.Button {
-		case tea.MouseWheelUp:
-			m.viewportFollow = false
-		case tea.MouseWheelDown:
-			if m.viewport.AtBottom() {
-				m.viewportFollow = true
-			}
-		}
-		return *m, vpCmd, true
-	case tea.MouseClickMsg:
-		if cmd, handled := m.handleTranscriptClick(msg); handled {
-			return *m, cmd, true
-		}
-		if cmd, handled := m.handleNowBarClick(msg); handled {
-			return *m, cmd, true
-		}
-		return *m, nil, false
+	switch msg.(type) {
+	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
+		cmd, handled := m.handleTranscriptMouse(msg)
+		return *m, cmd, handled
 	}
 	if k, ok := msg.(tea.KeyPressMsg); ok {
 		switch k.String() {
```

3. In `click.go`, the two click handlers take a `tea.Mouse`. They read only `Button`, `X` and `Y`, so their bodies are unchanged.

Apply this change to `internal/app/tui/click.go`:

```diff
diff --git a/internal/app/tui/click.go b/internal/app/tui/click.go
index eaa033ee..35795f75 100644
--- a/internal/app/tui/click.go
+++ b/internal/app/tui/click.go
@@ -95,7 +95,7 @@ func (m *Model) nowBarBand(plan nowBarPlan) (top, bottom int, ok bool) {
 // The bar is often the only handle on a running child: its transcript card
 // can scroll far out of view while the parent keeps working. Clicks on other
 // bar rows are consumed so they do not fall through to the transcript.
-func (m *Model) handleNowBarClick(msg tea.MouseClickMsg) (tea.Cmd, bool) {
+func (m *Model) handleNowBarClick(msg tea.Mouse) (tea.Cmd, bool) {
 	if msg.Button != tea.MouseLeft {
 		return nil, false
 	}
@@ -187,7 +187,7 @@ func (m *Model) scrollLiveRegionAt(msg tea.MouseWheelMsg) bool {
 // region (regardless of whether that region was already at its target state
 // — a click always consumes the event once it's inside the viewport bounds,
 // matching the wheel-scroll handling right above it in Update).
-func (m *Model) handleTranscriptClick(msg tea.MouseClickMsg) (tea.Cmd, bool) {
+func (m *Model) handleTranscriptClick(msg tea.Mouse) (tea.Cmd, bool) {
 	if msg.Button != tea.MouseLeft {
 		return nil, false
 	}
```

4. Migrate existing click tests:
   - Each `updated, _ := m.Update(tea.MouseClickMsg{X: …, Y: …, Button: tea.MouseLeft})` becomes `updated := clickAt(t, m, …, …)`, a press plus release.
   - Each `m.handleNowBarClick(tea.MouseClickMsg{` becomes `m.handleNowBarClick(tea.Mouse{`.
   - Add `clickAt` to `click_test.go`.
   - Drop the now-unused `tea` import from `steps_test.go`.
   - Run `gofmt -w` on the five files, which tightens the spacing of `clickAt`'s arithmetic arguments.

Apply this change to `internal/app/tui/click_test.go`:

```diff
diff --git a/internal/app/tui/click_test.go b/internal/app/tui/click_test.go
index e0c283e0..6575db38 100644
--- a/internal/app/tui/click_test.go
+++ b/internal/app/tui/click_test.go
@@ -88,6 +88,15 @@ func TestRegionAtFindsContainingRegion(t *testing.T) {
 	}
 }
 
+// clickAt sends a left press and its release on one cell: a click. Clicks
+// act on release, so a press alone can still become a drag.
+func clickAt(t *testing.T, m tea.Model, x, y int) tea.Model {
+	t.Helper()
+	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
+	updated, _ = updated.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
+	return updated
+}
+
 func TestMouseClickTogglesThinkingBlock(t *testing.T) {
 	m := newTestModel(t)
 	m.resize(80, 24)
@@ -104,7 +113,7 @@ func TestMouseClickTogglesThinkingBlock(t *testing.T) {
 
 	top := m.scrollHintRows()
 	y := top + region.startLine - m.viewport.YOffset()
-	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
+	updated := clickAt(t, m, 1, y)
 	mm := asModel(t, updated)
 
 	if !mm.isExpanded(key) {
@@ -131,7 +140,7 @@ func TestMouseClickActiveToolExpandsPerToolCall(t *testing.T) {
 
 	top := m.scrollHintRows()
 	y := top + region.startLine - m.viewport.YOffset()
-	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
+	updated := clickAt(t, m, 1, y)
 	mm := asModel(t, updated)
 
 	if !mm.isToolExpanded(key, true) {
@@ -159,7 +168,7 @@ func TestMouseClickOutsideViewportIsNoop(t *testing.T) {
 	m.invalidateTranscript()
 	m.refreshViewport()
 
-	updated, _ := m.Update(tea.MouseClickMsg{X: m.leftWidth + 10, Y: 0, Button: tea.MouseLeft})
+	updated := clickAt(t, m, m.leftWidth+10, 0)
 	mm := asModel(t, updated)
 
 	key := thinkID(ts)
@@ -189,7 +198,7 @@ func TestMouseClickExpandsFailedToolCall(t *testing.T) {
 
 	top := m.scrollHintRows()
 	y := top + region.startLine - m.viewport.YOffset()
-	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
+	updated := clickAt(t, m, 1, y)
 	mm := asModel(t, updated)
 
 	if !mm.isExpanded(key) {
@@ -212,7 +221,7 @@ func TestNowBarClickDrillsIntoAgent(t *testing.T) {
 	if !ok {
 		t.Fatal("expected a now bar band")
 	}
-	if _, handled := m.handleNowBarClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top + plan.agentRowStart}); !handled {
+	if _, handled := m.handleNowBarClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: top + plan.agentRowStart}); !handled {
 		t.Fatal("a click on an agent row must be handled")
 	}
 	if len(m.viewStack) != 1 {
@@ -236,7 +245,7 @@ func TestNowBarClickOnProgressRowDoesNothing(t *testing.T) {
 		t.Fatalf("agentRowStart = %d, want 1 under a turn row", plan.agentRowStart)
 	}
 	top, _, _ := m.nowBarBand(plan)
-	if _, handled := m.handleNowBarClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top}); !handled {
+	if _, handled := m.handleNowBarClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: top}); !handled {
 		t.Fatal("a click inside the bar must be consumed")
 	}
 	if len(m.viewStack) != 0 {
@@ -305,7 +314,7 @@ func TestMarkerClickOpensTheBeatHidingItsCard(t *testing.T) {
 	}
 
 	top := m.scrollHintRows()
-	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: top + marker.startLine - m.viewport.YOffset(), Button: tea.MouseLeft})
+	updated := clickAt(t, m, 1, top+marker.startLine-m.viewport.YOffset())
 	mm := asModel(t, updated)
 	r, ok := mm.regionFor(card)
 	if !ok {
```

Apply this change to `internal/app/tui/drill_test.go`:

```diff
diff --git a/internal/app/tui/drill_test.go b/internal/app/tui/drill_test.go
index 9bc01ba2..19bce82e 100644
--- a/internal/app/tui/drill_test.go
+++ b/internal/app/tui/drill_test.go
@@ -46,7 +46,7 @@ func TestClickRunningCardDrillsInAndBack(t *testing.T) {
 
 	top := m.scrollHintRows()
 	y := top + region.startLine - m.viewport.YOffset()
-	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
+	updated := clickAt(t, m, 1, y)
 	mm := asModel(t, updated)
 	if len(mm.viewStack) != 1 {
 		t.Fatalf("click should drill into the subagent, viewStack len = %d", len(mm.viewStack))
@@ -448,7 +448,7 @@ func TestClickThinkingBlockWhileDrilledIntoSubagent(t *testing.T) {
 	y := top + region.startLine - m.viewport.YOffset()
 
 	// The click must land on the correct region and expand it.
-	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
+	updated := clickAt(t, m, 1, y)
 	mm := asModel(t, updated)
 	if !mm.isExpanded(key) {
 		t.Fatal("expected the click to expand the child's thinking block while drilled in (off-by-one fix)")
```

Apply this change to `internal/app/tui/model_test.go`:

```diff
diff --git a/internal/app/tui/model_test.go b/internal/app/tui/model_test.go
index 500095b2..6ec0d76d 100644
--- a/internal/app/tui/model_test.go
+++ b/internal/app/tui/model_test.go
@@ -7923,7 +7923,7 @@ func TestDrilledInChildActiveToolOverrideSurvivesRepaint(t *testing.T) {
 
 	top := m.scrollHintRows() + m.breadcrumbRows()
 	y := top + region.startLine - m.viewport.YOffset()
-	updated, _ := m.Update(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
+	updated := clickAt(t, m, 1, y)
 	mm := asModel(t, updated)
 
 	if !mm.isToolExpanded(key, true) {
```

Apply this change to `internal/app/tui/nowbar_test.go`:

```diff
diff --git a/internal/app/tui/nowbar_test.go b/internal/app/tui/nowbar_test.go
index 2b0c09d1..31a12694 100644
--- a/internal/app/tui/nowbar_test.go
+++ b/internal/app/tui/nowbar_test.go
@@ -746,7 +746,7 @@ func TestNowBarWorkOverflowClickOpensWork(t *testing.T) {
 	if !ok {
 		t.Fatal("expected a now bar band")
 	}
-	if _, handled := m.handleNowBarClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top + listRow}); !handled {
+	if _, handled := m.handleNowBarClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: top + listRow}); !handled {
 		t.Fatal("the overflow row click must be handled")
 	}
 	if !m.tasksOpen() || m.tasksPanel.activeTab != tasksTabWork {
@@ -789,7 +789,7 @@ func TestNowBarWorkCardClickTogglesExpansion(t *testing.T) {
 	if hit.Action != workActionExpand {
 		t.Fatalf("collapsed card action = %v, want expand", hit.Action)
 	}
-	if _, handled := m.handleNowBarClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top + hit.Row}); !handled {
+	if _, handled := m.handleNowBarClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: top + hit.Row}); !handled {
 		t.Fatal("the card header click must be handled")
 	}
 	if m.workExpanded != "work:a" {
@@ -802,7 +802,7 @@ func TestNowBarWorkCardClickTogglesExpansion(t *testing.T) {
 	if hit.Action != workActionCollapse {
 		t.Fatalf("expanded card action = %v, want collapse", hit.Action)
 	}
-	if _, handled := m.handleNowBarClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: top + hit.Row}); !handled {
+	if _, handled := m.handleNowBarClick(tea.Mouse{Button: tea.MouseLeft, X: 1, Y: top + hit.Row}); !handled {
 		t.Fatal("the collapse click must be handled")
 	}
 	if m.workExpanded != "" {
```

Apply this change to `internal/app/tui/steps_test.go`:

```diff
diff --git a/internal/app/tui/steps_test.go b/internal/app/tui/steps_test.go
index 0bb1a2e1..9503fbd6 100644
--- a/internal/app/tui/steps_test.go
+++ b/internal/app/tui/steps_test.go
@@ -7,7 +7,6 @@ import (
 	"testing"
 	"time"
 
-	tea "charm.land/bubbletea/v2"
 	"github.com/charmbracelet/x/ansi"
 
 	"marshal/internal/app/config"
@@ -277,7 +276,7 @@ func TestClickOnToolRowTogglesThatRowOnly(t *testing.T) {
 		t.Fatalf("row region %+v should sit inside the step region %+v", region, stepRegion)
 	}
 	y := m.scrollHintRows() + region.startLine - m.viewport.YOffset()
-	updated, _ := m.Update(tea.MouseClickMsg{X: 2, Y: y, Button: tea.MouseLeft})
+	updated := clickAt(t, m, 2, y)
 	mm := asModel(t, updated)
 	if !mm.isExpanded(row) {
 		t.Fatal("click on the row did not expand it")
```

5. Create `internal/app/tui/selection_input_test.go` with exactly this content:

```go
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

// screenY is the screen row of content line l.
func screenY(m *Model, l int) int {
	return m.scrollHintRows() + m.breadcrumbRows() + l - m.viewport.YOffset()
}

// dragTo presses at one cell, drags to another and releases there, running
// the release's command so a local copy lands in the fake clipboard.
func dragTo(t *testing.T, m Model, x0, y0, x1, y1 int) Model {
	t.Helper()
	next, _ := m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	next, cmd := next.Update(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	m = next.(Model)
	for _, msg := range flattenCmd(cmd) {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestDragCopiesTheRenderedTranscript(t *testing.T) {
	m, clip := selectionFixture(t)
	last := len(m.contentLines) - 1
	m = dragTo(t, m, 0, screenY(&m, 0), 79, screenY(&m, last))
	if len(clip.writes) != 1 {
		t.Fatalf("clipboard writes = %d, want 1", len(clip.writes))
	}
	if got := clip.writes[0]; got != selectionFixtureCopy {
		t.Fatalf("copied:\n%s\n--- want:\n%s", got, selectionFixtureCopy)
	}
	if m.sel == nil || m.sel.dragging {
		t.Fatalf("after release the selection must stay, not dragging: %+v", m.sel)
	}
}

func TestPressAloneDoesNotToggle(t *testing.T) {
	m, _ := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
	next, _ := m.Update(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if strings.Contains(stripANSI(strings.Join(mm.contentLines, "\n")), "private reasoning text") {
		t.Fatal("a press alone expanded the thinking row; clicks act on release")
	}
}

func TestClickTogglesOnRelease(t *testing.T) {
	m, _ := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "thought for 2s"))
	mm := asModel(t, clickAt(t, m, 8, y))
	if !strings.Contains(stripANSI(strings.Join(mm.contentLines, "\n")), "private reasoning text") {
		t.Fatal("press and release on one cell did not toggle the row")
	}
	if mm.sel != nil {
		t.Fatal("a click must not leave a selection")
	}
}

func TestClickClearsASelection(t *testing.T) {
	m, _ := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "Model switched"))
	m = dragTo(t, m, 3, y, 10, y)
	if m.sel == nil {
		t.Fatal("drag left no selection")
	}
	mm := asModel(t, clickAt(t, m, 3, y))
	if mm.sel != nil {
		t.Fatal("a click must clear the selection")
	}
}

func TestDragHoldsTheViewWhileOutputStreams(t *testing.T) {
	m, _ := selectionFixture(t)
	m.resize(80, 30)
	m.viewportFollow = true
	m.invalidateTranscript()
	m.refreshViewport()
	y := screenY(&m, lineOf(t, &m, "Model switched"))
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: 10, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if !mm.dragging() {
		t.Fatal("setup: the drag did not start")
	}
	offset, hint := mm.viewport.YOffset(), mm.scrollHintRows()
	for i := 0; i < 5; i++ {
		mm.state.AddMessage(session.RoleSystem, "streamed line", session.ContentTypePlain)
	}
	mm.invalidateTranscript()
	mm.refreshViewport()
	if mm.viewport.YOffset() != offset || mm.scrollHintRows() != hint {
		t.Fatalf("output moved the view during a drag: offset %d→%d, hint %d→%d", offset, mm.viewport.YOffset(), hint, mm.scrollHintRows())
	}
	next, _ = mm.Update(tea.MouseReleaseMsg{X: 10, Y: y, Button: tea.MouseLeft})
	mm = asModel(t, next)
	if !mm.viewport.AtBottom() {
		t.Fatal("following must resume on release")
	}
}

func TestDragPastTheBottomAutoScrolls(t *testing.T) {
	m, _ := selectionFixture(t)
	m.resize(80, 30)
	m.viewportFollow = false
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewport.GotoTop()
	top := m.scrollHintRows() + m.breadcrumbRows()
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: top, Button: tea.MouseLeft})
	below := top + m.viewport.Height() + 1
	next, cmd := next.Update(tea.MouseMotionMsg{X: 3, Y: below, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("dragging past the bottom armed no auto-scroll tick")
	}
	mm := asModel(t, next)
	if !mm.dragging() {
		t.Fatal("setup: the drag did not start")
	}
	offset := mm.viewport.YOffset()
	if cmd := mm.handleSelScrollTick(selScrollTickMsg{gen: mm.drag.gen}); cmd == nil {
		t.Fatal("tick did not re-arm while the pointer stays past the edge")
	}
	if mm.viewport.YOffset() != offset+1 {
		t.Fatalf("offset %d→%d, want one row down", offset, mm.viewport.YOffset())
	}
	if cmd := mm.handleSelScrollTick(selScrollTickMsg{gen: mm.drag.gen - 1}); cmd != nil || mm.viewport.YOffset() != offset+1 {
		t.Fatal("a tick from an earlier drag must do nothing")
	}
}

func TestWheelDuringADragExtendsTheSelection(t *testing.T) {
	m, _ := selectionFixture(t)
	m.resize(80, 30)
	m.viewportFollow = false
	m.invalidateTranscript()
	m.refreshViewport()
	m.viewport.GotoTop()
	top := m.scrollHintRows() + m.breadcrumbRows()
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: top, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: 3, Y: top + 2, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if !mm.dragging() {
		t.Fatal("setup: the drag did not start")
	}
	before, _ := mm.resolve(mm.sel.head)
	next, _ = mm.Update(tea.MouseWheelMsg{X: 3, Y: top + 2, Button: tea.MouseWheelDown})
	mm = asModel(t, next)
	after, _ := mm.resolve(mm.sel.head)
	if after.line <= before.line {
		t.Fatalf("head stayed at line %d after the wheel scrolled (was %d)", after.line, before.line)
	}
}

func TestShiftClickExtendsTheSelection(t *testing.T) {
	m, clip := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	m = dragTo(t, m, 3, screenY(&m, l), 10, screenY(&m, l))
	next, cmd := m.Update(tea.MouseClickMsg{X: 79, Y: screenY(&m, l+1), Button: tea.MouseLeft, Mod: tea.ModShift})
	m = asModel(t, next)
	for _, msg := range flattenCmd(cmd) {
		next, _ := m.Update(msg)
		m = asModel(t, next)
	}
	if len(clip.writes) != 2 || clip.writes[1] != "Model switched to gpt-x\nsecond notice line" {
		t.Fatalf("writes = %q, want a second copy reaching the next row", clip.writes)
	}
}

func TestPressDuringADragAbandonsIt(t *testing.T) {
	m, clip := selectionFixture(t)
	y := screenY(&m, lineOf(t, &m, "Model switched"))
	next, _ := m.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseMotionMsg{X: 10, Y: y, Button: tea.MouseLeft})
	next, _ = next.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	mm := asModel(t, next)
	if mm.sel != nil || len(clip.writes) != 0 {
		t.Fatalf("a press during a drag must drop it uncopied: sel=%v writes=%q", mm.sel, clip.writes)
	}
}

func TestNoSelectionWithoutMouseCapture(t *testing.T) {
	m, clip := selectionFixture(t)
	m.state.Config.TUI.MouseCapture = false
	l := lineOf(t, &m, "Model switched")
	m = dragTo(t, m, 0, screenY(&m, l), 79, screenY(&m, l))
	if m.sel != nil || len(clip.writes) != 0 {
		t.Fatalf("with mouse capture off: sel=%v writes=%q, want nothing", m.sel, clip.writes)
	}
}
```

| Test | Asserts |
|---|---|
| `TestDragCopiesTheRenderedTranscript` | press, drag over the whole fixture and release: one clipboard write equal to `selectionFixtureCopy`; the selection stays |
| `TestPressAloneDoesNotToggle` | a press without release expands nothing |
| `TestClickTogglesOnRelease` | press and release on one cell toggles and leaves no selection |
| `TestClickClearsASelection` | a click after a drag clears the selection |
| `TestDragHoldsTheViewWhileOutputStreams` | appended output moves neither the offset nor the scroll-hint row during a drag; following resumes on release |
| `TestDragPastTheBottomAutoScrolls` | a pointer below the viewport arms a tick that scrolls one row and re-arms; a stale tick does nothing |
| `TestWheelDuringADragExtendsTheSelection` | the wheel moves the head with the text under the pointer |
| `TestShiftClickExtendsTheSelection` | Shift+click moves the head and copies again |
| `TestPressDuringADragAbandonsIt` | a second press drops the drag without copying |
| `TestNoSelectionWithoutMouseCapture` | with `tui.mouse_capture` off, a drag selects and copies nothing |

**Verify:**

```bash
gofmt -l internal/app/tui/                      # no output
go vet ./internal/app/tui/
go test ./internal/app/tui/ -count=1 -skip '^TestStatusLineUntrustedIsWarningColored$'   # ok
```

## Task 5: Esc, Ctrl+S and drill

**Goal:**
- Esc clears a selection before it does anything else.
- Drilling in or out clears it.
- The Ctrl+S re-capture notice says that dragging selects and copies.

**Files:** `internal/app/tui/keypress.go`, `internal/app/tui/model.go`, `internal/app/tui/selection_keys_test.go` (new).

Depends on: Task 3. It runs after Task 4 because both edit `model.go`.

**Steps:**

1. In `keypress.go`'s main Esc branch, clear the selection right after the completion-popup dismissal and before `popDrill`. Change the re-capture notice text. Ctrl+S needs no explicit clear: its notice rebuilds the transcript, and `revalidateSelection` clears the selection once capture is off.

Apply this change to `internal/app/tui/keypress.go`:

```diff
diff --git a/internal/app/tui/keypress.go b/internal/app/tui/keypress.go
index 85b78fb5..089f2550 100644
--- a/internal/app/tui/keypress.go
+++ b/internal/app/tui/keypress.go
@@ -156,6 +156,12 @@ func (m *Model) handleKeypress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
 			m.completionSuppressed = true
 			return *m, nil, true
 		}
+		// A text selection is the last thing the user made: Esc clears it
+		// and does nothing else.
+		if m.sel != nil {
+			m.clearSelection()
+			return *m, nil, true
+		}
 		// While drilled into a subagent, Esc pops back to the parent
 		// transcript. Ctrl+X while drilled into a running subagent stops
 		// that subagent; Ctrl+C (twice) stops the whole turn.
@@ -231,7 +237,7 @@ func (m *Model) handleKeypress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
 		m.mouseReleased = !m.mouseReleased
 		verb := "released to the terminal — click-drag to select, keys to scroll (PgUp/PgDn/Ctrl+U/Ctrl+D/End)"
 		if !m.mouseReleased {
-			verb = "captured — wheel scrolls the transcript"
+			verb = "captured — wheel scrolls, drag selects and copies"
 		}
 		m.state.AddMessage(session.RoleSystem, "Mouse "+verb+". Ctrl+S to toggle.", session.ContentTypePlain)
 		m.refreshViewport()
```

2. In `model.go`, `drillIntoSubagent` and `popDrill` clear the selection. Step IDs can collide between a parent and child transcript, so revalidation alone could leave a selection on the wrong block.

Apply this change to `internal/app/tui/model.go`:

```diff
diff --git a/internal/app/tui/model.go b/internal/app/tui/model.go
index 3d102730..a2877710 100644
--- a/internal/app/tui/model.go
+++ b/internal/app/tui/model.go
@@ -4283,6 +4283,7 @@ func (m *Model) drillIntoSubagent(v session.SubagentView) {
 		return
 	}
 	m.viewStack = append(m.viewStack, v)
+	m.clearSelection()
 	m.invalidateTranscript()
 	m.viewportFollow = true
 }
@@ -4308,6 +4309,7 @@ func (m *Model) popDrill() bool {
 		return false
 	}
 	m.viewStack = m.viewStack[:len(m.viewStack)-1]
+	m.clearSelection()
 	m.invalidateTranscript()
 	return true
 }
```

3. Create `internal/app/tui/selection_keys_test.go` with exactly this content:

```go
package tui

import (
	"strings"
	"testing"
)

func TestEscClearsTheSelectionBeforeBrowsing(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m = pressKeys(m, "esc")
	if m.sel != nil || m.browsing {
		t.Fatalf("first Esc: sel=%v browsing=%v, want the selection cleared and nothing else", m.sel, m.browsing)
	}
	m = pressKeys(m, "esc")
	if !m.browsing {
		t.Fatal("second Esc should enter browse mode")
	}
}

func TestDrillClearsTheSelection(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m.drillIntoSubagent(m.state.Subagents()[0])
	if m.sel != nil {
		t.Fatal("drilling in must clear the selection")
	}
	selectRows(t, &m, selPos{0, 0}, selPos{0, 79})
	m.popDrill()
	if m.sel != nil {
		t.Fatal("drilling out must clear the selection")
	}
}

func TestCtrlSClearsTheSelectionAndNamesDragToCopy(t *testing.T) {
	m, _ := selectionFixture(t)
	l := lineOf(t, &m, "Model switched")
	selectRows(t, &m, selPos{l, 0}, selPos{l, 79})
	m = pressKeys(m, "ctrl+s")
	if m.sel != nil {
		t.Fatal("Ctrl+S must clear the selection")
	}
	m = pressKeys(m, "ctrl+s")
	msgs := m.state.Messages()
	if last := msgs[len(msgs)-1].Content; !strings.Contains(last, "drag selects and copies") {
		t.Fatalf("re-capture notice = %q, want it to say drag selects and copies", last)
	}
}
```

| Test | Asserts |
|---|---|
| `TestEscClearsTheSelectionBeforeBrowsing` | the first Esc only clears; the second enters browse mode |
| `TestDrillClearsTheSelection` | drilling in and popping back each clear |
| `TestCtrlSClearsTheSelectionAndNamesDragToCopy` | Ctrl+S clears; re-capturing posts the new notice |

**Verify:**

```bash
gofmt -l internal/app/tui/                      # no output
go vet ./internal/app/tui/
go test ./internal/app/tui/ -run 'Esc|Drill|CtrlS|Browse' -count=1   # ok
```

## Task 6: Footer hint

**Goal:** while the mouse is captured, the status-line hint offers `drag copy` instead of `Ctrl+S select`. Released, it still says `Ctrl+S scroll`.

**Files:** `internal/app/tui/help/help.go`, `internal/app/tui/help/help_test.go`.

Parallel-safe with: Tasks 1–5.

**Steps:**

1. Apply this change to `internal/app/tui/help/help.go`:

```diff
diff --git a/internal/app/tui/help/help.go b/internal/app/tui/help/help.go
index 2ba2efb3..0516b31e 100644
--- a/internal/app/tui/help/help.go
+++ b/internal/app/tui/help/help.go
@@ -155,7 +155,7 @@ func Footer(h FooterHints) string {
 		if h.MouseReleased {
 			segs = append(segs, pair("Ctrl+S", "scroll"))
 		} else {
-			segs = append(segs, pair("Ctrl+S", "select"))
+			segs = append(segs, pair("drag", "copy"))
 		}
 	}
 	if showHelpHint {
```

2. Append `TestFooterMouseHintNamesDragWhileCaptured`, which checks both labels.

Apply this change to `internal/app/tui/help/help_test.go`:

```diff
diff --git a/internal/app/tui/help/help_test.go b/internal/app/tui/help/help_test.go
index ae67218d..31b7205d 100644
--- a/internal/app/tui/help/help_test.go
+++ b/internal/app/tui/help/help_test.go
@@ -136,3 +136,14 @@ func TestFooterShowsTodoToggleWhenTodosActive(t *testing.T) {
 		t.Fatalf("busy footer must keep its cancel-focused hints:\n%s", busy)
 	}
 }
+
+func TestFooterMouseHintNamesDragWhileCaptured(t *testing.T) {
+	captured := stripANSI(Footer(FooterHints{}))
+	if !strings.Contains(captured, "drag copy") || strings.Contains(captured, "Ctrl+S select") {
+		t.Fatalf("captured footer should offer drag-to-copy: %q", captured)
+	}
+	released := stripANSI(Footer(FooterHints{MouseReleased: true}))
+	if !strings.Contains(released, "Ctrl+S scroll") {
+		t.Fatalf("released footer should offer Ctrl+S to scroll: %q", released)
+	}
+}
```

**Verify:**

```bash
gofmt -l internal/app/tui/help/                 # no output
go test ./internal/app/tui/help/ -count=1      # ok
```

## Task 7: Full verification, manual check and docs

**Goal:** the repository builds, vets and tests clean apart from known baseline failures. The manual terminal check is recorded, and the spec and plan are committed with the work.

**Files:** `docs/daemon/p0-text-selection.md`, `docs/daemon/plans/2026-10-19-p0-text-selection-plan.md`, `docs/daemon/design.md`.

**Steps:**

1. Run the repository's checks (AGENTS.md):

   ```bash
   gofmt -l .                                    # no output
   CGO_ENABLED=1 go build ./cmd/marshal
   go vet ./...
   go test ./internal/app/tui/... -count=1 -skip '^TestStatusLineUntrustedIsWarningColored$'
   TMPDIR=/private/tmp/p0-suite go test ./... -count=1
   ```

   Create `/private/tmp/p0-suite` first. The `TMPDIR` override avoids the `/var` to `/private/var` symlink failures in `internal/snapshot` and `internal/app`. In the full run, only these known local failures are acceptable:
   - `TestStatusLineUntrustedIsWarningColored`;
   - the flaky `TestRunManagedCancellationKillsTree`;
   - `TestStandaloneBackendNavigateAndRead`;
   - `TestCLIGitOpsCommitAllWithoutARepoIdentity`.

   Confirm any other failure against a clean worktree at the base commit before treating it as caused by this change.

2. Manual check, using the binary from step 1:
   - **Terminals:** iTerm2, Ghostty and Terminal.app locally, then one of them over SSH (OSC 52).
   - **The check:** start a turn that streams. Drag from inside the visible transcript to rows above it, scrolling both by holding the pointer past the top edge and with the wheel. The paste must match the transcript's plain text under spec B6.
   - **Also confirm:**
     - a click still toggles rows;
     - Esc clears;
     - Ctrl+S still hands the mouse to the terminal;
     - selection works while an approval prompt is open.
   - **Record per terminal:**
     - the result;
     - whether Shift+click arrives;
     - any clipboard limits (Terminal.app has no OSC 52; iTerm2 needs clipboard access enabled; tmux needs `set-clipboard on`).

3. Add an "Implementation record" section at the end of `docs/daemon/p0-text-selection.md`: the commit range, the terminal results from step 2, and any deviation from this plan. Set this plan's Status line to executed. In `docs/daemon/design.md` §7, mark P0 done.

4. Commit `docs/daemon/` as `p0-text-selection: task 7 — verification and docs`. If the design doc's later phases are not ready to publish, commit only the P0 spec and plan.

**Verify:** step 1's commands give the results stated, step 2's record is written, and `git status` shows no stray files from this work.

## Integration notes

- **Click timing changes.** Every left click now acts on release, not press. That includes now-bar clicks and clicks while a panel form is open. Tests that drive clicks must send a press and a release (`clickAt`).
- **One wheel handler.** The wheel logic that was duplicated in `update` and `scrollTranscript` now lives in `handleWheel`. Change it there.
- **The copier is positional.** It reads the gutter geometry the renderers produce. If a renderer changes its gutter, rail or meta layout, `TestSelectedTextOfTheRenderedTranscript` and `TestDragCopiesTheRenderedTranscript` fail. Update `selectionFixtureCopy` deliberately, not to make them pass.
- **`stepHeaderLine` in `steps.go` is dead code** (the live headline is built inline in `renderStep`). This plan leaves it alone.

## Plan self-review

- **Self-contained and independently verifiable:** yes. Replayed task by task on a fresh worktree at the base, with each task's Verify passing after it.
- **Cold-executor test:** every new file is complete and every edit is an exact diff. No step asks the executor to make a design decision.
- **Cross-task declarations:** all are in Contracts, with the names matching the code.
- **Parallel-safe markers:** Task 1 and Task 2 share no files and neither uses the other's output. Task 6 touches only `help/`.
- **Anchors:** all confirmed at the base: `renderCompletedToolCallIn`, `renderToolGroup`, `refreshViewport`'s `addBlock`, `renderTranscriptFrame`, `update`, `scrollTranscript`, the Esc branch, the Ctrl+S notice, `drillIntoSubagent`, `popDrill` and the footer's mouse hint.
- **Verification commands:** match AGENTS.md, with the known baseline failure skipped by name.
- **Placeholders:** none.
- **Against the spec:** the spec was revised during planning so that B6, B7 and the design section describe this implementation. Reverse video replaces the proposed theme token; the reasons are in the spec's design section.
