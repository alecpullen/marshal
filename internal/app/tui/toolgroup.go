package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// bulletIndent is the width of the "  – " prefix that precedes each bullet's
// text: two cells for the dash gutter plus the en-dash and its trailing
// space. Bullet continuation lines are indented gutter + bulletIndent so
// wrapped text stays aligned under the first line's bullet text, matching
// how continuation() aligns heading text under its gutter.
const bulletIndent = 4

// transcriptEntry is either a single transcript item or a collapsed run of
// consecutive audit events sharing one tool name.
type transcriptEntry struct {
	Item  *session.TranscriptItem // non-nil for ungrouped entries
	Group []registry.AuditEvent   // len >= 2 for a collapsed run
}

// groupTranscript collapses runs of consecutive, mergeable audit items that
// share a tool name into single entries. Collapsing is a readability
// feature: the events most worth reading — failures, edits, hooked calls —
// never join a run (see mergeableAuditEvent).
func groupTranscript(items []session.TranscriptItem) []transcriptEntry {
	entries := make([]transcriptEntry, 0, len(items))
	for i := range items {
		item := &items[i]
		ev := mergeableAuditEvent(item)
		if ev == nil {
			entries = append(entries, transcriptEntry{Item: item})
			continue
		}
		if n := len(entries); n > 0 && len(entries[n-1].Group) > 0 && entries[n-1].Group[0].ToolName == ev.ToolName {
			entries[n-1].Group = append(entries[n-1].Group, *ev)
			continue
		}
		entries = append(entries, transcriptEntry{Item: item, Group: []registry.AuditEvent{*ev}})
	}
	// A lone event renders as the original item, not a group of one.
	// A group of 2+ events has Item set to nil (the group owns rendering).
	for i := range entries {
		if len(entries[i].Group) == 1 {
			entries[i].Group = nil
		} else if len(entries[i].Group) >= 2 {
			entries[i].Item = nil
		}
	}
	return entries
}

// mergeableAuditEvent returns the audit event for an item that may join a
// collapsed run, or nil when the item must render on its own: non-audit
// items, diff tools (edits always render their own diff), failures, and
// calls with hook metadata.
func mergeableAuditEvent(item *session.TranscriptItem) *registry.AuditEvent {
	if item.Kind != session.KindAudit || item.Audit == nil {
		return nil
	}
	ev := item.Audit
	if isDiffTool(ev.ToolName) || ev.Error != "" || len(ev.Hooks) > 0 {
		return nil
	}
	return ev
}

// toolTarget returns the short human-facing subject of a tool call — the
// path it read, the command it ran, the query it searched for.
func toolTarget(event registry.AuditEvent) string {
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

// renderToolGroup renders a collapsed run of same-tool audit events as a
// heading line (plural tool name + count) followed by bullet points — one
// per call — showing each target and result summary. When the terminal is
// very narrow (width < 40) and the group is not expanded, it collapses to
// a single line to avoid excessive wrapping.
func renderToolGroup(events []registry.AuditEvent, expanded bool, width int) string {
	head := fmt.Sprintf("%s: ×%d", pluralizeToolName(events[0].ToolName), len(events))
	gutter := gutterPrefix(toolCategoryGlyph(events[0].ToolName), dimColor)
	var b strings.Builder

	// Narrow terminals: collapse to a single line unless expanded.
	if width < 40 && !expanded {
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
		b.WriteString(statusOkStyle().Render(ansi.Truncate(head, max(width-3, 1), "")))
		b.WriteString("\n")
		return b.String()
	}

	// Default: heading line + indented bullet list. Wrapped continuation
	// lines are re-indented behind the continuation gutter so they never
	// start in column 0.
	b.WriteString(gutter)
	for i, hl := range strings.Split(ansi.Wrap(head, max(width-3, 1), ""), "\n") {
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
		for i, bl := range strings.Split(ansi.Wrap(line, max(width-gutterWidth-bulletIndent, 1), ""), "\n") {
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
