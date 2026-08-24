package tui

import (
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

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/liveregion"
	"marshal/internal/app/tui/theme"
	"marshal/internal/diffview"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

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
// viewport, spilling under the side rail. Expanding at the point raw content
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

func renderFinalAnswer(msg session.Message, width int) string {
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
func renderThinkingBox(reasoning, spinnerFrame string, elapsed time.Duration, offset, width int) string {
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
		Offset:     offset,
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
func renderMessage(msg session.Message, width int) string {
	// Expand tabs once, here, so every content-type branch below measures
	// what the terminal will actually render. See expandTabs.
	msg.Content = expandTabs(msg.Content)
	// Skill messages are handled before the role branches: the body
	// (ContentTypeSkillBody) is model context, not transcript content, and
	// the tag (ContentTypeSkill) renders as a compact one-line trace
	// regardless of the role it was stored under (always RoleSystem today).
	switch msg.ContentType {
	case session.ContentTypeSkillBody:
		return ""
	case session.ContentTypeSkill:
		return renderSkillTag(msg.Content, width)
	case session.ContentTypeCompaction:
		return renderCompactionMarker(msg.Content, width)
	}
	if msg.Final {
		return renderFinalAnswer(msg, width)
	}
	if msg.Role == session.RoleUser {
		return renderUserMessage(msg.Content, width)
	}
	if msg.Role == session.RoleSystem {
		return renderSystemNotice(msg.Content, width)
	}
	switch msg.ContentType {
	case session.ContentTypePlan:
		return renderPlanBlock(msg.Content, width)
	case session.ContentTypeToolResult:
		return renderToolResultLine(msg.Content, width)
	case session.ContentTypeCode:
		return gutterPrefix(glyph.Rail, accentColor) + renderCodeBlock(msg.Content, max(width-3, 1)) + "\n"
	default: // plain and markdown prose render identically
		return renderAgentMarkdown(msg.Content, width)
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

func renderTranscriptItem(item session.TranscriptItem, detailExpanded bool, spinnerFrame string, offset int, callers []string, width int) string {
	switch item.Kind {
	case session.KindThinking:
		if item.Thinking == nil {
			return ""
		}
		return renderThinkingSummary(item.Thinking.Text, item.Thinking.Duration, detailExpanded, width)
	case session.KindAudit:
		if item.Audit == nil {
			return ""
		}
		return renderCompletedToolCall(*item.Audit, detailExpanded, callers, width)
	case session.KindMessage:
		if item.Message == nil {
			return ""
		}
		// Narration is a distinct block type, not prose: it renders through
		// its own collapsible renderer rather than falling through to
		// renderMessage, which has no notion of expansion.
		if item.Message.ContentType == session.ContentTypeNarration {
			return renderNarration(item.Message.Content, detailExpanded, width)
		}
		var b strings.Builder
		if item.Message.Reasoning != "" {
			b.WriteString(renderThinkingSummary(item.Message.Reasoning, item.Message.ThinkDuration, detailExpanded, width))
		}
		b.WriteString(renderMessage(*item.Message, width))
		return b.String()
	case session.KindSubagent:
		if item.Subagent == nil {
			return ""
		}
		return renderSubagentCard(*item.Subagent, detailExpanded, spinnerFrame, offset, width)
	case session.KindRunEvent:
		if item.RunEvent == nil {
			return ""
		}
		return renderRunEvent(*item.RunEvent, detailExpanded, width)
	case session.KindJobExit:
		if item.JobExit == nil {
			return ""
		}
		return renderJobExit(*item.JobExit, detailExpanded, width)
	}
	return ""
}

// renderJobExit renders a finished background job as one settled transcript
// row, expanding to its buffered output. It is history, so it is flat: the
// tint marks activity, not severity.
func renderJobExit(e session.JobExit, expanded bool, width int) string {
	g, style := glyph.Job, statusOkStyle()
	gutterColor := theme.Current().FGMuted
	if e.ExitCode != 0 {
		g, style = glyph.Error, errorStyle()
		gutterColor = theme.Current().StatusError
	}
	parts := []string{e.ID, e.Command, fmt.Sprintf("exit %d", e.ExitCode)}
	if e.Duration > 0 {
		parts = append(parts, formatElapsed(e.Duration))
	}
	head := strings.Join(parts, dimSeparator)
	if e.Output != "" {
		if expanded {
			head += " " + glyph.DisclosureExpanded
		} else {
			head += " " + glyph.DisclosureCollapsed
		}
	}
	cw := contentWidth(width)
	var b strings.Builder
	for i, hl := range strings.Split(ansi.Wrap(head, cw, WrapBreakpoints), "\n") {
		if i == 0 {
			b.WriteString(gutterPrefix(g, gutterColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(style.Render(hl))
		b.WriteString("\n")
	}
	if expanded && e.Output != "" {
		for _, line := range strings.Split(strings.TrimRight(e.Output, "\n"), "\n") {
			for _, wl := range strings.Split(ansi.Wrap(line, cw, WrapBreakpoints), "\n") {
				b.WriteString(continuation())
				b.WriteString(mutedStyle().Render(wl))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// subagentTailBudget is how many logical tail lines to pull from a running
// child. liveregion windows them to the visible rows, so this only needs to
// be generous enough that there is material to scroll back through.
const subagentTailBudget = 40

// renderSubagentCard renders a subagent as a bounded, tinted region while
// it runs, and as one flat settled row once it finishes.
//
// A finished card is deliberately not routed through liveregion: it is
// history, it is a single row, and pushing it through a component named for
// live content would mean adding a "make it look settled" flag to that
// component's API.
func renderSubagentCard(v session.SubagentView, expanded bool, spinnerFrame string, offset, width int) string {
	th := theme.Current()
	live := v.Status == session.SubagentRunning

	// Duration: elapsed while running, total once finished.
	var dur string
	if live {
		dur = formatElapsed(max(time.Since(v.StartedAt), 0))
	} else if !v.EndedAt.IsZero() {
		dur = formatElapsed(max(v.EndedAt.Sub(v.StartedAt), 0))
	}

	// Metrics move off the identity row onto the meta row, so the label
	// reads as a name rather than as the head of a long sentence.
	var meta []string
	if v.Model != "" {
		if v.Provider != "" {
			meta = append(meta, fmt.Sprintf("%s @ %s", v.Model, v.Provider))
		} else {
			meta = append(meta, v.Model)
		}
	}
	if v.Fallback {
		meta = append(meta, "(fallback)")
	}
	if v.SalvagedReason != "" {
		meta = append(meta, fmt.Sprintf("(salvaged: %s)", v.SalvagedReason))
	}
	if v.TokensUsed > 0 {
		meta = append(meta, strutil.CompactTokens(v.TokensUsed)+" tok")
	}
	if v.ToolCalls > 0 {
		meta = append(meta, fmt.Sprintf("%d tool calls", v.ToolCalls))
	}
	if live && v.CurrentTool != "" {
		meta = append(meta, v.CurrentTool)
	}

	var out string
	if live {
		g := spinnerFrame
		if g == "" {
			g = glyph.Running
		}
		spec := liveregion.Spec{
			Glyph:      g,
			GlyphColor: th.AccentPrimary,
			Title:      v.Label,
			Right:      dur,
			Meta:       strings.Join(meta, dimSeparator),
			Body:       subagentTailLines(v.Child, subagentTailBudget),
			MaxRows:    liveregion.SubagentRows,
			Offset:     offset,
			Live:       true,
			Width:      width,
		}
		if v.Child != nil {
			spec.Footer = "ctrl+f to drill in"
		}
		out = liveregion.Render(spec, th)
	} else {
		out = settledSubagentRow(v, expanded, dur, meta, width)
	}

	if expanded && v.Summary != "" {
		cw := contentWidth(width)
		var b strings.Builder
		b.WriteString(out)
		for _, line := range strings.Split(ansi.Wrap(v.Summary, cw, WrapBreakpoints), "\n") {
			b.WriteString(continuation())
			b.WriteString(mutedStyle().Render(line))
			b.WriteString("\n")
		}
		out = b.String()
	}
	return out
}

// settledSubagentRow renders a finished subagent as one flat gutter row:
// label, outcome, metrics, duration, and a disclosure marker when there is
// a summary to expand.
func settledSubagentRow(v session.SubagentView, expanded bool, dur string, meta []string, width int) string {
	g, gutterColor, style := glyph.OK, theme.Current().FGMuted, statusOkStyle()
	word := "done"
	if v.Status == session.SubagentFailed {
		g, gutterColor, style = glyph.Error, theme.Current().StatusError, errorStyle()
		word = "failed"
	}
	parts := append([]string{v.Label, word}, meta...)
	if dur != "" {
		parts = append(parts, dur)
	}
	head := strings.Join(parts, dimSeparator)
	if v.Summary != "" {
		if expanded {
			head += " " + glyph.DisclosureExpanded
		} else {
			head += " " + glyph.DisclosureCollapsed
		}
	}
	cw := contentWidth(width)
	var b strings.Builder
	for i, hl := range strings.Split(ansi.Wrap(head, cw, WrapBreakpoints), "\n") {
		if i == 0 {
			b.WriteString(gutterPrefix(g, gutterColor))
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(style.Render(hl))
		b.WriteString("\n")
	}
	return b.String()
}

// subagentTailLines returns up to n dim continuation lines summarising
// what a running subagent is currently doing. It delegates to the shared
// session implementation so the TUI card and agent.output read the same
// tail source and cannot drift.
func subagentTailLines(child *session.State, n int) []string {
	if child == nil {
		return nil
	}
	return child.SubagentActivityTail(n)
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
func renderActiveToolCall(atc session.ActiveToolCall, sb session.SandboxInfo, allowNetwork bool, spinnerFrame string, now time.Time, expanded bool, width int) string {
	elapsed := now.Sub(atc.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	head := spinnerLabel(spinnerFrame, fmt.Sprintf("%s · %s", DisplayToolName(atc.Name), formatElapsed(elapsed)))
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
	if atc.Name == "shell.run" || atc.Name == "test.run" {
		cmdLine := "$ " + atc.Args
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
	} else if atc.Args != "" {
		argsWrapped := ansi.Wrap(atc.Args, cw, WrapBreakpoints)
		for _, wl := range strings.Split(argsWrapped, "\n") {
			b.WriteString(continuation())
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
	}
	if atc.Output != "" {
		lines := strings.Split(strings.TrimRight(atc.Output, "\n"), "\n")
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

func renderCompletedToolCall(event registry.AuditEvent, expanded bool, callers []string, width int) string {
	g := toolCategoryGlyph(event.ToolName)
	style := statusOkStyle()
	if event.Error != "" {
		g = glyph.Error
		style = errorStyle()
	}
	// Glyph and label share the state colour in both directions; success used
	// to pair a muted glyph with a green label while errors made both red.
	gutterColor := theme.Current().FGMuted
	if event.Error != "" {
		gutterColor = theme.Current().StatusError
	}
	gutter := gutterPrefix(g, gutterColor)
	head := DisplayToolName(event.ToolName)
	shellRow := isShellFamily(event.ToolName)
	// summaryDupesCommand tracks whether the head already carries the
	// command text. Only then should we suppress the ResultSummary —
	// background jobs ("started background job <id>") and killed commands
	// ("command "x" killed: timeout") carry information the head lacks.
	summaryDupesCommand := false
	// Subject-first for file and symbol tools that carry attribution: the
	// tool name is the least interesting thing on such a row. Falls back to
	// the tool-name-first shape whenever there are no symbols, which is
	// every language without a tree-sitter grammar and every whole-file
	// rewrite — so the fallback is the common path, not a degraded one.
	if s := shellSubject(event); shellRow && s != "" {
		head = s
		// The summary duplicates the head only for normal foreground
		// runs (command + exit code). Background jobs ("started
		// background job <id>") and killed commands ("killed: timeout")
		// carry information the head lacks, so keep them.
		if event.CommandExitCode != nil && event.Sandbox.KilledReason == "" {
			summaryDupesCommand = true
		}
		if hookHint := hookIndicatorText(event.Hooks); hookHint != "" {
			head += dimSeparator + hookHint
		}
	} else if subject := symbolSubject(event); subject != "" && subjectFirstTool(event.ToolName) {
		head = subject
		if stat := diffStat(event.ResultContent); stat != "" {
			head += dimSeparator + stat
		}
		// A hooked edit must still surface its hook decision even when the
		// row leads with the symbol subject; the else-if below would
		// otherwise drop it on exactly the rows that carry attribution.
		if hookHint := hookIndicatorText(event.Hooks); hookHint != "" {
			head += dimSeparator + hookHint
		}
	} else if s := searchSubject(event); s != "" {
		head = s
		if hookHint := hookIndicatorText(event.Hooks); hookHint != "" {
			head += dimSeparator + hookHint
		}
	} else if hookHint := hookIndicatorText(event.Hooks); hookHint != "" {
		head += dimSeparator + hookHint
	}
	if event.Error != "" {
		head += dimSeparator + event.Error
	} else if event.ResultSummary != "" && !summaryDupesCommand {
		head += dimSeparator + event.ResultSummary
	}
	if event.ResultContent != "" || event.Error != "" {
		if expanded {
			head += " " + glyph.DisclosureExpanded
		} else {
			head += " " + glyph.DisclosureCollapsed
		}
	}

	cw := contentWidth(width)
	var b strings.Builder
	headWrapped := ansi.Wrap(head, cw, WrapBreakpoints)
	headLines := strings.Split(headWrapped, "\n")
	for i, hl := range headLines {
		if i == 0 {
			b.WriteString(gutter)
		} else {
			b.WriteString(continuation())
		}
		b.WriteString(style.Render(hl))
		b.WriteString("\n")
	}
	// Blast radius: a continuation line naming what references the symbol
	// this row changed. It is a settled one-time addition to a settled row,
	// so the height change it causes is acceptable and it is not part of any
	// live-region body. Nothing renders when there is no cached result — a
	// no-server negative, an empty reference set, or a language without LSP
	// all leave the row exactly as it would be without this feature.
	if len(callers) > 0 {
		shown, extra := callers, 0
		if len(shown) > maxCallersShown {
			extra = len(shown) - maxCallersShown
			shown = shown[:maxCallersShown]
		}
		line := fmt.Sprintf("↳ %d callers: %s", len(callers), strings.Join(shown, ", "))
		if extra > 0 {
			line += fmt.Sprintf(" +%d", extra)
		}
		for _, wl := range strings.Split(ansi.Wrap(line, contentWidth(width), WrapBreakpoints), "\n") {
			b.WriteString(continuation())
			b.WriteString(mutedStyle().Render(wl))
			b.WriteString("\n")
		}
	}
	if isDiffTool(event.ToolName) && event.ResultContent != "" {
		files := splitDiffFiles(event.ResultContent)
		shown := len(files)
		if !expanded {
			shown = min(shown, maxDiffFiles)
		}
		for i := 0; i < shown; i++ {
			f := files[i]
			stat := fmt.Sprintf("+%d −%d", f.added, f.removed)
			if f.path != "" {
				stat = f.path + " " + stat
			}
			b.WriteString(nestedRail())
			b.WriteString(dimStyle().Render(stat))
			b.WriteString("\n")
			rendered := diffview.Render(f.raw, diffview.Options{
				Width:     nestedContentWidth(width),
				Mode:      diffview.ModeAuto,
				Highlight: true,
			})
			lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
			elided := 0
			if !expanded && len(lines) > maxDiffLinesPerFile {
				elided = len(lines) - maxDiffLinesPerFile
				lines = lines[:maxDiffLinesPerFile]
			}
			for _, line := range lines {
				if line == "" {
					continue
				}
				b.WriteString(nestedRail())
				b.WriteString(line)
				b.WriteString("\n")
			}
			if elided > 0 {
				b.WriteString(nestedRail())
				b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more lines", elided)))
				b.WriteString("\n")
			}
		}
		if !expanded && len(files) > shown {
			b.WriteString(nestedRail())
			b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more files", len(files)-shown)))
			b.WriteString("\n")
		}
	}
	// Non-diff tools with captured output (agent.run is the motivating
	// case — its ResultContent holds the subagent's report) render it on
	// ctrl+g, with the same gutter indentation as diffs but using Wrap
	// instead of Truncate since the content is prose, not code lines.
	if expanded && !isDiffTool(event.ToolName) && event.ResultContent != "" {
		lines := strings.Split(strings.TrimRight(event.ResultContent, "\n"), "\n")
		elided := 0
		if len(lines) > maxExpandedResultLines {
			elided = len(lines) - maxExpandedResultLines
			lines = lines[:maxExpandedResultLines]
		}
		for _, line := range lines {
			wrapped := ansi.Wrap(line, nestedContentWidth(width), WrapBreakpoints)
			for _, wl := range strings.Split(wrapped, "\n") {
				b.WriteString(nestedRail())
				b.WriteString(dimStyle().Render(wl))
				b.WriteString("\n")
			}
		}
		if elided > 0 {
			b.WriteString(nestedRail())
			b.WriteString(dimStyle().Render(fmt.Sprintf("… %d more lines", elided)))
			b.WriteString("\n")
		}
	}
	// Failed calls carry no ResultContent; expanding one shows the full
	// error, the exit code when the tool reported one, and the call args.
	// Each detail line wraps at the nested content width — the raw args JSON
	// used to run straight off the terminal edge.
	if expanded && event.Error != "" {
		detail := "error: " + event.Error
		if event.CommandExitCode != nil {
			detail += fmt.Sprintf(" (exit code %d)", *event.CommandExitCode)
		}
		lines := []string{detail}
		if len(event.Args) > 0 {
			lines = append(lines, "args: "+string(event.Args))
		}
		for _, line := range lines {
			wrapped := ansi.Wrap(line, nestedContentWidth(width), WrapBreakpoints)
			for _, wl := range strings.Split(wrapped, "\n") {
				b.WriteString(nestedRail())
				b.WriteString(dimStyle().Render(wl))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

const (
	// maxDiffLinesPerFile caps the rendered body lines each changed file
	// gets; maxDiffFiles caps how many files render, so a sweeping
	// refactor cannot flood the transcript.
	maxDiffLinesPerFile = 12
	maxDiffFiles        = 6
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
	return name == "file.write_patch" || name == "patch.apply"
}

// hookIndicatorText picks the single highest-signal hook event from the
// metadata slice for the tool summary line. Priority (most specific first):
// block > rewrote > failed-open > generic count. Returns "" when there are
// no hooks to summarize.
func hookIndicatorText(hooks []registry.HookMetadata) string {
	if len(hooks) == 0 {
		return ""
	}
	for _, h := range hooks {
		if h.Decision == "block" || h.Decision == "halt" {
			return "hook blocked"
		}
	}
	for _, h := range hooks {
		if h.Rewrote {
			return "hook rewrote"
		}
	}
	for _, h := range hooks {
		if h.FailedOpen {
			return "hook failed-open"
		}
	}
	return fmt.Sprintf("hooks %d", len(hooks))
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

func renderApprovalPanel(tc *session.PendingToolCall, sb session.SandboxInfo, allowNetwork bool, width int) string {
	var b strings.Builder
	b.WriteString(gutterPrefix(glyph.Warning, warningColor))
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
	b.WriteString(warningStyle().Render(strings.Join(headParts, dimSeparator)))
	b.WriteString("\n")

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

func renderQuestionPanel(q *session.PendingQuestion, width int) string {
	if q == nil || len(q.Questions) == 0 {
		return ""
	}
	gutter := gutterPrefix(glyph.Question, violetColor)
	indent := continuation()
	// contentWidth leaves room for the ▍ rail cell plus the 3-cell gutter.
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
			b.WriteString(indent)
			b.WriteString(mutedStyle().Render("(" + strings.Join(qs.Options, " / ") + ")"))
			b.WriteString("\n")
		}
	}
	return chromeRail(b.String(), violetColor)
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
