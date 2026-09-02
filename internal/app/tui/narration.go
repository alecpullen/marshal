package tui

import (
	"strings"

	"marshal/internal/app/tui/glyph"
)

// renderNarration renders the prose a model emitted alongside its tool
// calls, as context for the rows beneath it.
//
// It renders through the same glamour pipeline as the final answer
// (renderMarkdown), which is often where the important information lives:
// a heading, an emphasis, a list, or the question a turn ends on. Only the
// gutter separates the two: narration keeps the ambient "·" so the reader
// can still tell a mid-turn aside from the answer that closed the turn.
// Nothing is capped or collapsed — a disclosure marker on a question the
// agent is asking would hide the thing the user needs to answer.
func renderNarration(content string, width int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	// Expand tabs before any width math so glamour and wrapOverwideLines
	// measure what the terminal will actually render. See expandTabs in
	// transcript.go.
	content = expandTabs(content)

	gutter := gutterPrefix(glyph.Ambient, dimColor)
	body, ok := renderMarkdown(content, contentWidth(width))
	if !ok {
		body = renderPlainProse(content, width)
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.Trim(body, "\n"), "\n") {
		b.WriteString(gutter)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
