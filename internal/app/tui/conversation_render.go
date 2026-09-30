package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/theme"
)

// mappedIndent is the indent the mapped layout applies to prose.
//
// It MUST equal the transcript's gutterWidth, because renderFinalAnswerWithSink
// substitutes one for the other on a block's first line: the substitution is what
// keeps a click's cell equal to a screen column. renderMappedMessage reads it as
// the layout indent, and TestMappedIndentMatchesTheTranscriptGutter asserts the
// equality, so a change to either constant fails a test rather than silently
// shifting every offset in a block by a column.
const mappedIndent = 3

// This file turns a projected block into styled screen text.
//
// The rendering itself is deliberately thin: the projection (conversation's
// markdown parser) already decided what the text IS and which spans are which
// kind, so all that is left is choosing a colour per kind and joining the rows.
// Doing layout here as well — the obvious alternative — is what produces the two
// divergences this task exists to remove: text that wraps differently from the
// offsets a click maps to, and a copy that contains the renderer's own padding.
//
// There is deliberately NO per-block render cache here. There was one, keyed on
// (block, revision, width, mode, theme tier), and nothing ever constructed or
// read it: the transcript path builds its blocks through the sink and the
// document path is called only by tests, so the cache was dead weight whose only
// live effect was a reset guard in the session-switch path. A cache that no
// caller populates cannot make anything faster, and its key — a struct listing
// every input that can change the output — is a silent-staleness hazard the
// moment somebody wires it up without also wiring the invalidation. If block
// rendering ever needs memoizing it should be added together with the caller
// that populates it and a test that pins the invalidation.

// BlockRenderMode selects how much of a block is rendered.
type BlockRenderMode int

const (
	// BlockRenderFull renders the whole block.
	BlockRenderFull BlockRenderMode = iota
	// BlockRenderSummary renders a collapsed block as one line.
	BlockRenderSummary
)

// renderConversationBlock renders one block to styled screen text at a width.
//
// It delegates to the mapped renderer, and that is the point: this function and
// the live transcript path used to be TWO layout paths, and they disagreed. One
// laid out with no indent; the live one laid out with `mappedIndent` and let the
// caller substitute its gutter glyph on the first line. Wiring the document path
// up as it was would have drawn every block three columns to the left of where
// the transcript draws it and shifted every cell offset in the RenderedBlock by
// three.
//
// There is now exactly one layout, so that class of divergence cannot come back.
// The caller discards the mapping; a consumer that wants to select, click or
// search a block goes through the transcript path, which keeps it.
//
// The returned text has no trailing newline: the caller joins blocks, and a
// per-block newline is how a transcript acquires a blank line between every pair
// of items.
func (m *Model) renderConversationBlock(block conversation.Block, width int, mode BlockRenderMode) string {
	if width <= 0 || block.Text == "" {
		return ""
	}
	text, _ := renderMappedBlock(block.Text, width, mode, mappedIndent)
	return strings.TrimSuffix(text, "\n")
}

// renderConversationBlock is the pure rendering, with no model and no indent.
//
// It exists for callers that have a Block and no Model, and it is the indent-free
// form of the mapped layout (a Block has no transcript gutter — it is a document
// fragment, not a transcript row).
//
// An unmeasured width (zero or negative) renders NOTHING rather than rendering
// unwrapped text. A caller has not yet laid its frame out, and emitting the text
// whole would draw it under the side rail until the next resize happened to fix
// it; rendering nothing gives the caller an empty string it can recognise and
// render again once it is measured.
func renderConversationBlock(block conversation.Block, width int, mode BlockRenderMode) string {
	if width <= 0 || block.Text == "" {
		return ""
	}
	text, _ := renderMappedBlock(block.Text, width, mode, 0)
	return strings.TrimSuffix(text, "\n")
}

// mappedMessageSink collects the mappings produced during one transcript build.
//
// It exists because the renderers are pure functions reached through a chain that
// has no model in it (renderTranscriptItem → renderMessage → renderFinalAnswer),
// and threading a Model through four signatures to collect one value would make
// every one of those renderers harder to call for no other benefit. The sink is
// nil in tests that render a single item and do not care where it landed.
//
// A sink is per-BUILD, not per-model: refreshViewport creates one, the renderers
// fill it, and the result is stored on the model. Reusing one across builds would
// accumulate stale placements from a layout that no longer exists.
type mappedMessageSink struct {
	// pending is the mapping produced by the most recent mapped render, waiting
	// to be claimed by the block loop that knows the block's identity and its
	// position in the transcript.
	pending *conversation.RenderedBlock
	// pendingOffset is how many display lines the block renders BEFORE the
	// mapped body starts.
	//
	// It exists because a block is not only its body. A final answer with
	// captured reasoning renders the `⚙ thought for Ns ▹` summary above it, a
	// salvaged answer renders a "salvaged" note above it, and every copyable
	// answer renders the copy chip BELOW it. The mapping describes the body's
	// rows, so a caller that places the block at its FIRST row must know how
	// many lines to skip — otherwise the summary line maps onto body row 0 and
	// every selection, click, copy and find-highlight in the block is off by the
	// number of leading lines.
	//
	// Each renderer in the chain adds the prefix it wrote, so the total is
	// relative to the block's first line however many layers wrapped it.
	pendingOffset int
}

// take returns and clears the pending mapping and its row offset.
//
// The offset is cleared even when no mapping is pending. A renderer that wrote
// leading lines but published nothing — an answer whose body rendered empty, so
// the plain fallback ran instead — leaves its prefix in the accumulator, and
// carrying that into the next block would shift its body by a prefix it never
// wrote.
func (s *mappedMessageSink) take() (conversation.RenderedBlock, int, bool) {
	if s == nil {
		return conversation.RenderedBlock{}, 0, false
	}
	if s.pending == nil {
		s.pendingOffset = 0
		return conversation.RenderedBlock{}, 0, false
	}
	out, off := *s.pending, s.pendingOffset
	s.pending, s.pendingOffset = nil, 0
	return out, off, true
}

// renderMappedMessage renders an assistant's prose through the mapped path, and
// returns the placed rendering alongside the text.
//
// This is the transcript's selectable Markdown path: the one place where a
// reader can select, copy or search a block of prose. Other Glamour use in the
// app (panels, documents) is left alone, because those surfaces do not need a
// mapping and swapping them would change their layout for no gain.
//
// The returned RenderedBlock is what turns a later click into an offset, so it
// is returned WITH the text rather than recomputed: recomputing would parse the
// Markdown a second time and, worse, could disagree with what was drawn if
// anything between the two calls changed.
func renderMappedMessage(content string, width int, mode BlockRenderMode) (string, conversation.RenderedBlock) {
	return renderMappedBlock(content, width, mode, mappedIndent)
}

// renderMappedBlock is the ONE layout path for a block of prose.
//
// Both the live transcript renderer and the document-level
// renderConversationBlock funnel through it, which is what removes the class of
// bug the review found: two layout functions with different indent budgets, one
// of them live and one of them dead, silently disagreeing about where column 3
// is. There is exactly one answer now because there is exactly one function.
//
// indent is the leading gutter the layout reserves. The transcript passes
// mappedIndent so the caller can substitute its own gutter GLYPH on the first
// line without moving any offset; a document-level caller passes 0, because a
// block quoted out of the conversation has no transcript gutter.
//
// summaryBudget is the width BlockRenderSummary truncates to in the TRANSCRIPT
// case, where the width includes the indent. It is ignored when indent is 0
// (there the summary is bounded by width alone). The two differ on purpose: a
// summary is a fixed number of CELLS of text, and the transcript's cells start
// after the gutter.
func renderMappedBlock(
	content string, width int, mode BlockRenderMode, indent int,
) (string, conversation.RenderedBlock) {
	// Prose sits behind the transcript's gutter, so the layout indent is the
	// gutter width and the wrap budget is the matching remainder. Measuring
	// wrong here is how a line ends up under the side rail.
	opts := conversation.LayoutOptions{
		Width:       width,
		Indent:      indent,
		Breakpoints: WrapBreakpoints,
	}
	sp := conversation.ProjectMarkdown(content, conversation.MarkdownOptions{})
	if mode == BlockRenderSummary {
		sp = summarizeSpans(sp, summaryBudget(width, indent))
	}
	rows := conversation.Layout(sp, opts)
	rendered := conversation.RenderedBlock{
		BlockID: "", // the caller stamps the block identity
		// Revision is stamped from the block's own text, and this is the ONLY
		// place that can stamp it: the document path copies Block.Revision onto
		// the mapping, but the transcript renders a message straight to rows and
		// has no Block to copy from. Leaving it zero made
		// Selection.MatchesRevision compare 0 == 0 — always true — so a
		// selection's offsets were painted over text that had changed beneath it
		// instead of the highlight being dropped, which is what
		// docs/tui-interactions.md promises and what a streaming answer needs.
		Revision: blockTextRevision(sp.Text),
		Width:    width,
		Logical:  sp.Text,
		Rows:     rows,
	}
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderDisplayRow(row))
	}
	if b.Len() == 0 {
		return "", rendered
	}
	// The block's LEADING indent is emitted by the layout, and the caller
	// overwrites it on the first line with its own gutter glyph (see
	// renderFinalAnswerWithSink). Continuation rows keep the plain indent, which
	// is how a wrapped line aligns under its first line. Doing it this way — the
	// layout owning the indent, the caller owning only the first line's glyph —
	// is what keeps the mapping's cell 3 meaning screen column 3 instead of
	// needing an offset applied at every hit test.
	return b.String() + "\n", rendered
}

// summaryBudget picks the width a one-line summary is truncated to.
func summaryBudget(width, indent int) int {
	if indent > 0 {
		return contentWidth(width)
	}
	return width
}

// renderMappedFirstLine renders a mapped block's first line with the caller's
// gutter replacing the layout's leading indent.
//
// The indent and the gutter are the same width by construction (see
// renderMappedMessage), so substituting one for the other leaves every offset in
// the row unchanged — which is the whole point, and the reason this replaces the
// indent rather than being prepended to it.
func renderMappedFirstLine(line, gutter string) string {
	if gutter == "" {
		return line
	}
	// Drop the layout's leading indent span, if the row has one, and write the
	// caller's gutter in its place. The span is dropped rather than trimmed off
	// the string so that no author character can be mistaken for the indent.
	prefix := strings.Repeat(" ", mappedIndent)
	if strings.HasPrefix(line, prefix) {
		return gutter + line[len(prefix):]
	}
	return gutter + line
}

// summarizeSpans keeps only the text visible on the first display row.
//
// A collapsed block still has to map: dropping the rest of the spans here rather
// than truncating a rendered string afterwards is what keeps the mapping exact
// for what remains. A "…" is appended as chrome, so it is shown and not copied.
func summarizeSpans(sp conversation.Spans, width int) conversation.Spans {
	rows := conversation.Layout(sp, conversation.LayoutOptions{Width: width, Breakpoints: WrapBreakpoints})
	if len(rows) <= 1 {
		return sp
	}
	end := rows[0].Range.End
	if !rows[0].Range.HasText() {
		end = 0
	}
	out := conversation.Spans{Text: sp.Text[:end], Runs: nil}
	for _, r := range sp.Runs {
		if r.Range.Start >= end {
			break
		}
		if r.Range.End > end {
			r.Range.End = end
		}
		out.Runs = append(out.Runs, r)
	}
	if out.Text != sp.Text {
		// Mark the elision in the text so it is visible, as chrome so it is not
		// copied, and at the end so no existing offset moves.
		out.Text += "…"
		out.Runs = append(out.Runs, conversation.Run{
			Range: conversation.Range{Start: end, End: end + len("…")},
			Kind:  conversation.SpanSoftBreak,
		})
	}
	return out
}

// renderDisplayRow renders one row's spans with their kinds' styling.
func renderDisplayRow(row conversation.DisplayRow) string {
	var b strings.Builder
	for _, s := range row.Spans {
		if s.Text == "" {
			continue
		}
		b.WriteString(styleForSpanKind(s.Kind).Render(s.Text))
	}
	return b.String()
}

// styleForSpanKind maps a span kind to its styling.
//
// These are the SAME colours the glamour path used, so a block rendered through
// the mapped path is not visibly a different block from one rendered through the
// old one: the point of this task is to make the text addressable, not to
// restyle the transcript.
//
// An unstyled kind returns the zero Style rather than a special "no style"
// value: lipgloss's zero Style renders its input unchanged, which is exactly the
// desired behaviour, and a nil-able style would put a nil check at every call
// site to express the same thing.
func styleForSpanKind(kind conversation.SpanKind) lipgloss.Style {
	th := theme.Current()
	switch kind {
	case conversation.SpanHeading:
		return lipgloss.NewStyle().Foreground(th.AccentSecondary).Bold(true)
	case conversation.SpanEmphasis:
		return lipgloss.NewStyle().Italic(true)
	case conversation.SpanStrong:
		return lipgloss.NewStyle().Bold(true)
	case conversation.SpanCode:
		return lipgloss.NewStyle().Foreground(th.AccentTertiary)
	case conversation.SpanLink:
		return lipgloss.NewStyle().Foreground(th.StatusInfo).Underline(true)
	case conversation.SpanBullet, conversation.SpanQuote:
		return lipgloss.NewStyle().Foreground(th.AccentPrimary)
	case conversation.SpanRule, conversation.SpanTableBorder:
		return lipgloss.NewStyle().Foreground(th.BorderMuted)
	}
	// Plain, an indent, a table separator and the invisible wrap residue all
	// render unstyled. The separator is structural in the LOGICAL text and a
	// plain gap on screen: styling it would draw attention to a column boundary
	// the reader did not ask about.
	return lipgloss.NewStyle()
}
