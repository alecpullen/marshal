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

// This file turns a projected block into styled screen text, and caches that by
// the things that can change it.
//
// The rendering itself is deliberately thin: the projection (conversation's
// markdown parser) already decided what the text IS and which spans are which
// kind, so all that is left is choosing a colour per kind and joining the rows.
// Doing layout here as well — the obvious alternative — is what produces the two
// divergences this task exists to remove: text that wraps differently from the
// offsets a click maps to, and a copy that contains the renderer's own padding.
//
// The cache is keyed on everything that can change the output. Missing a key
// does not produce a wrong render, it produces a STALE one, which is worse: it
// looks correct until the terminal is resized or the theme changes, and then it
// stays wrong until the block's text changes. So the key is written as a struct
// rather than a string, and its fields are the four inputs the function actually
// reads.

// blockRenderKey identifies one rendering of one block.
//
// The fields are the inputs renderConversationBlock reads, and nothing else.
// Rendering mode is here because the transcript renders some blocks as a
// one-line summary and others in full; expansion is here because a collapsed
// group and an expanded one differ without their text changing; revision is here
// so a block whose content changed is reparsed even at the same width.
type blockRenderKey struct {
	block    conversation.BlockID
	revision int
	width    int
	mode     BlockRenderMode
	// themeTier is part of the key because the same text under a different
	// colour tier renders to different escapes, and a cache that ignored it
	// would keep 256-colour escapes after the theme dropped to a terminal that
	// cannot show them.
	themeTier theme.ColorTier
}

// BlockRenderMode selects how much of a block is rendered.
type BlockRenderMode int

const (
	// BlockRenderFull renders the whole block.
	BlockRenderFull BlockRenderMode = iota
	// BlockRenderSummary renders a collapsed block as one line.
	BlockRenderSummary
)

// conversationRenderCache is a bounded cache of rendered blocks.
//
// It is bounded because a long session has thousands of blocks and every resize
// multiplies them: an unbounded cache is a slow leak that only shows up in the
// sessions that matter most. Eviction is least-recently-used, which matches how
// a transcript is read — the newest blocks are the ones being re-rendered — and
// the bound is generous enough that ordinary use never evicts.
type conversationRenderCache struct {
	entries map[blockRenderKey]string
	// order is the LRU list of keys, oldest first.
	order []blockRenderKey
	max   int
}

// newConversationRenderCache returns a cache holding at most max renderings.
func newConversationRenderCache(max int) *conversationRenderCache {
	if max <= 0 {
		max = 512
	}
	return &conversationRenderCache{entries: map[blockRenderKey]string{}, max: max}
}

// Len reports how many renderings are held, for a test asserting boundedness.
func (c *conversationRenderCache) Len() int {
	if c == nil {
		return 0
	}
	return len(c.entries)
}

// get returns a cached rendering, marking it as recently used.
func (c *conversationRenderCache) get(k blockRenderKey) (string, bool) {
	if c == nil {
		return "", false
	}
	v, ok := c.entries[k]
	if !ok {
		return "", false
	}
	c.touch(k)
	return v, true
}

// put stores a rendering, evicting the least recently used entry when full.
func (c *conversationRenderCache) put(k blockRenderKey, v string) {
	if c == nil {
		return
	}
	if _, exists := c.entries[k]; !exists && len(c.entries) >= c.max {
		c.evictOldest()
	}
	c.entries[k] = v
	c.touch(k)
}

// touch moves a key to the most-recent end of the order.
func (c *conversationRenderCache) touch(k blockRenderKey) {
	for i, existing := range c.order {
		if existing == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, k)
}

// evictOldest drops the least recently used entry.
func (c *conversationRenderCache) evictOldest() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = c.order[1:]
	delete(c.entries, oldest)
}

// reset empties the cache, which is what a session switch requires: a
// blockRenderKey names a block by its transcript identity, and identities are
// scoped per session, so carrying entries across a switch would serve one
// conversation's rendering for another's block.
func (c *conversationRenderCache) reset() {
	if c == nil {
		return
	}
	c.entries = map[blockRenderKey]string{}
	c.order = nil
}

// renderConversationBlock renders one block to styled screen text at a width.
//
// The returned text has no trailing newline: the caller joins blocks, and a
// per-block newline is how a transcript acquires a blank line between every pair
// of items.
func (m *Model) renderConversationBlock(block conversation.Block, width int, mode BlockRenderMode) string {
	if width <= 0 {
		return ""
	}
	key := blockRenderKey{
		block:     block.ID,
		revision:  block.Revision,
		width:     width,
		mode:      mode,
		themeTier: theme.Current().Tier,
	}
	if m.convRender == nil {
		m.convRender = newConversationRenderCache(0)
	}
	if cached, ok := m.convRender.get(key); ok {
		return cached
	}
	out := renderConversationBlock(block, width, mode)
	m.convRender.put(key, out)
	return out
}

// renderConversationBlock is the pure rendering, with no cache and no model.
//
// It is separated from the cached wrapper so it can be tested directly, and so
// the cache's correctness is the only thing the wrapper has to get right.
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
	sp := conversation.ProjectMarkdown(block.Text, conversation.MarkdownOptions{})
	if mode == BlockRenderSummary {
		sp = summarizeSpans(sp, width)
	}
	rows := conversation.Layout(sp, conversation.LayoutOptions{
		Width:       width,
		Breakpoints: WrapBreakpoints,
	})
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderDisplayRow(row))
	}
	return b.String()
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
}

// take returns and clears the pending mapping.
func (s *mappedMessageSink) take() (conversation.RenderedBlock, bool) {
	if s == nil || s.pending == nil {
		return conversation.RenderedBlock{}, false
	}
	out := *s.pending
	s.pending = nil
	return out, true
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
	// Prose sits behind the transcript's gutter, so the layout indent is the
	// gutter width and the wrap budget is the matching remainder. Measuring
	// wrong here is how a line ends up under the side rail.
	opts := conversation.LayoutOptions{
		Width:       width,
		Indent:      mappedIndent,
		Breakpoints: WrapBreakpoints,
	}
	sp := conversation.ProjectMarkdown(content, conversation.MarkdownOptions{})
	if mode == BlockRenderSummary {
		sp = summarizeSpans(sp, contentWidth(width))
	}
	rows := conversation.Layout(sp, opts)
	rendered := conversation.RenderedBlock{
		BlockID: "", // the caller stamps the block identity
		Width:   width,
		Logical: sp.Text,
		Rows:    rows,
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
