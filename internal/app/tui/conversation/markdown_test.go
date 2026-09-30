package conversation

import (
	"strings"
	"testing"
)

// project is the test shorthand: project with defaults and lay out at a width
// wide enough that only the projection is under test.
func project(t *testing.T, source string, width int) RenderedBlock {
	t.Helper()
	sp := ProjectMarkdown(source, MarkdownOptions{})
	return RenderedBlock{
		BlockID: "msg:1",
		Width:   width,
		Logical: sp.Text,
		Rows:    Layout(sp, LayoutOptions{Width: width}),
	}
}

// readable returns the projection's readable text: the non-decorative content,
// read off the rows with each row's own separator.
func readable(block RenderedBlock) string { return reassembled(block.Rows) }

// markdownFixtures are the constructs the projection must handle, chosen so that
// each one exercises a different emission path rather than a different wording.
var markdownFixtures = []struct {
	name string
	src  string
}{
	{"plain paragraph", "A single paragraph."},
	{"two paragraphs", "First paragraph.\n\nSecond paragraph."},
	{"heading", "# Title\n\nBody."},
	{"deep heading", "#### Deep\n\nBody."},
	{"emphasis", "an *italic* and a **bold** word"},
	{"emphasis in heading", "## A **bold** title"},
	{"inline code", "run `go test ./...` now"},
	{"fenced code", "```go\nfunc main() {}\n```"},
	{"fenced code with tab", "```go\nif x {\n\treturn\n}\n```"},
	{"tilde fence", "~~~\nraw\n~~~"},
	{"unclosed fence", "```go\nstill code"},
	{"bulleted list", "- one\n- two\n- three"},
	{"nested list", "- outer\n  - inner\n- outer two"},
	{"ordered list", "1. first\n2. second"},
	{"ordered list from three", "3. third\n4. fourth"},
	{"blockquote", "> quoted line"},
	{"multiline blockquote", "> line one\n> line two"},
	{"blockquote with emphasis", "> quoted *emphasis*"},
	{"thematic break", "above\n\n---\n\nbelow"},
	{"link", "see [the docs](https://example.com/docs)"},
	{"autolink", "visit <https://example.com>"},
	{"image", "![alt text](img.png)"},
	{"entity", "AT&amp;T and &lt;tag&gt;"},
	{"hard break", "line one  \nline two"},
	{"table", "| a | b |\n| --- | --- |\n| 1 | 2 |"},
	{"table with wide cells", "| 名前 | 値 |\n| --- | --- |\n| 日本 | 1 |"},
	{"strikethrough", "~~gone~~ kept"},
	{"raw html", "<div>raw</div>"},
	{"empty", ""},
	{"only whitespace", "   \n"},
}

// Every construct the projection accepts must produce bytes that map: each one
// either parses into text or falls back to its own source. A construct that
// produced NOTHING would silently delete content from a reader's screen.
func TestEveryMarkdownConstructProjectsSomeText(t *testing.T) {
	for _, f := range markdownFixtures {
		if f.name == "empty" || f.name == "only whitespace" {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			block := project(t, f.src, 80)
			if strings.TrimSpace(block.LogicalText()) == "" {
				t.Fatalf("projection of %q is empty; content was dropped", f.src)
			}
		})
	}
}

// The whole point of the projection: syntax that is not content must not appear
// in what a reader copies.
func TestMarkdownSyntaxIsNotPartOfTheReadableText(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"heading markers", "## Title", "Title"},
		{"emphasis markers", "an *italic* word", "an italic word"},
		{"strong markers", "a **bold** word", "a bold word"},
		{"inline code backticks", "run `now`", "run now"},
		{"fence lines", "```go\nx := 1\n```", "x := 1"},
		// The author's "-" is replaced by the projection's own bullet: the
		// marker stays (a list must read as a list) and the source character
		// does not.
		{"list marker source", "- item one", "• item one"},
		{"link destination", "see [docs](https://x.test)", "see docs"},
		{"image destination", "![alt](img.png)", "alt"},
		{"strikethrough markers", "~~gone~~ kept", "gone kept"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := readable(project(t, c.src, 80))
			if got != c.want {
				t.Fatalf("readable text of %q = %q, want %q", c.src, got, c.want)
			}
		})
	}
}

// Chrome must not reach readable text; STRUCTURE must.
//
// This is the distinction that is easy to collapse and expensive to get wrong.
// "Don't copy the renderer's chrome" is right, and implementing it as "don't copy
// anything the renderer generated" silently strips the bullet out of every list
// and the separator out of every table — leaving a copy that is not what the
// reader read.
func TestChromeIsExcludedFromReadableTextButStructureIsNot(t *testing.T) {
	t.Run("an indent is chrome", func(t *testing.T) {
		sp := ProjectMarkdown("code", MarkdownOptions{})
		rows := Layout(sp, LayoutOptions{Width: 40, Indent: 4})
		if got := rows[0].Text(); got != "    code" {
			t.Fatalf("display form = %q, want the indent shown", got)
		}
		if got := rows[0].ContentText(); got != "code" {
			t.Fatalf("readable form = %q, want the indent dropped", got)
		}
	})
	t.Run("a rule is chrome", func(t *testing.T) {
		block := project(t, "above\n\n---\n\nbelow", 40)
		got := readable(block)
		if strings.Contains(got, "─") {
			t.Fatalf("readable form carries the generated rule: %q", got)
		}
		if !strings.Contains(got, "above") || !strings.Contains(got, "below") {
			t.Fatalf("readable form lost the surrounding text: %q", got)
		}
	})
	t.Run("a bullet is structure", func(t *testing.T) {
		block := project(t, "- one", 40)
		got := readable(block)
		if !strings.Contains(got, "•") {
			t.Fatalf("readable form dropped the list bullet: %q", got)
		}
		if !strings.Contains(rowsText(block.Rows), "•") {
			t.Fatalf("display form dropped the list bullet: %q", rowsText(block.Rows))
		}
	})
	t.Run("a quote marker is structure", func(t *testing.T) {
		block := project(t, "> quoted", 40)
		if got := readable(block); !strings.Contains(got, ">") {
			t.Fatalf("readable form dropped the quote marker: %q", got)
		}
	})
	t.Run("a table separator is structure shown expanded", func(t *testing.T) {
		src := "| a | b |\n| --- | --- |\n| 1 | 2 |"
		sp := ProjectMarkdown(src, MarkdownOptions{})
		if !strings.Contains(sp.Text, "\t") {
			t.Fatalf("the logical separator is not a tab: %q", sp.Text)
		}
		block := RenderedBlock{Logical: sp.Text, Rows: Layout(sp, LayoutOptions{Width: 80})}
		// The display expands the tab to the terminal's tab stop...
		if got := rowsText(block.Rows); strings.Contains(got, "\t") {
			t.Fatalf("the display form still has a literal tab: %q", got)
		}
		// ...and the readable form keeps it, so the fields stay separated.
		if got := readable(block); !strings.Contains(got, "\t") {
			t.Fatalf("the readable form lost the field separator: %q", got)
		}
	})
}

// A table's readable projection is its VALUES, one row per line: a pasted row
// has to be usable in a spreadsheet, and a picture of a table is not.
func TestATableCopiesAsValuesNotAsABorderPicture(t *testing.T) {
	block := project(t, "| name | value |\n| --- | --- |\n| a | 1 |\n| b | 2 |", 80)
	got := readable(block)
	want := "name\tvalue\na\t1\nb\t2"
	if got != want {
		t.Fatalf("table copied as %q, want %q", got, want)
	}
	for _, border := range []string{"|", "─"} {
		if strings.Contains(got, border) {
			t.Fatalf("the copied table contains a border %q: %q", border, got)
		}
	}
}

// A link copies as its visible label. The URL is not spliced into the words —
// and it is not lost either: Copy answer carries the original Markdown, which
// still has it. Losing it HERE is the point, not a bug.
func TestALinkCopiesAsItsLabelAndTheURLStaysInTheSource(t *testing.T) {
	const src = "see [the docs](https://example.com/docs) for more"
	block := project(t, src, 80)
	got := readable(block)
	if got != "see the docs for more" {
		t.Fatalf("link copied as %q", got)
	}
	if strings.Contains(got, "example.com") {
		t.Fatalf("the URL leaked into the readable text: %q", got)
	}
	// The source still has it, which is what Copy answer preserves.
	if !strings.Contains(src, "example.com") {
		t.Fatal("the fixture must keep the URL in the source")
	}
}

// A fenced code block keeps the author's bytes and loses only the fence. That
// includes interior indentation, blank lines and tabs.
func TestCodeBlocksKeepTheirBytesAndLoseOnlyTheFence(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"simple", "```\nx := 1\n```", "x := 1"},
		{"indented", "```\nif x {\n    return\n}\n```", "if x {\n    return\n}"},
		{"tab indented", "```\nif x {\n\treturn\n}\n```", "if x {\n\treturn\n}"},
		{"with info string", "```go title=main.go\nx := 1\n```", "x := 1"},
		{"blank line inside", "```\na\n\nb\n```", "a\n\nb"},
		{"trailing spaces", "```\na   \n```", "a   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			block := project(t, c.src, 80)
			if got := block.LogicalText(); got != c.want {
				t.Fatalf("code projected as %q, want %q", got, c.want)
			}
			// A tab is logical, not expanded, so the copy is byte-identical.
			if want := strings.Count(c.want, "\t"); want > 0 {
				if got := strings.Count(block.LogicalText(), "\t"); got != want {
					t.Fatalf("tabs in the logical text: got %d, want %d", got, want)
				}
			}
		})
	}
}

// A fenced code block INSIDE a container keeps the container's prefix on its
// own source lines: a quote-fenced block is quoted line by line ("quoted
// code" keeps its ">" per line), and a CODE block is still code (SpanCode)
// inside a LIST (its marker is the bullet, not a fence rewrap). writeCode
// slices from lines() whose starts include the container's indentation, so
// the prefix rides along.
func TestAFencedCodeBlockInsideAContainerKeepsTheContainerPrefix(t *testing.T) {
	t.Run("in a blockquote", func(t *testing.T) {
		sp := ProjectMarkdown("> ```\n> code line\n> ```", MarkdownOptions{})
		if !strings.Contains(sp.Text, "> code line") {
			t.Fatalf("a quoted code block lost the quote prefix: %q", sp.Text)
		}
		// The code itself is still styled as code, not as plain body text.
		off := strings.Index(sp.Text, "code line")
		if got := kindAt(sp.Runs, off); got != SpanCode {
			t.Fatalf("the quoted code has kind %v, want SpanCode (runs %+v)", got, sp.Runs)
		}
	})
	t.Run("in a list item", func(t *testing.T) {
		sp := ProjectMarkdown("- item\n\n  ```\n  keep me\n  ```", MarkdownOptions{})
		if !strings.Contains(sp.Text, "keep me") {
			t.Fatalf("a fenced block inside a list item lost its content: %q", sp.Text)
		}
		off := strings.Index(sp.Text, "keep me")
		if got := kindAt(sp.Runs, off); got != SpanCode {
			t.Fatalf("the fenced block's content has kind %v, want SpanCode", got)
		}
	})
}

// A multi-line blockquote carries its marker on EVERY line, not just the
// first: a projection that emitted "> " once and then the bare body would
// read as a quote ending after one line, and a copy of it would lose the
// structure the reader saw. This is the test a mutation that collapsed
// writeQuote to one marker plus a plain body survived.
func TestAMultilineBlockquoteKeepsItsMarkerOnEveryLine(t *testing.T) {
	block := project(t, "> line one\n> line two", 80)
	got := readable(block)
	want := "> line one\n> line two"
	if got != want {
		t.Fatalf("multiline blockquote read as %q, want %q (the marker on every line)", got, want)
	}
	// And the quote markers themselves are still marked decorative, so a
	// selection copy can drop them on purpose if it wants.
	sp := ProjectMarkdown("> line one\n> line two", MarkdownOptions{})
	sawQuoteMarker := 0
	for _, r := range sp.Runs {
		if r.Kind == SpanQuote {
			sawQuoteMarker++
		}
	}
	if sawQuoteMarker != 2 {
		t.Fatalf("the projection styled %d quote markers, want one per line", sawQuoteMarker)
	}
}

// Emphasis INSIDE a blockquote keeps its styling: the sub-projection's runs
// are re-applied through the re-emission, and a mutation that dropped them
// (one marker plus a plain body) passed every existing test.
func TestEmphasisInsideAQuoteKeepsItsKind(t *testing.T) {
	sp := ProjectMarkdown("> a *b*", MarkdownOptions{})
	off := strings.Index(sp.Text, "b")
	if off < 0 {
		t.Fatalf("projection %q does not contain the quoted word", sp.Text)
	}
	if got := kindAt(sp.Runs, off); got != SpanEmphasis {
		t.Fatalf("the quoted word has kind %v, want emphasis (runs %+v)", got, sp.Runs)
	}
	// The marker, not the word, is the quote's own styling.
	if got := kindAt(sp.Runs, 0); got != SpanQuote {
		t.Fatalf("the quote marker has kind %v, want SpanQuote", got)
	}
}

// A run must cover every byte of the logical text, so a consumer can style or
// search any position without a gap to special-case.
func TestEveryLogicalByteIsCoveredByARun(t *testing.T) {
	for _, f := range markdownFixtures {
		t.Run(f.name, func(t *testing.T) {
			sp := ProjectMarkdown(f.src, MarkdownOptions{})
			if sp.Text == "" {
				return
			}
			covered := make([]bool, len(sp.Text))
			for _, r := range sp.Runs {
				if !r.Range.HasText() {
					t.Fatalf("run %+v carries no text range", r)
				}
				if r.Range.Start < 0 || r.Range.End > len(sp.Text) {
					t.Fatalf("run %+v is outside the text (%d bytes)", r, len(sp.Text))
				}
				for i := r.Range.Start; i < r.Range.End; i++ {
					covered[i] = true
				}
			}
			for i, ok := range covered {
				if !ok {
					t.Fatalf("byte %d (%.20q) is in no run: text=%q runs=%+v",
						i, sp.Text[i:], sp.Text, sp.Runs)
				}
			}
		})
	}
}

// Runs must be ordered and non-overlapping, or a consumer reading "the run at
// this offset" gets an answer that depends on walk order.
func TestRunsAreOrderedAndDoNotOverlap(t *testing.T) {
	for _, f := range markdownFixtures {
		t.Run(f.name, func(t *testing.T) {
			sp := ProjectMarkdown(f.src, MarkdownOptions{})
			for i := 1; i < len(sp.Runs); i++ {
				prev, cur := sp.Runs[i-1], sp.Runs[i]
				if cur.Range.Start < prev.Range.End {
					t.Fatalf("run %d %+v overlaps run %d %+v in %q",
						i, cur, i-1, prev, sp.Text)
				}
			}
		})
	}
}

// Styling survives the projection for the constructs that carry it. A projection
// that produced only plain text would render correctly and be useless: the
// renderer styles through these kinds and has nothing else to go on.
func TestProjectionCarriesStylingKinds(t *testing.T) {
	cases := []struct {
		name string
		src  string
		text string
		kind SpanKind
	}{
		{"heading", "## Title", "Title", SpanHeading},
		{"emphasis", "an *italic* word", "italic", SpanEmphasis},
		{"strong", "a **bold** word", "bold", SpanStrong},
		{"inline code", "run `now`", "now", SpanCode},
		{"code block", "```\nx := 1\n```", "x := 1", SpanCode},
		{"link label", "[docs](https://x.test)", "docs", SpanLink},
		{"bullet", "- item", "• ", SpanBullet},
		{"quote marker", "> quoted", "> ", SpanQuote},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := ProjectMarkdown(c.src, MarkdownOptions{})
			off := strings.Index(sp.Text, c.text)
			if off < 0 {
				t.Fatalf("projection %q does not contain %q", sp.Text, c.text)
			}
			if got := kindAt(sp.Runs, off); got != c.kind {
				t.Fatalf("text %q at offset %d has kind %v, want %v (runs %+v)",
					c.text, off, got, c.kind, sp.Runs)
			}
		})
	}
}

// Nested styling must not lose the outer kind: a bold word inside a heading is
// still heading text, and a heading whose words reverted to plain would render
// as body copy.
func TestNestedStylingKeepsTheOuterKind(t *testing.T) {
	sp := ProjectMarkdown("## A **bold** title", MarkdownOptions{})
	off := strings.Index(sp.Text, "A ")
	if off < 0 {
		t.Fatalf("projection %q missing the heading text", sp.Text)
	}
	if got := kindAt(sp.Runs, off); got != SpanHeading {
		t.Fatalf("the heading's unemphasised text has kind %v, want heading", got)
	}
	bold := strings.Index(sp.Text, "bold")
	if got := kindAt(sp.Runs, bold); got != SpanStrong {
		t.Fatalf("the nested bold text has kind %v, want strong", got)
	}
	// And the styling after the nested run returns to the heading.
	after := strings.Index(sp.Text, " title")
	if got := kindAt(sp.Runs, after+1); got != SpanHeading {
		t.Fatalf("text after the nested run has kind %v, want heading", got)
	}
}

// Character references resolve: the screen shows "&", so the projection — and
// therefore a copy — must carry "&", not "&amp;". A copy that produced the
// entity would be a different document from the one that was read.
func TestCharacterReferencesResolveInTheLogicalText(t *testing.T) {
	block := project(t, "AT&amp;T and &lt;tag&gt;", 80)
	if got := block.LogicalText(); got != "AT&T and <tag>" {
		t.Fatalf("logical text = %q, want the resolved characters", got)
	}
}

// ...except inside inline code, where the author's literal bytes ARE the
// content. Resolving "&amp;" there would change what a piece of code says,
// which is the one thing a code projection must never do.
func TestCharacterReferencesDoNotResolveInsideInlineCode(t *testing.T) {
	block := project(t, "use `a &amp;&amp; b` here", 80)
	if got := block.LogicalText(); !strings.Contains(got, "a &amp;&amp; b") {
		t.Fatalf("inline code was rewritten: %q", got)
	}
}

// Raw HTML has no text representation, so it keeps its literal source: showing
// the tags is honest, and a copy of them still maps.
func TestRawHTMLEntitiesAreNotResolved(t *testing.T) {
	block := project(t, "<div>&amp;</div>", 80)
	if got := block.LogicalText(); !strings.Contains(got, "&amp;") {
		t.Fatalf("raw HTML was rewritten: %q", got)
	}
}

// An unmapped construct is projected as its own source, which is the plan's
// explicit fallback: plain and mapped, never rich and unmapped.
func TestAnUnmappedConstructFallsBackToItsSource(t *testing.T) {
	block := project(t, "<div>raw</div>", 80)
	if got := block.LogicalText(); !strings.Contains(got, "raw") {
		t.Fatalf("raw HTML projected as %q, want its own text present", got)
	}
	if strings.TrimSpace(readable(block)) == "" {
		t.Fatal("raw HTML produced nothing readable")
	}
}

// A heading's own line ends where the author's did, so a projection that read
// two blocks off as lines keeps them apart in a copy.
func TestBlockBoundariesSurviveInTheReadableText(t *testing.T) {
	block := project(t, "# Title\n\nFirst body.\n\n- item", 80)
	got := readable(block)
	want := "Title\nFirst body.\n• item"
	if got != want {
		t.Fatalf("readable text = %q, want %q", got, want)
	}
}

// The projection carries no trailing newline: the break between blocks is the
// caller's separator, and a projection that shipped one would leave a blank line
// after every block a caller concatenates.
func TestProjectionCarriesNoTrailingNewline(t *testing.T) {
	for _, f := range markdownFixtures {
		t.Run(f.name, func(t *testing.T) {
			sp := ProjectMarkdown(f.src, MarkdownOptions{})
			if strings.HasSuffix(sp.Text, "\n") {
				t.Fatalf("projection of %q ends with a newline: %q", f.src, sp.Text)
			}
		})
	}
}

// An empty source projects to nothing at all, so a caller does not lay out a
// stray blank row for a block with no content.
func TestEmptySourceProjectsToNothing(t *testing.T) {
	sp := ProjectMarkdown("", MarkdownOptions{})
	if sp.Text != "" || len(sp.Runs) != 0 {
		t.Fatalf("empty source projected to text %q runs %+v", sp.Text, sp.Runs)
	}
	block := project(t, "", 40)
	if len(block.Rows) != 0 {
		t.Fatalf("empty source laid out %d rows: %+v", len(block.Rows), block.Rows)
	}
}

// The projection is laid out by the same mapping as any other text, so a
// projected block still satisfies the width invariant. A projection that
// produced rows wider than the terminal would be the visible failure this whole
// task exists to prevent.
func TestProjectedRowsRespectTheWidth(t *testing.T) {
	for _, f := range markdownFixtures {
		t.Run(f.name, func(t *testing.T) {
			for _, width := range []int{12, 30, 80} {
				block := project(t, f.src, width)
				for i, row := range block.Rows {
					if row.Cells > width {
						t.Fatalf("width %d: row %d is %d cells: %q",
							width, i, row.Cells, row.Text())
					}
				}
			}
		})
	}
}

// A drag across a projected block returns the readable text, not the source and
// not the display picture. This is the join between this task and the selection
// work: the two have to agree about what the text is.
func TestTextAcrossAProjectedBlockReturnsReadableText(t *testing.T) {
	block := project(t, "run `go test` now", 80)
	// Row 0 is "run go test now"; select from the first cell through the last.
	row := block.Rows[0]
	got := block.textAcross(0, 0, 0, row.Cells)
	if got != "run go test now" {
		t.Fatalf("textAcross over a projected row = %q, want the readable text", got)
	}
	if strings.Contains(got, "`") {
		t.Fatalf("the backticks leaked into the selection: %q", got)
	}
}
