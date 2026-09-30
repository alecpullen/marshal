package conversation

import (
	"unicode"
)

// This file owns finding text in the conversation the reader is looking at.
//
// It is deliberately pure and deliberately narrow: it searches the READABLE
// projection of blocks handed to it, in memory, in the conversation currently
// on screen. Archived generations live in /history search, which reads the
// database; a "find" that silently ranged over every past session would move
// the reader somewhere they did not ask to go.
//
// Two decisions shape everything below.
//
// The first is WHAT is searched: a block's READABLE text, not its source. The
// reader is looking at a projection (a list bullet, a resolved entity, a
// table's cells in a row), so a match has to be findable the way it was read
// and its offsets have to name the text on screen. Searching the Markdown
// source would find `**bold**` but not `bold`, and would report offsets into a
// string nothing is rendered from.
//
// The second is WHAT IS NOT searched. Text a RENDERER inserted is not the
// author's, so a query for `•` must not report a hit on every list item; and
// content the transcript deliberately does not draw — a skill body, a
// subagent's report — is not reachable by scrolling, so offering to jump to it
// would be a lie, and would surface instructions the transcript hides on
// purpose.

// maxFindMatches bounds the result list.
//
// The bound exists because the alternative is unbounded: a pasted log with one
// word repeated a hundred thousand times would otherwise build a hundred
// thousand results on every keystroke, which is the shape of bug that makes the
// UI feel broken rather than slow.
const maxFindMatches = 500

// searchIndexMaxBytes caps the projected TEXT the index retains, alongside the
// entry count.
//
// The entry cap alone bounds the cache by the number of blocks in the
// conversation, which is not the quantity that costs: 4096 entries of a
// streaming answer's full text each is 4096 × the block size, and the memory a
// long session spends on cached projections would then be sized for the
// largest blocks it happened to contain rather than for anything it needed.
// The ceiling trades one re-projection per keystroke, for a block that has
// itself been evicted, against a cache whose footprint a session cannot
// outgrow. 4 MiB holds the readable projection of a very large visible
// conversation; a block evicted for size is simply re-projected next time it
// is searched, exactly as it would be after an LRU eviction.
const searchIndexMaxBytes = 4 << 20

// FindQuery is what the reader typed, kept with the form used to search.
//
// It is a type rather than a bare string so that the fold and the echo cannot
// drift apart: the fold is the search key and the string is what the UI shows,
// and a caller that rebuilt either one would have to remember which.
type FindQuery struct {
	raw  string
	fold []rune
}

// NewFindQuery keeps a query's text and computes its search form.
func NewFindQuery(raw string) FindQuery {
	return FindQuery{raw: raw, fold: foldRunes(raw)}
}

// String returns the query as typed, for the UI.
func (q FindQuery) String() string { return q.raw }

// Empty reports whether there is nothing to search for.
func (q FindQuery) Empty() bool { return q.raw == "" || len(q.fold) == 0 }

// FindMatch is one occurrence of the query in the conversation.
type FindMatch struct {
	// Block is the identity of the block the match was found in. For a member
	// of a collapsed group this is the MEMBER, so a caller can open the exact
	// thing that matched rather than the run it was folded into.
	Block BlockID
	// Scroll is the identity of the top-level block the reader must be shown to
	// see this match. It differs from Block exactly when Block is a child of a
	// collapsed group, and it is a separate field rather than a fallback
	// because a caller that scrolled to the wrong one of the two would move the
	// reader somewhere with the match off screen and no way to tell why.
	Scroll BlockID
	// Range is the matched byte range in the block's readable text.
	Range Range
}

// FindInDocument returns every occurrence of the query, in reading order.
//
// index is optional and may be nil; it only exists so a keystroke does not
// reproject every block in a long conversation.
func FindInDocument(doc *Document, q FindQuery, index *SearchIndex) []FindMatch {
	if doc == nil || q.Empty() {
		return nil
	}
	needle := q.fold
	var out []FindMatch
	for _, b := range doc.Blocks() {
		out = appendFindMatches(out, b.ID, b, needle, index)
		if len(out) >= maxFindMatches {
			break
		}
		// A collapsed group's members are searched as blocks of their own:
		// their output is still in the conversation, and a query that matched
		// inside one must be able to open it.
		for _, child := range b.Children {
			out = appendFindMatches(out, b.ID, child, needle, index)
			if len(out) >= maxFindMatches {
				break
			}
		}
		if len(out) >= maxFindMatches {
			break
		}
	}
	if len(out) > maxFindMatches {
		out = out[:maxFindMatches]
	}
	return out
}

// appendFindMatches adds a block's matches, naming scrollTo as the block to
// scroll to.
func appendFindMatches(out []FindMatch, scrollTo BlockID, b Block, needle []rune, index *SearchIndex) []FindMatch {
	if b.Hidden || b.ID == "" || b.Text == "" {
		return out
	}
	text, runs := projectBlock(b, index)
	if text == "" {
		return out
	}
	hay := foldRunesWithMap(text)
	if len(hay.fold) < len(needle) {
		return out
	}
	for i := 0; i+len(needle) <= len(hay.fold); {
		if !runesEqualAt(hay.fold, needle, i) {
			i++
			continue
		}
		start := hay.offsets[i]
		end := hay.offsets[i+len(needle)]
		i += len(needle)
		// A match that spans decoration is not a phrase anyone read. The
		// bullet in "- one" is the renderer's, so "• one" is on screen and is
		// not searchable text; the same rule stops a query from matching across
		// a table's cell separator.
		if spansDecoration(runs, start, end) {
			continue
		}
		out = append(out, FindMatch{
			Block:  b.ID,
			Scroll: scrollTo,
			Range:  Range{Start: start, End: end},
		})
		if len(out) >= maxFindMatches {
			return out
		}
	}
	return out
}

// spansDecoration reports whether a byte range covers any text the renderer
// generated.
//
// It is a rule about PROVENANCE, so it uses Decorative rather than Chrome: a
// list bullet and a table separator are both generated, and a query that
// included either would match text the author never wrote.
func spansDecoration(runs []Run, start, end int) bool {
	for _, r := range runs {
		if !r.Kind.Decorative() {
			continue
		}
		if r.Range.Start < end && start < r.Range.End {
			return true
		}
	}
	return false
}

// projectBlock returns a block's readable text and the runs that style it.
//
// A Markdown block is projected through the same parser the renderer uses, so a
// match's offsets name the text that is actually laid out. A block whose text is
// not Markdown (tool output, a run event) is searched as written, because
// projecting it would resolve syntax the author never meant as syntax — a tool
// that printed a table would have its columns rewritten.
func projectBlock(b Block, index *SearchIndex) (string, []Run) {
	if !searchableMarkdown(b) {
		return b.Text, nil
	}
	if index != nil {
		return index.spans(b)
	}
	sp := ProjectMarkdown(b.Text, MarkdownOptions{})
	return sp.Text, sp.Runs
}

// searchableMarkdown reports whether a block's text may be projected as
// Markdown before it is searched.
//
// The rule the whole function serves is that the projection must match what
// the reader is LOOKING at, and the transcript's renderers are the authority
// on that. A message is rendered through the same Markdown projection here
// (renderMessageWithSink), so a match's offsets highlight the text that was
// drawn. A thinking entry, a subagent card, a run event and a background
// job's exit are NOT: renderThinkingSummary, renderSubagentCard,
// renderRunEvent and renderJobExit wrap, gutter and truncate the RAW text
// with no Markdown pass, so "- bullet" is shown as "- bullet" and searching a
// projected "•" would find a character that is nowhere on screen — while the
// "- bullet" the reader can see would be unfindable. Those kinds therefore
// stay searchable AS WRITTEN.
func searchableMarkdown(b Block) bool {
	switch b.Kind {
	case BlockMessage:
		// A user's prompt is Markdown to the same parser; an assistant's answer
		// is the projection the reader read. But captured output and patches
		// are verbatim by source, and the message renderer draws them that way.
		return b.Source != SourceOutput && b.Source != SourcePatch
	default:
		// Everything else — a thinking entry, a subagent card, a run event, a
		// job exit, a tool call's output, a collapsed group's members — is
		// drawn raw, and a projection that rewrote it would not match what the
		// reader sees.
		return false
	}
}

// foldedText is a case-folded string with the mapping back to the original.
//
// The mapping is the whole point: a case-insensitive search has to report
// offsets into the text being displayed and highlighted, and an offset into a
// folded copy is a position in a different string. Where a fold is not
// length-preserving the positions stop being related at all, which is why the
// fold below refuses to expand a rune.
type foldedText struct {
	fold []rune
	// offsets has one more entry than fold: offsets[i] is where folded rune i
	// begins in the original text, and the last entry is the original's length.
	offsets []int
}

// foldRunes folds a string for searching.
func foldRunes(s string) []rune { return foldRunesWithMap(s).fold }

// foldRunesWithMap folds a string and records where each folded rune came from.
//
// The fold is SIMPLE case folding — unicode.ToLower, which is a rune-to-rune
// function — and that is the load-bearing choice, not an incidental one. Simple
// folding maps every source rune to exactly one folded rune, so the folded
// string has the same rune count as the source and folded rune i always came
// from source rune i. That is what makes the offset map exact.
//
// The tempting alternatives both break it. strings.ToLower performs FULL
// folding in some locales, and Go's unicode.SpecialCase likewise, turning one
// rune into several: "İ" folds to "i" plus a combining dot, two runes for one.
// The moment a fold invents a rune, every offset after it is off by the number
// the fold invented — and the error is invisible in English, which is exactly
// how it survives to production.
//
// The offsets are BYTE offsets even though the map is by rune index, because
// they have to name positions in the original string: a folded rune's byte
// width can differ from the source rune's ("K" is one byte, "K" in a wider
// encoding is not), so the byte offset is recorded from the walk rather than
// derived from the index.
func foldRunesWithMap(s string) foldedText {
	fold := make([]rune, 0, len(s))
	offsets := make([]int, 0, len(s)+1)
	for i, r := range s {
		fold = append(fold, unicode.ToLower(r))
		offsets = append(offsets, i)
	}
	return foldedText{fold: fold, offsets: append(offsets, len(s))}
}

// runesEqualAt reports whether needle appears in hay at position i.
func runesEqualAt(hay, needle []rune, i int) bool {
	if i < 0 || i+len(needle) > len(hay) {
		return false
	}
	for j, r := range needle {
		if hay[i+j] != r {
			return false
		}
	}
	return true
}

// SearchIndex caches the projections search reads.
//
// Projecting Markdown is the expensive half of a search, and a keystroke
// re-searches the whole conversation: without this, typing a query costs one
// Goldmark parse per block per character. The cache is keyed on (identity,
// revision, width) — the same triple the render cache uses — because a block
// whose content changed must be reprojected or a match would name offsets into
// text that is no longer on screen.
type SearchIndex struct {
	entries map[BlockID]searchIndexEntry
	// order is the LRU list of identities, oldest first.
	order []BlockID
	max   int
}

// searchIndexEntry is one cached projection.
type searchIndexEntry struct {
	revision int
	text     string
	runs     []Run
}

// NewSearchIndex returns an index holding at most max projections.
func NewSearchIndex(max int) *SearchIndex {
	if max <= 0 {
		max = 4096
	}
	return &SearchIndex{entries: map[BlockID]searchIndexEntry{}, max: max}
}

// Len reports how many projections are cached, for a test asserting the bound.
func (s *SearchIndex) Len() int {
	if s == nil {
		return 0
	}
	return len(s.entries)
}

// text returns a block's readable text, projecting it if needed.
func (s *SearchIndex) text(b Block) string {
	text, _ := s.spans(b)
	return text
}

// spans returns a block's readable text and runs, projecting it if needed.
func (s *SearchIndex) spans(b Block) (string, []Run) {
	if e, ok := s.entries[b.ID]; ok && e.revision == b.Revision {
		s.touch(b.ID)
		return e.text, e.runs
	}
	sp := ProjectMarkdown(b.Text, MarkdownOptions{})
	s.store(b.ID, searchIndexEntry{revision: b.Revision, text: sp.Text, runs: sp.Runs})
	return sp.Text, sp.Runs
}

// store caches a projection, evicting the least recently used entries until
// both bounds hold: the entry count, and the total bytes of retained text.
//
// The byte ceiling exists for the same reason the entry cap does — a cache
// whose footprint a session cannot outgrow — and it is enforced the same way:
// the LEAST recently used projection is the one dropped, so the text the
// reader is most likely to search again survives longest. A lone entry larger
// than the ceiling itself is still kept (evicting everything else to hold
// nothing would not be a bound, it would be an empty cache).
func (s *SearchIndex) store(id BlockID, e searchIndexEntry) {
	if id == "" {
		return
	}
	if _, exists := s.entries[id]; !exists {
		for len(s.entries) >= s.max {
			s.evictOldest()
		}
		// A fresh projection's bytes count before it lands, so the ceiling
		// governs what RETAINS rather than what arrived.
		var total int
		for _, existing := range s.entries {
			total += len(existing.text)
		}
		for total+len(e.text) > searchIndexMaxBytes && len(s.order) > 0 {
			oldest := s.order[0]
			total -= len(s.entries[oldest].text)
			s.evictOldest()
		}
	}
	s.entries[id] = e
	s.touch(id)
}

// touch moves an identity to the most-recent end of the order.
func (s *SearchIndex) touch(id BlockID) {
	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.order = append(s.order, id)
}

// evictOldest drops the least recently used projection.
func (s *SearchIndex) evictOldest() {
	if len(s.order) == 0 {
		return
	}
	oldest := s.order[0]
	s.order = s.order[1:]
	delete(s.entries, oldest)
}

// Reset empties the index, which is what a session switch or a rebuild at a new
// width requires.
func (s *SearchIndex) Reset() {
	if s == nil {
		return
	}
	s.entries = map[BlockID]searchIndexEntry{}
	s.order = nil
}

// ReadableText returns a block's readable text without styling, for a caller
// that only needs the characters.
func ReadableText(b Block) string {
	if b.Hidden {
		return ""
	}
	if !searchableMarkdown(b) {
		return b.Text
	}
	return ProjectMarkdown(b.Text, MarkdownOptions{}).Text
}

// DocumentTruncated reports whether any searched block's captured text was
// capped, so the find UI can state the scope it actually searched instead of
// implying it covered everything.
//
// The flag is read from the block, where the ADAPTER set it from the tool's own
// report. It is deliberately not derived here by looking for the word
// "truncated" in the text: that would be a second, prose-based source of truth
// for a fact the tool already states structurally, and it would misfire on a
// message that merely discusses truncation.
func DocumentTruncated(doc *Document) bool {
	if doc == nil {
		return false
	}
	for _, b := range doc.Blocks() {
		if b.Hidden {
			continue
		}
		if b.Truncated {
			return true
		}
		for _, child := range b.Children {
			if child.Truncated {
				return true
			}
		}
	}
	return false
}
