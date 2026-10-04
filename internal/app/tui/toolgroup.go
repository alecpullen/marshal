package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/tools/registry"
)

// bulletIndent is the width of the "  – " prefix that precedes each bullet's
// text: two cells for the dash gutter plus the en-dash and its trailing
// space. Bullet continuation lines are indented gutter + bulletIndent so
// wrapped text stays aligned under the first line's bullet text, matching
// how continuation() aligns heading text under its gutter.
const bulletIndent = 4

// toolTarget returns the short human-facing subject of a tool call — the
// path it read, the command it ran, the query it searched for.
func toolTarget(event registry.AuditEvent) string {
	if s := symbolSubject(event); s != "" {
		return s
	}
	if len(event.FilesChanged) > 0 {
		return event.FilesChanged[0]
	}
	if len(event.Args) == 0 {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(event.Args, &args); err != nil {
		return ""
	}
	for _, key := range []string{"path", "command", "query", "name"} {
		if s, ok := args[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// renderToolGroup renders a run of same-tool audit events. Collapsed it is
// one line, the plural tool name, the count and as many targets as fit: a run
// that grows while a step is live then only ever changes one row, instead of
// adding a bullet per call. Expanded it is a heading line followed by bullet
// points, one per call, showing each target and result summary.
func renderToolGroup(events []registry.AuditEvent, expanded bool, width int) string {
	head := fmt.Sprintf("%s: ×%d", pluralizeToolName(events[0].ToolName), len(events))
	gutter := gutterPrefix(toolCategoryGlyph(events[0].ToolName), dimColor)
	var b strings.Builder

	if !expanded {
		targets := make([]string, 0, len(events))
		for _, ev := range events {
			if t := toolTarget(ev); t != "" {
				targets = append(targets, t)
			}
		}
		if len(targets) > 0 {
			head += dimSeparator + strings.Join(targets, ", ")
		}
		b.WriteString(gutter)
		b.WriteString(statusOkStyle().Render(ansi.Truncate(head, max(width-gutterWidth, 1), "…")))
		b.WriteString("\n")
		return b.String()
	}

	// Heading line + indented bullet list. Wrapped continuation
	// lines are re-indented behind the continuation gutter so they never
	// start in column 0.
	b.WriteString(gutter)
	for i, hl := range strings.Split(ansi.Wrap(head, max(width-3, 1), WrapBreakpoints), "\n") {
		if i > 0 {
			b.WriteString(continuation())
		}
		b.WriteString(statusOkStyle().Render(hl))
		b.WriteString("\n")
	}
	for _, ev := range events {
		line := toolTarget(ev)
		if ev.ResultSummary != "" {
			if line != "" {
				line += dimSeparator
			}
			line += ev.ResultSummary
		}
		// Wrap the bullet text at the width left after the gutter and the
		// bullet prefix; continuation lines indent gutter + bulletIndent so
		// they sit under the bullet text (the first line's "  – " prefix is
		// written separately, not part of the wrap).
		for i, bl := range strings.Split(ansi.Wrap(line, max(width-gutterWidth-bulletIndent, 1), WrapBreakpoints), "\n") {
			if i == 0 {
				b.WriteString(gutter)
				b.WriteString("  – ")
			} else {
				b.WriteString(continuation())
				b.WriteString(strings.Repeat(" ", bulletIndent))
			}
			b.WriteString(mutedStyle().Render(bl))
			b.WriteString("\n")
		}
	}
	return b.String()
}
