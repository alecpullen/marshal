// internal/app/tui/find.go — the find UI over the live conversation
package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/tui/conversation"
)

// This file owns the find experience: the query, the results, which one the
// reader is on, and where they go back to.
//
// It is a READING state, in the same family as the selection and the reading
// anchor, and it follows the same rule: opening it changes nothing about the
// session, closing it puts the reader back, and nothing it does reaches the
// agent. A find that submitted anything would be a very expensive search, and
// one that did not restore the reader's place would make the feature something
// people avoid using.
//
// Three pieces of state are separate on purpose, because collapsing any two of
// them produces a bug that looks like the feature working:
//
//   - query is what the reader typed. It is kept as typed so the input can echo
//     it; the folded form used for matching lives in the query object.
//   - matches is the result list, rebuilt whenever the conversation or the query
//     changes. It is rebuilt WHOLESALE rather than patched: a match is a
//     position in a projection that may have been reprojected at a new revision,
//     and patching one would leave stale offsets alongside fresh ones.
//   - current is an index into matches. It is re-resolved BY IDENTITY after a
//     rebuild (block, then range) rather than kept as a number, because a
//     rebuild can insert a match above the reader's and a bare index would then
//     name a different hit.
//
// The entry anchor is a fourth thing, and it is here rather than reusing
// readingAnchor for a specific reason: the reading anchor is CONSTANTLY
// rewritten — every jump calls captureReadingAnchor so the next refresh can
// restore — so closing find by consulting it would return the reader to the
// last match they visited, not to where they were when they opened the search.

// findState is the find UI's own state.
type findState struct {
	// open reports whether the find input owns the keyboard.
	open bool
	// query is what the reader typed, as typed.
	query string
	// matches are the current results, in reading order.
	matches []conversation.FindMatch
	// current indexes matches. It is meaningful only while matches is nonempty.
	current int
	// entryAnchor is where the reader was when find opened. Closing with Esc
	// returns here; accepting with Enter does not.
	entryAnchor conversation.Anchor
	// entryFollow records whether the reader was following the bottom when find
	// opened, so closing can restore that too. A reader who was following must
	// not be left pinned to a line when they close a search they opened from the
	// live end.
	entryFollow bool
	// truncated records whether the searched text included a capped block, so
	// the status can state the scope instead of implying it covered everything.
	truncated bool
}

// openFind opens the find input with an initial query.
//
// The entry anchor is captured HERE, before anything moves, and it is captured
// even when the reader is following: a following reader closing the search must
// go back to following, and storing an empty anchor plus the follow flag is what
// expresses that.
func (m *Model) openFind(query string) {
	if !m.find.open {
		m.find.entryFollow = m.viewportFollow
		if m.viewportFollow {
			// A following reader has no position worth preserving; the flag
			// above is what restores them.
			m.find.entryAnchor = conversation.Anchor{}
		} else {
			m.captureReadingAnchor()
			m.find.entryAnchor = m.readingAnchor
		}
	}
	m.find.open = true
	m.setFindQuery(query)
}

// closeFind closes the input and returns the reader to where they were.
//
// Esc's meaning: "not that, put me back". The entry anchor is restored as the
// reading anchor AND resolved immediately, so the reader sees the restore happen
// rather than only finding out on the next reflow.
func (m *Model) closeFind() {
	if !m.find.open {
		return
	}
	m.find.open = false
	m.find.query = ""
	m.find.matches = nil
	m.find.current = 0
	// Restore the position captured at open, not wherever the jumps ended up.
	m.readingAnchor = m.find.entryAnchor
	m.viewportFollow = m.find.entryFollow
	if m.viewportFollow {
		m.viewport.GotoBottom()
		return
	}
	if m.readingAnchor.Block != "" {
		m.restoreReadingAnchor()
	}
}

// acceptFind closes the input and LEAVES the reader on the match they are on.
//
// Enter's meaning: "this is the one, I will stay here". The difference from Esc
// is the whole reason both keys exist — a reader who found the line they wanted
// must not be sent back to where they started.
func (m *Model) acceptFind() {
	if !m.find.open {
		return
	}
	// The accepted position is wherever the reader is now. Recording it as the
	// anchor is what makes the next reflow hold it: without this the anchor
	// would still be the pre-jump position and the first resize would send them
	// back.
	m.find.open = false
	m.find.query = ""
	if len(m.find.matches) > 0 {
		m.captureReadingAnchor()
		// Accepting takes the reader OFF follow: they have chosen a line, and a
		// later stream of output must not drag them away from it.
		m.viewportFollow = false
	}
	m.find.matches = nil
	m.find.current = 0
}

// setFindQuery replaces the query and re-runs the search.
//
// The current match is carried across by identity, so typing another character
// does not throw the reader back to the first hit — which is the difference
// between a search that narrows and one that restarts on every keystroke.
func (m *Model) setFindQuery(query string) {
	if query == m.find.query {
		return
	}
	m.find.query = query
	m.refreshFind()
}

// refreshFind rebuilds the results for the current query.
//
// It is idempotent and cheap to call: it is what the model calls after a
// transcript rebuild, so an open search stays in step with the conversation as
// output streams in.
func (m *Model) refreshFind() {
	if !m.find.open {
		return
	}
	if m.findIndex == nil {
		m.findIndex = conversation.NewSearchIndex(0)
	}
	previous, hadPrevious := m.currentFindMatch()
	doc := m.conversationDocument()
	m.find.matches = conversation.FindInDocument(doc, conversation.NewFindQuery(m.find.query), m.findIndex)
	m.find.truncated = conversation.DocumentTruncated(doc)

	if len(m.find.matches) == 0 {
		m.find.current = 0
		return
	}
	if hadPrevious {
		// Tier 1: the exact hit survived. Its INDEX may have changed — new
		// streaming output can add matches above it — which is precisely why
		// the index is not what is carried.
		for i, match := range m.find.matches {
			if match.Block == previous.Block && match.Range == previous.Range {
				m.find.current = i
				return
			}
		}
		// Tier 2: the exact range is gone but the block still matches. A query
		// that got LONGER matches a longer span, so its range is necessarily
		// different from the one the reader was on: tier 1 can never fire for
		// someone still typing, and without this tier every keystroke would
		// throw them back to the first hit in the conversation.
		for i, match := range m.find.matches {
			if match.Block == previous.Block {
				m.find.current = i
				return
			}
		}
	}
	// Tier 3: neither survived. Clamp rather than reset, because a reader whose
	// hit is genuinely gone is still better served by the nearest position than
	// by the first match in the transcript.
	m.find.current = clampFindIndex(m.find.current, len(m.find.matches))
}

// currentFindMatch returns the match the reader is on.
func (m Model) currentFindMatch() (conversation.FindMatch, bool) {
	if len(m.find.matches) == 0 {
		return conversation.FindMatch{}, false
	}
	i := clampFindIndex(m.find.current, len(m.find.matches))
	return m.find.matches[i], true
}

// clampFindIndex keeps an index inside a result list.
func clampFindIndex(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// nextFindMatch advances to the next result, wrapping.
//
// Wrapping rather than stopping: a reader at the last hit pressing "next" means
// "go round again", and a key that did nothing there would read as broken.
func (m *Model) nextFindMatch() int {
	if len(m.find.matches) == 0 {
		return 0
	}
	m.find.current = (clampFindIndex(m.find.current, len(m.find.matches)) + 1) % len(m.find.matches)
	return m.find.current
}

// prevFindMatch steps back a result, wrapping.
func (m *Model) prevFindMatch() int {
	if len(m.find.matches) == 0 {
		return 0
	}
	n := len(m.find.matches)
	m.find.current = (clampFindIndex(m.find.current, n) - 1 + n) % n
	return m.find.current
}

// gotoFindMatch scrolls the conversation to the current match.
//
// It scrolls to the SCROLL identity rather than the match's own block, because
// a match inside a collapsed group names a member that is not a top-level block:
// the group is what is on screen, and scrolling to the member's identity would
// find nothing.
func (m *Model) gotoFindMatch() bool {
	match, ok := m.currentFindMatch()
	if !ok {
		return false
	}
	row, ok := m.blockStartRow(match.Scroll)
	if !ok {
		// The block is not rendered at this width — it is real, but nothing
		// scrolls to it. Reporting false keeps the caller from claiming a jump
		// that did not happen.
		return false
	}
	// The reader has chosen a line, so following ends. Without this the next
	// refresh would drag them back to the bottom and the jump would look like it
	// had failed.
	m.viewportFollow = false
	// The match's first line is the row its block starts on plus the row the
	// match's offset falls on, so a hit deep inside a long block lands on the
	// hit rather than on the block's first line.
	target := row
	if span, ok := m.mappedBlockSpan(match.Block); ok {
		if blockRow, ok := span.rendered.RowForOffset(match.Range.Start); ok {
			target = row + blockRow
		}
	}
	m.viewport.SetYOffset(clampOffset(target, m.viewport.Height(), m.viewport.TotalLineCount()))
	// Record the destination as the reading anchor, so a later reflow keeps the
	// reader on the match rather than on whatever the anchor said before.
	m.captureReadingAnchor()
	return true
}

// findHighlight returns the cells a match covers on a transcript row, so the
// renderer can mark it.
//
// It works in TRANSCRIPT rows like selectionHighlight, and through the SAME
// mapping the offsets came from: a highlight derived from anything else would
// drift from the offsets by exactly the amount the two disagree, and it would
// do so silently. Marking requires the row to be in the match's own rendered
// block, which is what keeps a marker from being drawn on an unrelated line.
func (m Model) findHighlight(row int) (startCell, endCell int, ok bool) {
	if !m.find.open || len(m.find.matches) == 0 {
		return 0, 0, false
	}
	match, hasMatch := m.currentFindMatch()
	if !hasMatch {
		return 0, 0, false
	}
	span, hasSpan := m.mappedBlockSpan(match.Block)
	if !hasSpan {
		return 0, 0, false
	}
	blockRow := row - span.blockRow
	if blockRow < 0 || blockRow >= len(span.rendered.Rows) {
		return 0, 0, false
	}
	return span.rendered.HighlightRange(blockRow, match.Range.Start, match.Range.End)
}

// findMatchHighlights returns every match's cells on a transcript row, so the
// renderer can mark all of them while distinguishing the current one.
//
// It is separate from findHighlight because the two answer different questions:
// "which cells are a hit" is a property of the results, and "which hit is the
// reader on" is a property of the cursor. A renderer that conflated them could
// only ever show one match, and a reader would have no way to see how many
// others a query matched.
func (m Model) findMatchHighlights(row int) []findMatchSpan {
	if !m.find.open || len(m.find.matches) == 0 {
		return nil
	}
	current, hasCurrent := m.currentFindMatch()
	var out []findMatchSpan
	for _, match := range m.find.matches {
		span, ok := m.mappedBlockSpan(match.Block)
		if !ok {
			continue
		}
		blockRow := row - span.blockRow
		if blockRow < 0 || blockRow >= len(span.rendered.Rows) {
			continue
		}
		start, end, ok := span.rendered.HighlightRange(blockRow, match.Range.Start, match.Range.End)
		if !ok || end <= start {
			continue
		}
		isCurrent := hasCurrent && match.Block == current.Block && match.Range == current.Range
		out = append(out, findMatchSpan{start: start, end: end, current: isCurrent})
	}
	return out
}

// findMatchSpan is one match's cells on one row.
type findMatchSpan struct {
	start, end int
	// current distinguishes the hit the reader is on from the others, so the
	// renderer can mark it differently. A reader needs to see both: which lines
	// matched, and which one "next" is counting from.
	current bool
}

// findStatus renders the search indicator for the status line.
//
// It reports "" when find is closed, which is what the status line uses to
// decide whether to show the segment at all.
//
// Three statements, and they are deliberately different: a count and a position
// while there are results, "no match" when there are none, and a scope note when
// the searched text included a capped block. "0/0" is never emitted — a reader
// told they are on result 0 of 0 has been given a number where an answer was
// owed.
func (m Model) findStatus() string {
	if !m.find.open {
		return ""
	}
	var b strings.Builder
	switch {
	case m.find.query == "":
		b.WriteString("find: type to search")
	case len(m.find.matches) == 0:
		b.WriteString(fmt.Sprintf("find %q: no match", m.find.query))
	default:
		i := clampFindIndex(m.find.current, len(m.find.matches)) + 1
		b.WriteString(fmt.Sprintf("find %q: %d/%d", m.find.query, i, len(m.find.matches)))
	}
	if m.find.truncated {
		b.WriteString(" · searched captured output, which may itself be truncated")
	}
	return b.String()
}

// findInputPrompt is the placeholder the input shows while find owns the
// keyboard.
const findInputPrompt = "find: "

// findInputTitle is the label the input carries, so the reader can see which
// surface owns their keystrokes.
const findInputTitle = "Find"

// resetFindIndex drops the search's projection cache.
//
// It is called on a session switch, for the same reason the render cache is
// reset there: a block identity is scoped per session, so carrying entries
// across a switch would serve one conversation's projection for another's
// block.
func (m *Model) resetFindIndex() {
	if m.findIndex != nil {
		m.findIndex.Reset()
	}
}
