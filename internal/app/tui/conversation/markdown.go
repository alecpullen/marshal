package conversation

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	astext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// This file projects Markdown into the styled logical text that the mapped
// renderer lays out.
//
// It walks the Goldmark AST ONCE and emits text and spans together. The
// alternative the plan names and rejects — render with Glamour, then recover the
// source by substring-matching the rendered string — cannot work: Glamour's
// output is wrapped, indented, padded and coloured, so every offset in it is a
// cell position in a different string, and the mapping back to source becomes a
// search that succeeds most of the time. "Most of the time" is the problem: a
// match that lands one character off puts a selection one character off,
// silently, and only for the inputs nobody tested.
//
// Three texts are in play here, and confusing them is how this goes wrong:
//
//   - The SOURCE is the author's Markdown, exactly as written.
//   - The LOGICAL text is a readable projection of it: paragraphs, headings,
//     lists, tables and code with their syntax resolved, in the order a reader
//     reads them. It is what a copy of a rendering yields, and what Task 12
//     selects from. It is NOT the source: a fence is gone, a link's URL is
//     gone, and "**" is gone.
//   - The ORIGINAL Markdown is what "Copy answer" preserves (Task 4), and it is
//     none of the above. Stripping a fence is right for a readable projection
//     and wrong for a payload whose recipient expects Markdown.
//
// The projection inserts text the author did not write — a list bullet, a table
// cell separator, a quote marker — and marks it with a decorative SpanKind. It
// is really there in the logical text (so the ranges stay a plain slice of one
// string) and it is excluded wherever it matters: a row's readable projection,
// a hit test, and a selection copy.
//
// Every byte of the logical text is covered by a run, so a consumer can style
// it, search it and select it without ever looking at an escape sequence.

// MarkdownOptions configures the projection.
type MarkdownOptions struct {
	// Bullet is the marker placed before an unordered list item. Decoration.
	Bullet string
	// CellSeparator is placed between table cells, replacing the table's
	// border in the readable projection. Decoration.
	CellSeparator string
	// QuoteMarker is placed before each line of a blockquote. Decoration.
	QuoteMarker string
	// Rule is the line a thematic break projects to. Decoration.
	Rule string
}

// defaultMarkdownOptions returns the projection's defaults.
//
// The cell separator is a TAB, which is the point rather than a quirk: a reader
// pasting a table row into a spreadsheet or an editor wants fields between the
// values, and the border character a rendered table draws between them is
// exactly what makes a pasted row useless.
func defaultMarkdownOptions() MarkdownOptions {
	return MarkdownOptions{
		Bullet:        "• ",
		CellSeparator: "\t",
		QuoteMarker:   "> ",
		Rule:          "───",
	}
}

// markdownParser is the Goldmark instance the projection parses with.
//
// GFM is enabled because tables, strikethrough and task lists are ordinary
// output for a coding agent, and a parser that did not know them would hand
// their syntax through as literal text. It is built once: constructing it per
// call reparses the extension set every time a block renders.
var markdownParser = goldmark.New(goldmark.WithExtensions(extension.GFM))

// ProjectMarkdown projects one block of Markdown into styled logical text.
//
// An empty source yields an empty projection, and the projection never ends with
// a newline: a block's trailing break is the separator BETWEEN blocks, which is
// the caller's business, and a projection that carried one would put a blank
// line after every heading and before every list.
//
// Note what a caller does NOT get back: the source bytes. A consumer that needs
// those (a "copy the original Markdown" action) must keep them itself, because
// this projection has deliberately resolved them away — a fence is gone, and so
// is a link's URL.
func ProjectMarkdown(source string, opts MarkdownOptions) Spans {
	if source == "" {
		return Spans{}
	}
	if opts.Bullet == "" && opts.CellSeparator == "" {
		opts = defaultMarkdownOptions()
	}
	src := []byte(source)
	root := markdownParser.Parser().Parse(text.NewReader(src))
	w := &mdWriter{src: src, opts: opts}
	w.blocks(root)
	text, runs := trimTrailingNewline(w.b.String(), w.runs)
	return Spans{Text: text, Runs: runs}
}

// trimTrailingNewline drops a single trailing break, adjusting the runs so they
// still cover exactly the text that remains.
//
// One break only: two would be a deliberate blank line at the end of the
// author's text, and collapsing it would quietly change what they wrote.
func trimTrailingNewline(text string, runs []Run) (string, []Run) {
	if !strings.HasSuffix(text, "\n") {
		return text, runs
	}
	text = strings.TrimSuffix(text, "\n")
	n := len(text)
	out := make([]Run, 0, len(runs))
	for _, r := range runs {
		if r.Range.Start >= n {
			continue
		}
		if r.Range.End > n {
			r.Range.End = n
		}
		out = append(out, r)
	}
	return text, out
}

// mdWriter accumulates logical text and the runs that style it.
//
// Offsets are tracked by the builder's own length rather than counted at each
// call site: a run's range names bytes in the FINAL text, and the builder is the
// only thing that knows that length without a second pass.
type mdWriter struct {
	src  []byte
	opts MarkdownOptions
	b    strings.Builder
	runs []Run
	// kind is the styling in effect. It is a field rather than a parameter
	// because inline nesting is unbounded (emphasis inside a link inside a
	// heading) and threading it through every case would make the one thing
	// that matters — what style this text has — the hardest thing to read.
	kind SpanKind
}

// write appends logical text under a kind.
func (w *mdWriter) write(s string, kind SpanKind) {
	if s == "" {
		return
	}
	start := w.b.Len()
	w.b.WriteString(s)
	w.runs = append(w.runs, Run{Range: Range{Start: start, End: start + len(s)}, Kind: kind})
}

// writeDecor appends text the RENDERER generated rather than the author.
//
// It carries a real range, because it really is in the logical text — that is
// what keeps the ranges a plain slice of one string with no gaps to reconcile.
// What makes it decoration is its KIND: every consumer that wants the document
// rather than the picture filters on SpanKind.Decorative, which is one rule in
// one place instead of a special case at each of them.
func (w *mdWriter) writeDecor(s string, kind SpanKind) {
	w.write(s, kind)
}

// newline ends the current line, under the ambient kind so a run is never
// split by a break it did not cause.
func (w *mdWriter) newline() { w.write("\n", w.kind) }

// under emits content with a temporarily different kind.
func (w *mdWriter) under(kind SpanKind, emit func()) {
	saved := w.kind
	w.kind = kind
	emit()
	w.kind = saved
}

// blocks walks a block-level subtree.
func (w *mdWriter) blocks(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.block(c)
	}
}

// block emits one block-level node.
//
// Core Markdown is matched by node TYPE and GFM's additions by node KIND,
// because the two are different things: a type switch can only match types, and
// astext.KindTable is a value. Getting these confused compiles into a switch
// that silently never matches the extension nodes and passes their syntax
// through as text.
func (w *mdWriter) block(n ast.Node) {
	switch t := n.(type) {
	case *ast.Heading:
		w.under(SpanHeading, func() { w.inlines(t) })
		w.newline()
	case *ast.Paragraph:
		w.inlines(t)
		w.newline()
	case *ast.TextBlock:
		// A paragraph's continuation inside a container (a list item, a
		// definition). Its content is inline and its line ends here.
		w.inlines(t)
		w.newline()
	case *ast.FencedCodeBlock:
		w.writeCode(t)
	case *ast.CodeBlock:
		w.writeCode(t)
	case *ast.Blockquote:
		w.writeQuote(t)
	case *ast.List:
		w.writeList(t)
	case *ast.ListItem:
		w.listItem(t, nil, 0)
	case *ast.ThematicBreak:
		// The author wrote "---", which is syntax. The rule it produces is the
		// renderer's, so it is decoration and copies as nothing.
		w.writeDecor(w.opts.Rule, SpanRule)
		w.newline()
	case *ast.HTMLBlock:
		w.writeSource(n)
	default:
		w.blockByKind(n)
	}
}

// blockByKind handles the extension blocks, which have no distinct Go type to
// switch on.
func (w *mdWriter) blockByKind(n ast.Node) {
	switch n.Kind() {
	case astext.KindTable:
		if t, ok := n.(*astext.Table); ok {
			w.writeTable(t)
			return
		}
	case astext.KindDefinitionList:
		w.blocks(n)
		return
	case astext.KindDefinitionTerm:
		w.under(SpanStrong, func() { w.inlines(n) })
		w.newline()
		return
	case astext.KindDefinitionDescription:
		w.blocks(n)
		return
	}
	// An unmodelled block: project its source lines plainly. This is the
	// explicit fallback the plan requires — an unmapped construct gets a
	// PLAIN-SOURCE representation with a valid mapping, never rich text that
	// does not map. A container with no lines of its own still has children to
	// show.
	if !w.writeSource(n) {
		w.blocks(n)
	}
}

// writeSource emits a node's own source lines as plain text, and reports
// whether it had any.
//
// This is also what keeps raw HTML honest: HTML has no text representation, so
// showing its source is the truthful projection, and the bytes map because they
// are emitted unchanged.
func (w *mdWriter) writeSource(n ast.Node) bool {
	src := nodeSource(n, w.src)
	if src == "" {
		return false
	}
	w.write(src, SpanPlain)
	if !strings.HasSuffix(src, "\n") {
		w.newline()
	}
	return true
}

// writeCode emits a code block's source with its fence removed.
//
// The content is sliced from the source between the fence lines rather than read
// from the parsed node, for the same reason ParseCodeFences slices it: the
// node's value is a normalised rendering (dedented, with an invented final
// newline), and the projection is meant to show the author's bytes. Interior
// indentation, blank lines and tabs are the author's and are preserved; a tab
// stays a tab here and is expanded only at layout, which is what keeps a copy of
// indented code byte-identical.
func (w *mdWriter) writeCode(n ast.Node) {
	body := nodeSource(n, w.src)
	if body == "" {
		// An empty fence is still a code block, and it shows as an empty line
		// rather than vanishing: a reader who wrote it should see it.
		w.newline()
		return
	}
	w.write(body, SpanCode)
	if !strings.HasSuffix(body, "\n") {
		w.newline()
	}
}

// writeQuote emits a blockquote with a marker before each line.
//
// The body is projected by a SUB-WRITER and then re-emitted line by line, which
// is what lets the marker be inserted without losing the inner styling: each
// line's runs are re-applied through writeRunsWithin, so emphasis inside a quote
// stays emphasis while the offsets stay exact. Prepending to each line in place
// instead would shift every offset after the first one, and the shift is not a
// constant — it is one marker per line.
func (w *mdWriter) writeQuote(n *ast.Blockquote) {
	sub := &mdWriter{src: w.src, opts: w.opts}
	sub.blocks(n)
	body := strings.TrimSuffix(sub.b.String(), "\n")
	if body == "" {
		return
	}
	lines := strings.Split(body, "\n")
	// starts[i] is where line i begins in body, so a sub-run can be mapped into
	// its line by subtracting it.
	starts := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		starts[i] = off
		off += len(l) + 1
	}
	for i, line := range lines {
		w.writeDecor(w.opts.QuoteMarker, SpanQuote)
		w.writeRunsWithin(line, starts[i], sub.runs)
		w.newline()
	}
}

// writeRunsWithin emits text, styled by the runs that cover it. Text not covered
// is plain, which is what makes the result exactly text with no gaps.
func (w *mdWriter) writeRunsWithin(text string, base int, runs []Run) {
	pos := 0
	for _, r := range runs {
		start := max(r.Range.Start-base, 0)
		end := min(r.Range.End-base, len(text))
		if end <= start {
			continue
		}
		if start > pos {
			w.write(text[pos:start], SpanPlain)
		}
		w.write(text[start:end], r.Kind)
		pos = end
	}
	if pos < len(text) {
		w.write(text[pos:], SpanPlain)
	}
}

// writeList emits a list's items, numbering them from the author's own start.
func (w *mdWriter) writeList(n *ast.List) {
	for i, c := range children(n) {
		w.listItem(c, n, i)
	}
}

// listItem emits one item: its marker, then its content.
//
// The marker is decoration. An ordered list keeps the author's numbering (a list
// beginning at 3 still reads "3."), which is why the start is read from the list
// rather than counted from one.
func (w *mdWriter) listItem(n ast.Node, list *ast.List, index int) {
	marker := w.opts.Bullet
	if list != nil && list.IsOrdered() {
		start := list.Start
		if start <= 0 {
			start = 1
		}
		marker = itoa(start+index) + ". "
	}
	if marker != "" {
		w.writeDecor(marker, SpanBullet)
	}
	w.blocks(n)
}

// writeTable projects a table as one row per line, cells separated by the
// configured separator.
//
// Borders are not emitted at all: the readable projection of a table row is its
// values. The header row keeps heading styling so a reader can tell it apart.
func (w *mdWriter) writeTable(t *astext.Table) {
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		kind := SpanPlain
		if _, ok := row.(*astext.TableHeader); ok {
			kind = SpanHeading
		}
		for i, cell := range children(row) {
			if i > 0 {
				w.writeDecor(w.opts.CellSeparator, SpanTableSep)
			}
			w.under(kind, func() { w.inlines(cell) })
		}
		w.newline()
	}
}

// itoa renders a small integer, including a negative one.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// children returns a node's children, for a walk that needs their positions.
func children(n ast.Node) []ast.Node {
	var out []ast.Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, c)
	}
	return out
}

// inlines walks a node's inline content.
func (w *mdWriter) inlines(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.inline(c)
	}
}

// inline emits one inline node.
func (w *mdWriter) inline(n ast.Node) {
	switch t := n.(type) {
	case *ast.Text:
		// The text node's segment names the source bytes directly, which is the
		// whole reason no reverse lookup is needed.
		//
		// Character references are resolved EXCEPT inside raw content. The
		// screen shows "&" where the author wrote "&amp;", so a copy that
		// produced "&amp;" would be a different document from the one that was
		// read; but inside inline code the author's literal bytes are the
		// content, and resolving there would change what the code says.
		text := t.Segment.Value(w.src)
		if !t.IsRaw() {
			text = util.ResolveNumericReferences(text)
			text = util.ResolveEntityNames(text)
		}
		w.write(string(text), w.kind)
		if t.HardLineBreak() || t.SoftLineBreak() {
			// Both are a real break in the projection. The distinction between
			// them is preserved in the SOURCE, which Copy answer uses; inside
			// the readable projection both are simply a line ending, and
			// preserving the source's distinction here would put two spaces
			// before a break into a copy.
			w.newline()
		}
	case *ast.String:
		w.write(string(t.Value), w.kind)
	case *ast.CodeSpan:
		// Inline code keeps its bytes exactly: interior spacing is meaningful,
		// and a copy of `a  b` must not become `a b`.
		w.under(SpanCode, func() { w.inlines(t) })
	case *ast.Emphasis:
		kind := SpanEmphasis
		if t.Level >= 2 {
			kind = SpanStrong
		}
		w.under(kind, func() { w.inlines(t) })
	case *ast.Link:
		// Only the LABEL is logical text. The destination is syntax: a reader
		// copying a link's words does not want the URL spliced into them, and
		// the URL survives untouched in the original Markdown that Copy answer
		// puts on the clipboard.
		w.under(SpanLink, func() { w.inlines(t) })
	case *ast.Image:
		// An image's alt text is the only part a text medium can show, so it is
		// the logical text; its destination is not shown at all.
		w.under(SpanLink, func() { w.inlines(t) })
	case *ast.AutoLink:
		w.write(string(t.Label(w.src)), SpanLink)
	case *ast.RawHTML:
		w.write(strings.Join(segmentText(n, w.src), ""), SpanPlain)
	default:
		if n.Kind() == astext.KindStrikethrough {
			// Strikethrough's text is still text; only its decoration is lost,
			// and losing decoration is better than losing words.
			w.inlines(n)
			return
		}
		// Inline content of an unmodelled kind: emit its text plainly so it
		// stays readable and selectable rather than disappearing.
		if n.Type() == ast.TypeInline && n.FirstChild() == nil {
			if !w.writeSource(n) {
				return
			}
			return
		}
		w.inlines(n)
	}
}

// nodeSource returns a block node's source lines, or "" when it has none.
//
// The span runs from the start of the first line to the end of the last, so a
// container's prefix (a list marker, a quote's ">") is included: this is source
// as written, which is what an unmodelled construct falls back to showing.
func nodeSource(n ast.Node, src []byte) string {
	lines := n.Lines()
	if lines.Len() == 0 {
		return ""
	}
	first := lines.At(0)
	last := lines.At(lines.Len() - 1)
	start := lineStart(src, first.Start)
	stop := last.Stop
	if stop > len(src) || start > stop {
		return ""
	}
	return string(src[start:stop])
}

// segmentText collects a node's source segments, for a node carrying raw bytes
// with no inline children.
func segmentText(n ast.Node, src []byte) []string {
	lines := n.Lines()
	if lines.Len() == 0 {
		return nil
	}
	out := make([]string, 0, lines.Len())
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		if seg.Start <= len(src) && seg.Stop <= len(src) && seg.Start <= seg.Stop {
			out = append(out, string(src[seg.Start:seg.Stop]))
		}
	}
	return out
}
