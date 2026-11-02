package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/liveregion"
	"marshal/internal/app/tui/theme"
	"marshal/internal/diffview"
	"marshal/internal/strutil"
)

// regionView is the Model-owned view state for one bounded live region.
// offset is how far its body is scrolled back; minRows is its high-water
// mark. They travel together because they are the same concern, and
// because the live-region renderers' parameter lists are already long.
type regionView struct {
	offset  int
	minRows int
}

// marshalStyleConfig adapts glamour's dark style to the Warm Sunset
// palette: coral H1 instead of the banner-style default, violet section
// headings.
//
// The document margin is pinned to gutterWidth so agent prose starts in the
// same column as every gutter-prefixed item. Glamour's default margin is 2,
// which left prose one column to the left of the tool rows and final answers
// surrounding it.
func marshalStyleConfig(margin uint) gansi.StyleConfig {
	cfg := styles.DarkStyleConfig
	cfg.Heading.StylePrimitive.Color = strutil.Ptr("175") // violetColor
	cfg.H1.StylePrimitive = gansi.StylePrimitive{
		Color: strutil.Ptr("209"), // accentColor
		Bold:  strutil.Ptr(true),
	}
	// Dark's H2–H6 carry literal "## "/"### " prefixes that render in the
	// heading's own colour — colored text with stray hashes next to it. H1
	// is replaced wholesale above; here we keep each level's styling and
	// drop only the marker.
	for _, h := range []*gansi.StylePrimitive{
		&cfg.H2.StylePrimitive, &cfg.H3.StylePrimitive,
		&cfg.H4.StylePrimitive, &cfg.H5.StylePrimitive,
		&cfg.H6.StylePrimitive,
	} {
		h.Prefix = ""
	}
	cfg.Document.Margin = &margin
	return cfg
}

const maxRenderers = 4

// mdRenderers caches glamour renderers by wrap width; building one parses
// the full style config, which is too slow to repeat per message. The cache
// is bounded to prevent unbounded growth on repeated resizes.
type rendererKey struct {
	width  int
	margin uint
}

var (
	mdMu        sync.Mutex
	mdRenderers = map[rendererKey]*glamour.TermRenderer{}
)

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// getRenderer returns a cached glamour renderer for the requested width,
// evicting the entry farthest from width when the cache is full.
func getRenderer(width int, margin uint) *glamour.TermRenderer {
	key := rendererKey{width: width, margin: margin}
	mdMu.Lock()
	defer mdMu.Unlock()
	if r, ok := mdRenderers[key]; ok {
		return r
	}
	if len(mdRenderers) >= maxRenderers {
		var evictKey rendererKey
		var evictDist int
		first := true
		for k := range mdRenderers {
			d := abs(k.width - width)
			if first || d > evictDist {
				evictKey, evictDist, first = k, d, false
			}
		}
		delete(mdRenderers, evictKey)
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(marshalStyleConfig(margin)),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	mdRenderers[key] = r
	return r
}

// renderMarkdown renders content as ANSI-styled markdown wrapped to
// width. ok is false when glamour is unavailable (renderer construction
// or rendering failed); callers fall back to plain text.
// renderMarkdown renders prose that supplies its own gutter, so glamour adds
// no margin of its own.
func renderMarkdown(content string, width int) (out string, ok bool) {
	return renderMarkdownWithMargin(content, width, 0)
}

// renderMarkdownWithMargin renders prose that stands alone in the transcript,
// with glamour's document margin doing the indenting.
func renderMarkdownWithMargin(content string, width int, margin uint) (out string, ok bool) {
	r := getRenderer(width, margin)
	if r == nil {
		return "", false
	}
	rendered, err := r.Render(dedentMarkdown(content))
	if err != nil {
		return "", false
	}
	return wrapOverwideLines(rendered, width), true
}

// wrapOverwideLines soft-wraps any rendered line wider than width.
// glamour's WithWordWrap does not apply to fenced code blocks, so without
// this a long code line overflows the transcript — and with horizontal
// scrolling disabled its tail would be unreachable. Continuation lines
// keep the original line's indent so wrapped code still reads as code.
// ANSI styling is preserved by ansi.Wrap.
func wrapOverwideLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if ansi.StringWidth(line) <= width {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		body := strings.TrimLeft(line, " ")
		wrapWidth := width - ansi.StringWidth(indent)
		if wrapWidth < 20 {
			// Degenerate mostly-indent line: wrapping at the residual width
			// would shred it; wrap the whole line at full width instead.
			indent = ""
			body = line
			wrapWidth = width
		}
		parts := strings.Split(ansi.Wrap(body, wrapWidth, WrapBreakpoints), "\n")
		for j := range parts {
			parts[j] = indent + parts[j]
		}
		lines[i] = strings.Join(parts, "\n")
	}
	return strings.Join(lines, "\n")
}

// dedentMarkdown strips the whitespace prefix common to every non-blank line
// before the content reaches glamour.
//
// CommonMark treats any line indented four or more spaces as an indented code
// block, and a code block renders "#" and "**" literally — so a response the
// model happened to indent arrives in the transcript as raw markdown with the
// syntax showing. expandTabs makes this worse rather than better: it runs
// ahead of the markdown parser and turns a single leading tab into tabStop
// spaces, which is always past the four-space threshold.
//
// The prefix is measured in bytes and removed uniformly, so indentation
// *within* the document (nested lists, fenced code interiors) keeps its
// relative shape; only the flat outer indent that misleads the parser goes.
// Blank lines are ignored when measuring and left alone when stripping.
//
// A document whose first line is flush left has a common prefix of "" and is
// returned untouched, which is the overwhelmingly common case.
func dedentMarkdown(content string) string {
	lines := strings.Split(content, "\n")
	prefix := ""
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if !found {
			prefix, found = indent, true
			continue
		}
		// Shrink to the longest shared prefix of the two indents.
		n := min(len(prefix), len(indent))
		i := 0
		for i < n && prefix[i] == indent[i] {
			i++
		}
		prefix = prefix[:i]
		if prefix == "" {
			return content
		}
	}
	if !found || prefix == "" {
		return content
	}
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, prefix)
	}
	return strings.Join(lines, "\n")
}

// tabStop is the column interval a terminal advances a "\t" to.
const tabStop = 8

// expandTabs replaces tab characters with the spaces the terminal would
// render them as. Width math throughout the transcript goes through
// ansi.StringWidth, which counts "\t" as a single cell — but the terminal
// advances to the next multiple of tabStop, so unexpanded tabs make every
// wrap decision undercount and the rendered line ends up wider than the
// viewport, spilling past the column edge. Expanding at the point raw content
// enters the renderers keeps measurement and rendering in agreement.
//
// Input is raw content, never styled output: the column accounting has no
// notion of ANSI escapes.
func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + tabStop)
	col := 0
	for _, r := range s {
		switch r {
		case '\t':
			pad := tabStop - col%tabStop
			b.WriteString(strings.Repeat(" ", pad))
			col += pad
		case '\n':
			b.WriteRune(r)
			col = 0
		default:
			b.WriteRune(r)
			col += ansi.StringWidth(string(r))
		}
	}
	return b.String()
}

// renderPlainProse is the fallback prose treatment when markdown
// rendering fails: wrapped text with the transcript's 2-space indent.
func renderPlainProse(content string, width int) string {
	cw := contentWidth(width)
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		wrapped := ansi.Wrap(line, cw, WrapBreakpoints)
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(continuation())
			b.WriteString(wl)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// The transcript's indentation contract. Every renderer measures and indents
// through these three values and the helpers below — no literal column math.
//
// Before this contract each renderer computed its own indent, so a single turn
// staircased across five different columns (2 for prose and system notices, 3
// for tool rows and final answers, 5 for diff bodies) with no meaning attached
// to the steps. The ragged left edge was the single largest contributor to the
// transcript reading as "messy".
const (
	// gutterWidth is the width of a glyph gutter (" X "). Every top-level
	// transcript item starts here.
	gutterWidth = 3
	// continuationIndent aligns the wrapped lines of a gutter-prefixed item
	// under its first line.
	continuationIndent = gutterWidth
	// nestedBodyIndent is the column for content subordinate to a gutter row:
	// diff hunks, expanded tool results, plan bodies. It is the gutter width
	// plus the two-cell nested rail (see nestedRail).
	nestedBodyIndent = gutterWidth + 2
)

// contentWidth is the wrap budget for text that sits behind a gutter.
func contentWidth(width int) int { return max(width-gutterWidth, 1) }

// nestedContentWidth is the wrap budget for nested bodies (diffs, expanded
// results) that sit behind the nested rail.
func nestedContentWidth(width int) int { return max(width-nestedBodyIndent, 1) }

// continuation is the indent for wrapped lines of a gutter-prefixed item.
func continuation() string { return strings.Repeat(" ", continuationIndent) }

// nestedRail is the prefix for content subordinate to a gutter row. It uses
// the same ▍ rail glyph as every other structural rail in the UI rather than a
// second box-drawing idiom.
func nestedRail() string {
	return continuation() + lipgloss.NewStyle().Foreground(dimColor).Render(glyph.Rail) + " "
}

// gutterPrefix renders the hairline gutter marker: a single glyph preceded
// and followed by one space. In NO_COLOR mode the glyph survives because
// lipgloss emits no SGR when the color is NoColor{}.
func gutterPrefix(glyph string, c color.Color) string {
	return lipgloss.NewStyle().Foreground(c).Render(" " + glyph + " ")
}

func renderCodeBlock(content string, width int) string {
	if width < 1 {
		width = 1
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	// Reserve 2 cells for left/right surface padding.
	inner := max(width-2, 1)
	style := codeSurfaceStyle().Width(inner)
	var b strings.Builder
	for i, line := range strings.Split(trimmed, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(style.Render(ansi.Truncate(line, inner, "…")))
	}
	return b.String()
}

func renderFinalAnswer(msg *acpwire.WireMessage, width int) string {
	if width < 10 {
		width = 10
	}
	gutter := gutterPrefix(glyph.Rail, accentColor)
	cw := contentWidth(width)

	var b strings.Builder
	if msg.Salvaged {
		note := "salvaged"
		if msg.SalvageReason != "" {
			note += dimSeparator + msg.SalvageReason
		}
		b.WriteString(gutter)
		b.WriteString(mutedStyle().Render(note))
		b.WriteString("\n")
	}

	body, ok := renderMarkdown(msg.Content, cw)
	if !ok {
		body = renderPlainProse(msg.Content, cw)
	}
	for _, line := range strings.Split(strings.Trim(body, "\n"), "\n") {
		b.WriteString(gutter)
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func formatThinkDuration(d time.Duration) string {
	return fmt.Sprintf("%.0fs", d.Seconds())
}

// renderThinkingBox renders live reasoning as a bounded region. It returns
// nothing until reasoning text has arrived; providers that do not stream
// reasoning should not get an empty thinking panel.
func renderThinkingBox(reasoning, spinnerFrame string, elapsed time.Duration, rv regionView, width int) string {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return ""
	}
	right := ""
	if elapsed > 0 {
		right = formatElapsed(elapsed)
	}
	return liveregion.Render(liveregion.Spec{
		Glyph:      spinnerFrame,
		GlyphColor: dimColor,
		Title:      "thinking",
		Right:      right,
		Body:       strings.Split(reasoning, "\n"),
		MaxRows:    liveregion.ThinkingRows,
		Offset:     rv.offset,
		MinRows:    rv.minRows,
		Live:       true,
		Width:      width,
	}, theme.Current())
}

// renderReconnectNotice renders the live "connection lost — retrying"
// activity row as a single warning-styled line: spinner glyph + label,
// wrapped/truncated to the content width. It mirrors renderThinkingBox's
// gutter so the row lines up with every other transcript item.
func renderReconnectNotice(label, spinnerFrame string, width int) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	cw := contentWidth(width)
	var b strings.Builder
	header := spinnerLabel(spinnerFrame, label)
	for i, hl := range strings.Split(ansi.Wrap(header, cw, WrapBreakpoints), "\n") {
		if i == 0 {
			b.WriteString(gutterPrefix(glyph.Ambient, dimColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(warningStyle().Render(hl))
		b.WriteString("\n")
	}
	return b.String()
}

// renderThinkingSummary renders a finished message's captured reasoning,
// either collapsed to one line or, when expanded, as a full boxed panel
// matching renderThinkingBox's style.
// collapsedThinkingLine is the one-line "thought for 3s" marker, on the same
// gutter as every other transcript item. disclosure is glyph.DisclosureCollapsed
// when the block has reasoning text a click can reveal, or "" when there's
// nothing to expand.
func collapsedThinkingLine(duration time.Duration, disclosure string) string {
	label := fmt.Sprintf("thought for %s", formatThinkDuration(duration))
	if disclosure != "" {
		label += " " + disclosure
	}
	return gutterPrefix(glyph.Thinking, dimColor) +
		thinkingLineStyle().Render(label) + "\n"
}

func renderThinkingSummary(reasoning string, duration time.Duration, expanded bool, width int) string {
	// A thinking phase with no captured reasoning still marks its position in
	// the transcript — models that do not expose reasoning would otherwise
	// leave no trace of having thought. There is nothing to expand.
	if strings.TrimSpace(reasoning) == "" {
		return collapsedThinkingLine(duration, "")
	}
	if !expanded {
		return collapsedThinkingLine(duration, glyph.DisclosureCollapsed)
	}
	cw := contentWidth(width)
	var b strings.Builder
	b.WriteString(gutterPrefix(glyph.Thinking, dimColor))
	b.WriteString(thinkingLineStyle().Render(fmt.Sprintf("thought for %s %s", formatThinkDuration(duration), glyph.DisclosureExpanded)))
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimSpace(reasoning), "\n") {
		wrapped := ansi.Wrap(line, cw, WrapBreakpoints)
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(gutterPrefix(glyph.Rail, dimColor))
			b.WriteString(thinkingLineStyle().Render(wl))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderMessage formats one transcript entry with gutter-prefixed lines:
// user prompts get a ❯ prefix, agent prose renders as plain markdown with
// no role label, tool results render with a · gutter, system notices are
// dim. Final answers use a ▍ gutter and rich-content rendering.
func renderMessage(msg *acpwire.WireMessage, width int) string {
	// The copy is what tab expansion writes into: the wire index's payload is
	// read-only, and every content-type branch below must measure what the
	// terminal will actually render. See expandTabs.
	cp := *msg
	cp.Content = expandTabs(cp.Content)
	// Skill messages are handled before the role branches: the body
	// (ContentTypeSkillBody) is model context, not transcript content, and
	// the tag (ContentTypeSkill) renders as a compact one-line trace
	// regardless of the role it was stored under (always RoleSystem today).
	switch session.ContentType(cp.ContentType) {
	case session.ContentTypeSkillBody:
		return ""
	case session.ContentTypeSkill:
		return renderSkillTag(cp.Content, width)
	case session.ContentTypeCompaction:
		return renderCompactionMarker(cp.Content, width)
	case session.ContentTypeSubagentReport:
		return ""
	case session.ContentTypeWatchReport:
		return ""
	case session.ContentTypeSteering:
		return renderSteeringMarker(cp.Content, width)
	}
	if cp.Final {
		return renderFinalAnswer(&cp, width)
	}
	if session.Role(cp.Role) == session.RoleUser {
		return renderUserMessage(cp.Content, width)
	}
	if session.Role(cp.Role) == session.RoleSystem {
		return renderSystemNotice(cp.Content, width)
	}
	switch session.ContentType(cp.ContentType) {
	case session.ContentTypePlan:
		return renderPlanBlock(cp.Content, width)
	case session.ContentTypeToolResult:
		return renderToolResultLine(cp.Content, width)
	case session.ContentTypeCode:
		return gutterPrefix(glyph.Rail, accentColor) + renderCodeBlock(cp.Content, max(width-3, 1)) + "\n"
	default: // plain and markdown prose render identically
		return renderAgentMarkdown(cp.Content, width)
	}
}

// renderTurnSeparator marks where a new user turn begins. The transcript is
// otherwise one undifferentiated flow of gutter-prefixed lines with nothing
// to scan back to, which makes a long session hard to navigate.
//
// It is deliberately a hairline rule rather than a boxed header: the content
// is the interface, and the separator only needs to be findable, not loud.
func renderTurnSeparator(width int) string {
	w := max(width-gutterWidth, 1)
	rule := strings.Repeat("─", w)
	return continuation() + lipgloss.NewStyle().Foreground(theme.Current().BorderMuted).Render(rule) + "\n"
}

func renderUserMessage(content string, width int) string {
	gutter := gutterPrefix(glyph.User, accentColor)
	cw := contentWidth(width)
	wrapped := ansi.Wrap(content, cw, WrapBreakpoints)
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(gutter)
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(lipgloss.NewStyle().Foreground(userColor).Render(line))
		b.WriteString("\n")
	}
	return b.String()
}

func renderAgentMarkdown(content string, width int) string {
	// Glamour's document margin (pinned to gutterWidth in marshalStyleConfig)
	// provides the prose indent; wrap to the matching budget so rendered lines
	// stay inside the viewport.
	cw := contentWidth(width)
	out, ok := renderMarkdownWithMargin(content, cw, gutterWidth)
	if !ok {
		out = renderPlainProse(content, width)
	}
	return strings.Trim(out, "\n") + "\n"
}

// renderItem draws a message, a finished thinking row or a run event. d is
// the node's detail when the wire cut it; nil draws the wire's text.
func renderItem(w *acpwire.WireNode, d *acpwire.NodeDetail, expanded bool, width int) string {
	switch {
	case w == nil:
		return ""
	case w.Thinking != nil:
		return renderThinkingSummary(w.Thinking.Text, time.Duration(w.Thinking.DurationMs)*time.Millisecond, expanded, width)
	case w.Message != nil:
		msg := w.Message
		if session.ContentType(msg.ContentType) == session.ContentTypeSkillAuto {
			return renderAutoSkills(msg.Content, expanded, width)
		}
		// Narration is a distinct block type: it renders through its own
		// renderer — the same markdown as the final answer, on the ambient
		// gutter — rather than falling through to renderMessage.
		if session.ContentType(msg.ContentType) == session.ContentTypeNarration {
			return renderNarration(msg.Content, width)
		}
		// The reasoning is the whole of it when the detail holds it; the
		// wire's is capped.
		reasoning := msg.Reasoning
		if d != nil && len(d.Thinking) > 0 {
			reasoning = d.Thinking[0].Text
		}
		var b strings.Builder
		if reasoning != "" {
			b.WriteString(renderThinkingSummary(reasoning, time.Duration(msg.ThinkMs)*time.Millisecond, expanded, width))
		}
		b.WriteString(renderMessage(msg, width))
		return b.String()
	case w.RunEvent != nil:
		return renderRunEvent(w.RunEvent, d, expanded, width)
	}
	return ""
}

func renderSkillTag(name string, width int) string {
	cw := contentWidth(width)
	wrapped := ansi.Wrap("skill.load: "+name, cw, WrapBreakpoints)
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(gutterPrefix(glyph.Ambient, dimColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(mutedStyle().Render(line))
		b.WriteString("\n")
	}
	return b.String()
}

// renderSteeringMarker renders a user-typed mid-turn steering message as a
// compact dim one-line marker. The user DID type it, so an honest transcript
// shows it — but visually distinct from a real ❯ prompt (no userColor, no
// turn separator; session.IsUserTurnMessage excludes it) so a long turn's
// interventions read as asides, not new turns. Content is the enveloped form
// ("[user steering, mid-turn]: ..."); the envelope prefix is stripped here
// because the gutter + "steering:" label already say what it is.
func renderSteeringMarker(content string, width int) string {
	text := strings.TrimPrefix(content, "[user steering, mid-turn]: ")
	cw := contentWidth(width)
	wrapped := ansi.Wrap("steering: "+text, cw, WrapBreakpoints)
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(gutterPrefix(glyph.Ambient, dimColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(mutedStyle().Render(line))
		b.WriteString("\n")
	}
	return b.String()
}

// renderCompactionMarker renders a context compaction as a single dim
// rule-like line. Compaction silently discards most of the transcript, so
// the marker exists to make the seam visible: without it a long session
// looks like the model spontaneously forgot its earlier work.
func renderCompactionMarker(text string, width int) string {
	label := " " + text + " "
	rule := strings.Repeat("─", max(width-lipgloss.Width(label)-3, 0))
	return dimStyle().Render("─"+label+rule) + "\n"
}

func renderSystemNotice(content string, width int) string {
	cw := contentWidth(width)
	wrapped := ansi.Wrap(content, cw, WrapBreakpoints)
	var b strings.Builder
	for i, line := range strings.Split(wrapped, "\n") {
		if i == 0 {
			b.WriteString(gutterPrefix(glyph.Ambient, dimColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(mutedStyle().Render(line))
		b.WriteString("\n")
	}
	return b.String()
}

// renderQueuedMessages renders the F16 steering queue as a footer
// beneath the live transcript so the user can see what they typed
// while the agent was working. width is informational (no wrapping
// here yet — each queued line stays short in practice).
func renderQueuedMessages(q []string, width int) string {
	if len(q) == 0 {
		return ""
	}
	gutter := gutterPrefix(glyph.Ambient, dimColor)
	var b strings.Builder
	for _, msg := range q {
		b.WriteString(gutter)
		b.WriteString(mutedStyle().Render("queued: " + strutil.Truncate(msg, max(width-12, 1), false)))
		b.WriteString("\n")
	}
	return b.String()
}

func renderToolResultLine(content string, width int) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return ""
	}
	gutter := gutterPrefix(glyph.Ambient, dimColor)
	cw := contentWidth(width)
	var b strings.Builder
	firstWrapped := ansi.Wrap(strings.TrimSpace(lines[0]), cw, WrapBreakpoints)
	firstLines := strings.Split(firstWrapped, "\n")
	for i, wl := range firstLines {
		if i == 0 {
			b.WriteString(gutter)
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(mutedStyle().Render(wl))
		b.WriteString("\n")
	}
	for _, line := range lines[1:] {
		wrapped := ansi.Wrap(line, cw, WrapBreakpoints)
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(continuation())
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func renderPlanBlock(content string, width int) string {
	gutter := gutterPrefix(glyph.Ambient, dimColor)
	var b strings.Builder
	b.WriteString(gutter)
	b.WriteString(mutedStyle().Render("plan"))
	b.WriteString("\n")
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		wrapped := ansi.Wrap(line, nestedContentWidth(width), WrapBreakpoints)
		for _, wl := range strings.Split(wrapped, "\n") {
			b.WriteString(nestedRail())
			b.WriteString(wl)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderActiveToolCall shows the in-flight tool as a spinner line with a
// hairline gutter — no border. Command tools get a $ line and, when a
// sandbox backend is active, an isolation-status line, both dim-indented
// under the gutter. When the tool has streamed output, the last 6 lines
// are rendered dim-indented beneath the command line.
func renderActiveToolCall(t *acpwire.WireTool, sb session.SandboxInfo, allowNetwork bool, spinnerFrame string, now time.Time, expanded bool, width int) string {
	if t == nil || t.Running == nil {
		return ""
	}
	running := t.Running
	elapsed := now.Sub(acpwire.Time(running.StartedAt))
	if elapsed < 0 {
		elapsed = 0
	}
	head := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", t.Display, formatElapsed(elapsed)))
	gutter := gutterPrefix(glyph.Running, accentColor)
	cw := contentWidth(width)
	headerWrapped := ansi.Wrap(head, cw, WrapBreakpoints)
	headerLines := strings.Split(headerWrapped, "\n")
	var b strings.Builder
	for i, hl := range headerLines {
		if i == 0 {
			b.WriteString(gutter + toolBulletStyle().Render(hl))
		} else {
			b.WriteString(continuation() + toolBulletStyle().Render(hl))
		}
		b.WriteString("\n")
	}
	if t.Name == "shell.run" || t.Name == "test.run" {
		cmdLine := "$ " + running.Args
		cmdWrapped := ansi.Wrap(cmdLine, cw, WrapBreakpoints)
		for _, wl := range strings.Split(cmdWrapped, "\n") {
			b.WriteString(continuation())
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
		if iso := sandboxIsolationText(sb, allowNetwork); iso != "" {
			isoWrapped := ansi.Wrap(iso, cw, WrapBreakpoints)
			for _, wl := range strings.Split(isoWrapped, "\n") {
				b.WriteString(continuation())
				b.WriteString(mutedStyle().Render(wl))
				b.WriteString("\n")
			}
		}
	} else if running.Args != "" {
		argsWrapped := ansi.Wrap(running.Args, cw, WrapBreakpoints)
		for _, wl := range strings.Split(argsWrapped, "\n") {
			b.WriteString(continuation())
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
	}
	if running.Output != "" {
		lines := strings.Split(strings.TrimRight(running.Output, "\n"), "\n")
		const tail = 6
		if !expanded && len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		for _, line := range lines {
			wrapped := ansi.Wrap(line, cw, WrapBreakpoints)
			for _, wl := range strings.Split(wrapped, "\n") {
				b.WriteString(continuation())
				b.WriteString(mutedStyle().Render(wl))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// callersDetail is the blast-radius line under a row that changed a symbol,
// wrapped to w and styled: what references the symbol. It is a settled
// one-time addition to a settled row, so the height change it causes is
// acceptable and it is not part of any live-region body. Nothing renders when
// there is no cached result — a no-server negative, an empty reference set,
// or a language without LSP all leave the row exactly as it would be without
// this feature.
func callersDetail(callers []string, w int) []string {
	if len(callers) == 0 {
		return nil
	}
	shown, extra := callers, 0
	if len(shown) > maxCallersShown {
		extra = len(shown) - maxCallersShown
		shown = shown[:maxCallersShown]
	}
	line := fmt.Sprintf("↳ %d callers: %s", len(callers), strings.Join(shown, ", "))
	if extra > 0 {
		line += fmt.Sprintf(" +%d", extra)
	}
	var out []string
	for _, wl := range strings.Split(ansi.Wrap(line, w, WrapBreakpoints), "\n") {
		out = append(out, mutedStyle().Render(wl))
	}
	return out
}

// toolBody is an expanded call's body under its subject, styled and fitted to
// w: its diff or its result, then its error and arguments when it failed. d
// is the call's detail when the wire cut it; nil draws the wire's text.
func toolBody(name string, c acpwire.WireCall, d *acpwire.CallDetail, w int) []string {
	output, args := c.Output, c.Args
	if d != nil {
		args = compactJSON(d.Args)
		// An empty wire output means the call had no result: d.Output falls
		// back to the summary, which is not a result.
		if output != "" {
			output = d.Output
			if isDiffTool(name) {
				output = d.Diff
			}
		}
	}
	var out []string
	if isDiffTool(name) && output != "" {
		for _, f := range splitDiffFiles(output) {
			stat := fmt.Sprintf("+%d −%d", f.added, f.removed)
			if f.path != "" {
				stat = f.path + " " + stat
			}
			out = append(out, dimStyle().Render(stat))
			rendered := diffview.Render(f.raw, diffview.Options{
				Width:     w,
				Mode:      diffview.ModeAuto,
				Highlight: true,
			})
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				if line == "" {
					continue
				}
				// Clip rather than wrap: diffview does not hard-clamp to the
				// width it is given, and a wrapped continuation carries no
				// rail, so an over-wide line breaks the chrome.
				out = append(out, ansi.Truncate(line, w, "…"))
			}
		}
	}
	// Non-diff tools with captured output (agent.run is the motivating
	// case — its ResultContent holds the subagent's report) render it,
	// wrapped rather than clipped since the content is prose, not code
	// lines.
	if !isDiffTool(name) && output != "" {
		lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
		elided := 0
		if len(lines) > maxExpandedResultLines {
			elided = len(lines) - maxExpandedResultLines
			lines = lines[:maxExpandedResultLines]
		}
		for _, line := range lines {
			for _, wl := range strings.Split(ansi.Wrap(line, w, WrapBreakpoints), "\n") {
				out = append(out, dimStyle().Render(wl))
			}
		}
		if elided > 0 {
			out = append(out, dimStyle().Render(fmt.Sprintf("… %d more lines", elided)))
		}
	}
	// Failed calls carry no ResultContent; expanding one shows the full
	// error, the exit code when the tool reported one, and the call args.
	// Each detail line wraps at w — the raw args JSON used to run straight
	// off the terminal edge.
	if c.Error != "" {
		detail := "error: " + c.Error
		if c.ExitCode != nil {
			detail += fmt.Sprintf(" (exit code %d)", *c.ExitCode)
		}
		lines := []string{detail}
		if args != "" {
			lines = append(lines, "args: "+args)
		}
		for _, line := range lines {
			for _, wl := range strings.Split(ansi.Wrap(line, w, WrapBreakpoints), "\n") {
				out = append(out, dimStyle().Render(wl))
			}
		}
	}
	return out
}

// compactJSON is s with its JSON whitespace removed, or s itself when it does
// not parse.
func compactJSON(s string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}

const (
	// maxExpandedResultLines caps the body an expanded (ctrl+g) non-diff
	// tool row renders — a subagent report gets the same budget as one
	// file's diff.
	maxExpandedResultLines = 12
)

// diffFile is one file's slice of a unified diff, with precomputed
// add/remove counts for the +N −M stat header.
type diffFile struct {
	path    string
	raw     string
	added   int
	removed int
}

// splitDiffFiles splits a unified diff into per-file chunks on "--- "/
// "+++ " header pairs. Content with no file headers is returned as a
// single anonymous chunk so the caller still renders it.
func splitDiffFiles(diff string) []diffFile {
	var files []diffFile
	var cur *diffFile
	flush := func() {
		if cur != nil {
			files = append(files, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "--- ") {
			flush()
			cur = &diffFile{}
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimPrefix(line, "+++ ")
			path = strings.TrimPrefix(path, "b/")
			if idx := strings.IndexByte(path, '\t'); idx >= 0 {
				path = path[:idx]
			}
			cur.path = path
		}
		cur.raw += line + "\n"
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++ "):
			cur.added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--- "):
			cur.removed++
		}
	}
	flush()
	if len(files) == 0 {
		files = []diffFile{{raw: diff}}
	}
	return files
}

func isDiffTool(name string) bool {
	return name == "file.write_patch" || name == "file.edit" || name == "patch.apply"
}

func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
}

func riskText(tc *session.PendingToolCall) string {
	if tc.Risk != "" {
		return tc.Risk
	}
	return tc.Reason
}

// sandboxIsolationText renders an honest one-line description of the active
// sandbox backend for shell/test commands. It combines the backend's
// capability snapshot with the configured AllowNetwork flag so the user
// never sees a false "isolated" claim (restricted mode cannot block network
// cross-platform). Returns "" for backends with no isolation story (e.g.
// passthrough).
func sandboxIsolationText(sb session.SandboxInfo, allowNetwork bool) string {
	switch sb.Backend {
	case "", "passthrough":
		return ""
	case "container":
		if sb.NetworkIsolation && !allowNetwork {
			return "sandbox: container · network blocked"
		}
		return "sandbox: container · network allowed"
	case "restricted":
		if sb.NetworkIsolation {
			return "sandbox: restricted"
		}
		return "sandbox: restricted · network not isolated"
	default:
		return "sandbox: " + sb.Backend
	}
}

func renderApprovalPanel(tc *session.PendingToolCall, sb session.SandboxInfo, allowNetwork bool, width int, why ...approvalWhy) string {
	var w approvalWhy
	if len(why) > 0 {
		w = why[0]
	}
	tc = shownToolCall(tc)
	var b strings.Builder
	headParts := []string{}
	if tc.Name == "shell.run" {
		headParts = append(headParts, tc.Command)
	} else {
		headParts = append(headParts, tc.Name)
	}
	headParts = append(headParts, riskText(tc))
	if iso := sandboxIsolationText(sb, allowNetwork); iso != "" {
		headParts = append(headParts, iso)
	}
	// The head line carries the command/name, risk, and sandbox isolation.
	// It is wrapped to the content width so a long risk or reason (riskText
	// falls back to Reason) cannot overflow the panel.
	cw := max(width-gutterWidth, 1)
	for i, hl := range strings.Split(ansi.Wrap(strings.Join(headParts, dimSeparator), cw, WrapBreakpoints), "\n") {
		if i == 0 {
			b.WriteString(gutterPrefix(glyph.Warning, warningColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(warningStyle().Render(hl))
		b.WriteString("\n")
	}

	for _, line := range []string{w.ownerLine(tc), w.whyLine()} {
		if line == "" {
			continue
		}
		for i, wl := range strings.Split(ansi.Wrap(line, cw, WrapBreakpoints), "\n") {
			if i == 0 {
				b.WriteString(gutterPrefix(" ", warningColor))
			} else {
				b.WriteString(continuation())
			}
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
	}

	// Reason is a separate fact from Risk: riskText above shows whichever
	// is set (preferring Risk), but both can be present and both matter —
	// the cost-consent prompt's sentence is the reason, not the risk label.
	if tc.Reason != "" {
		for i, wl := range strings.Split(ansi.Wrap(tc.Reason, cw, WrapBreakpoints), "\n") {
			if i == 0 {
				b.WriteString(gutterPrefix(" ", warningColor))
			} else {
				b.WriteString(continuation())
			}
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
	}

	b.WriteString(gutterPrefix(" ", warningColor))
	b.WriteString(approvalActionRow(0))
	b.WriteString("\n")
	return b.String()
}

// approvalActions are the approval choices with their mnemonic keys.
var approvalActions = []struct{ key, label string }{
	{"a", "allow"},
	{"A", "always"},
	{"s", "session"},
	{"e", "edit"},
	{"d", "deny"},
}

// approvalActionRow renders the choices with the armed one on the selection
// background and every mnemonic key shown. It used to be a space-aligned run
// of uniformly muted words whose armed entry was marked only by a leading ▸,
// with the keys not surfaced at all.
func approvalActionRow(armed int) string {
	th := theme.Current()
	parts := make([]string, 0, len(approvalActions))
	for i, a := range approvalActions {
		label := a.key + " " + a.label
		if i == armed {
			parts = append(parts, lipgloss.NewStyle().
				Foreground(th.FGEmphasis).
				Background(th.BGSelection).
				Bold(true).
				Render(" "+glyph.Running+" "+label+" "))
			continue
		}
		parts = append(parts, mutedStyle().Render("   "+label+" "))
	}
	return strings.Join(parts, "")
}

func renderQuestionPanel(q acpwire.QuestionRequest, width int) string {
	if len(q.Questions) == 0 {
		return ""
	}
	gutter := gutterPrefix(glyph.Question, violetColor)
	indent := continuation()
	// contentWidth already leaves room for the 3-cell gutter.
	cw := contentWidth(width)
	questionStyle := lipgloss.NewStyle().Foreground(violetColor).Bold(true)
	var b strings.Builder
	for _, qs := range q.Questions {
		for j, line := range strings.Split(ansi.Wrap(qs.Question, cw, WrapBreakpoints), "\n") {
			if j == 0 {
				b.WriteString(gutter)
			} else {
				b.WriteString(indent)
			}
			b.WriteString(questionStyle.Render(line))
			b.WriteString("\n")
		}
		if len(qs.Options) > 0 {
			labels := make([]string, 0, len(qs.Options))
			for _, o := range qs.Options {
				labels = append(labels, o.Label)
			}
			b.WriteString(indent)
			b.WriteString(mutedStyle().Render("(" + strings.Join(labels, " / ") + ")"))
			b.WriteString("\n")
		}
	}
	// No rail: the console draws the gutter around this section and its
	// header carries the keys.
	return b.String()
}

// renderWelcomeBanner prints the one-time startup identity as plain
// transcript lines — brand chrome pays rent once, at startup, not as a
// persistent title bar.
func renderWelcomeBanner(width int) string {
	_ = width
	dot := lipgloss.NewStyle().Foreground(accentColor).Render(glyph.Brand)
	brand := lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("marshal")
	tagline := mutedStyle().Render("local-friendly coding agent")
	cta := mutedStyle().Render("Type a question, or " + lipgloss.NewStyle().Bold(true).Render("/") + " for commands.")
	return "  " + dot + " " + brand + dimSeparator + tagline + "\n\n  " + cta + "\n\n"
}
