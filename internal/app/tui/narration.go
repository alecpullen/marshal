package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
)

// narrationCollapsedRows caps how many display rows a narration block
// occupies before it collapses behind a disclosure marker.
//
// A long turn can produce dozens of these — one per model iteration — so
// without a cap a single verbose model could flood the scrollback. Three
// rows is enough for the sentence or two that narration usually is, and
// bounds the worst case at roughly one row per iteration.
const narrationCollapsedRows = 3

// renderNarration renders the prose a model emitted alongside its tool
// calls, as context for the rows beneath it.
//
// It uses the ambient gutter and muted text: narration is secondary,
// non-actionable content, and must not compete with the final answer,
// which owns the ▍ gutter and full markdown rendering.
//
// The content is rendered as plain prose rather than markdown. Narration is
// speech, not a document; running it through glamour would let a stray '#'
// or '*' restyle a line for no reason.
func renderNarration(content string, expanded bool, width int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	// Expand tabs before any width math so ansi.Wrap measures what the
	// terminal will actually render. See expandTabs in transcript.go.
	content = expandTabs(content)
	cw := contentWidth(width)

	// Wrap first, then cap — the cap is on display rows, not logical lines.
	// Capping logical lines would let one long sentence occupy ten rows.
	var rows []string
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, strings.Split(ansi.Wrap(line, cw, WrapBreakpoints), "\n")...)
	}
	if len(rows) == 0 {
		return ""
	}

	marker := ""
	if len(rows) > narrationCollapsedRows {
		if expanded {
			marker = " " + glyph.DisclosureExpanded
		} else {
			marker = " " + glyph.DisclosureCollapsed
			rows = rows[:narrationCollapsedRows]
		}
	}

	// Truncate the last visible row to leave room for the marker, then
	// append it. Appending after wrapping (the old approach) could
	// overflow the viewport when the row fills its full budget; re-wrapping
	// could add a row beyond the cap. Truncating guarantees the row +
	// marker fits within cw and the row count stays at the cap.
	if marker != "" {
		markerW := ansi.StringWidth(marker)
		last := rows[len(rows)-1]
		if w := ansi.StringWidth(last); w+markerW > cw {
			last = ansi.Truncate(last, max(cw-markerW, 1), "")
		}
		rows[len(rows)-1] = last + marker
	}

	var b strings.Builder
	for i, row := range rows {
		if i == 0 {
			b.WriteString(gutterPrefix(glyph.Ambient, dimColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(mutedStyle().Render(row))
		b.WriteString("\n")
	}
	return b.String()
}
