package tui

import (
	"fmt"
	"hash/fnv"
	"image/color"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/viewmodel"
)

// minHeadlineCols is the room the right-aligned meta must leave the headline.
const minHeadlineCols = 24

// subRegion is a clickable range inside a rendered step, in lines relative to
// the step's first line. Rows record their own so a click on a tool row
// toggles that row rather than the whole step.
type subRegion struct {
	start, end int
	id         viewmodel.NodeID
	subagent   *session.SubagentView
	live       bool // bounded live region (wheel scrolls it)
}

// stepRenderCtx carries everything renderStep reads from the Model.
type stepRenderCtx struct {
	// expanded resolves whether a node draws expanded: its own choice, else
	// the state it inherits.
	expanded func(id viewmodel.NodeID, inherited bool) bool
	// liveExpanded is expanded for an in-flight call, which stays collapsed
	// until clicked whatever the global default says.
	liveExpanded func(viewmodel.NodeID) bool
	region       func(viewmodel.NodeID) regionView
	noteRows     func(id viewmodel.NodeID, rows int)
	callers      func(viewmodel.NodeID) []string

	spinner       string // live step header glyph
	toolSpinner   string // live tool row glyph
	thinkSpinner  string
	thinkElapsed  time.Duration
	now           time.Time
	routeModel    string
	routeProvider string
	sandbox       session.SandboxInfo
	allowNetwork  bool
	// workdir is the session's active root. A shell row that begins by
	// changing into it says "⌂ cmd" instead of repeating the path.
	workdir string
}

// open resolves whether a node draws expanded.
func (c *stepRenderCtx) open(id viewmodel.NodeID, inherited bool) bool {
	if c.expanded == nil {
		return inherited
	}
	return c.expanded(id, inherited)
}

func (c *stepRenderCtx) liveToolExpanded(id viewmodel.NodeID) bool {
	return c.liveExpanded != nil && c.liveExpanded(id)
}

// callersFor is the nil-safe form of the callers callback: a render context
// built by hand (tests, a one-off render) has no blast-radius cache, and a
// missing callback must read as "no callers" rather than panic.
func (c *stepRenderCtx) callersFor(id viewmodel.NodeID) []string {
	if c.callers == nil {
		return nil
	}
	return c.callers(id)
}

// renderStep renders one beat: a head (state glyph, narration, right-aligned
// meta), its reasoning when the beat is expanded, then tool rows at nested
// indent. Every piece ends in a newline, so the block's line count is
// strings.Count(out, "\n").
func renderStep(n *viewmodel.Node, c *stepRenderCtx, width int, inherited bool) (string, []subRegion) {
	si := n.Step
	rows := n.Children
	open := c.open(n.ID, inherited)
	var b strings.Builder
	var subs []subRegion
	lines := 0
	write := func(s string) {
		b.WriteString(s)
		lines += strings.Count(s, "\n")
	}
	row := func(id viewmodel.NodeID, s string, sub subRegion) {
		sub.id = id
		sub.start = lines
		write(s)
		sub.end = lines
		subs = append(subs, sub)
	}
	head, _ := stepHeadline(si)
	headless := head == ""

	failed := stepFailed(rows)

	g, gc := stepGlyph(n, failed, c)
	meta := metaParts{
		owner:      stepOwner(si.Step),
		role:       stepRoleWord(si.Step),
		model:      stepModel(si.Step.Actor, c.routeModel, c.routeProvider),
		thought:    thoughtMeta(si.Thinking),
		dur:        stepDuration(n, c.now),
		tag:        todoTag(si.Todo),
		ownerColor: actorColor(si.Step.Actor.Role),
	}
	if headless {
		// A beat with no narration gets no invented title: its head counts
		// its calls, and carries the meta like any other.
		write(headlessHead(g, gc, countToolCalls(rows), meta, width))
	} else {
		// Render narration as a single Markdown document so formatting and
		// code fences survive sentence boundaries.
		var texts []string
		for _, m := range si.Narration {
			if text := strings.TrimSpace(m.Content); text != "" {
				texts = append(texts, text)
			}
		}
		text := strings.Join(texts, "\n\n")
		cw := nestedContentWidth(width)
		body, ok := renderMarkdown(text, cw)
		if !ok {
			body = renderPlainProse(text, cw)
		}
		wrapped := strings.Split(strings.Trim(body, "\n"), "\n")
		first := strings.TrimSpace(wrapped[0])
		first = ansi.Cut(first, 0, ansi.StringWidth(strings.TrimRight(ansi.Strip(first), " ")))
		metaText, metaW := rightMeta(meta, ansi.StringWidth(first), width)
		line := gutterPrefix(g, gc) + first
		metaFits := metaW > 0 && ansi.StringWidth(first)+metaW+2 <= width-gutterWidth
		if metaFits {
			line += strings.Repeat(" ", width-gutterWidth-ansi.StringWidth(first)-metaW) + metaText
		}
		write(line + "\n")
		for _, line := range wrapped[1:] {
			write(strings.Repeat(" ", nestedBodyIndent) + strings.TrimRight(line, " ") + "\n")
		}
		if metaW > 0 && !metaFits {
			write(strings.Repeat(" ", max(width-metaW, gutterWidth)) + metaText + "\n")
		}
	}

	// The reasoning shows when the beat is expanded; collapsed, the head's
	// "thought Ns" stands for it.
	if open {
		write(renderBeatReasoning(si.Thinking, width))
	}

	// A long beat shows its newest rows; the rest collapse into one line.
	// Expanding the beat shows them all.
	rows = slices.DeleteFunc(slices.Clone(rows), func(ch *viewmodel.Node) bool { return !spineRow(ch) })
	if over := len(rows) - viewmodel.BeatRowCap; over > 0 && !open {
		write(spineBlock(mutedStyle().Render(fmt.Sprintf("… %d earlier calls", over))+"\n", false))
		rows = rows[over:]
	}

	// Calls and the plan line, each hung off the spine.
	w := max(width-spineWidth, 1)
	for i, ch := range rows {
		last := i == len(rows)-1
		switch {
		case ch.Kind == viewmodel.KindPlanUpdate:
			// The plan line is not a target of its own: a click on it opens
			// the beat.
			write(spineBlock(renderPlanRow(ch.PlanUpdate, w), last))
		case ch.Active != nil:
			row(ch.ID, spineBlock(renderActiveCallRow(*ch.Active, c, c.liveToolExpanded(ch.ID), w), last), subRegion{})
		case len(ch.Tools) > 1:
			row(ch.ID, spineBlock(renderGroupRow(ch.Tools, c.open(ch.ID, open), w, c.workdir), last), subRegion{})
		default:
			row(ch.ID, spineBlock(renderCallRow(ch.Tools[0], c.open(ch.ID, open), c.callersFor(ch.ID), w, c.workdir), last), subRegion{})
		}
	}

	return b.String(), subs
}

// spineRow reports whether a beat's child draws as a row on its spine.
func spineRow(ch *viewmodel.Node) bool {
	switch ch.Kind {
	case viewmodel.KindPlanUpdate:
		return ch.PlanUpdate != nil
	case viewmodel.KindTool:
		return ch.Active != nil || len(ch.Tools) > 0
	}
	return false
}

// renderCompletion is one finished piece of background work: a head row with
// its outcome, final measurement and duration, and the summary line under it.
// Expanded, it becomes the issue report.
func renderCompletion(c *viewmodel.CompletionInfo, expanded bool, width int, workdir string) string {
	th := theme.Current()
	g, colour := glyph.Job, th.StatusInfo
	if c.Source == viewmodel.CompletionSubagent {
		g, colour = glyph.Agent, th.AccentSecondary
	}
	head := lipgloss.NewStyle().Foreground(colour).Render(c.ID) + " " +
		lipgloss.NewStyle().Foreground(th.FGDefault).Render(viewmodel.ScrubText(viewmodel.ElideWorkdir(c.Label, workdir)))

	var meta []string
	switch c.Outcome {
	case viewmodel.OutcomeDone:
		meta = append(meta, statusOkStyle().Render(c.Outcome))
	case viewmodel.OutcomeUnknown:
		meta = append(meta, warningStyle().Render(c.Outcome))
	default:
		meta = append(meta, errorStyle().Render(c.Outcome))
	}
	if c.Measure != "" {
		meta = append(meta, mutedStyle().Render(c.Measure))
	}
	if c.Duration > 0 {
		meta = append(meta, mutedStyle().Render(formatElapsed(c.Duration)))
	}
	if len(meta) > 0 {
		head += dimSeparator + strings.Join(meta, dimSeparator)
	}

	// The disclosure sits in the last column, so the head is truncated to
	// leave room for it.
	head = ansi.Truncate(head, max(width-gutterWidth-2, 1), "…")
	pad := max(width-gutterWidth-ansi.StringWidth(head)-1, 0)
	disclosure := glyph.DisclosureCollapsed
	if expanded {
		disclosure = glyph.DisclosureExpanded
	}

	var b strings.Builder
	b.WriteString(gutterPrefix(g, colour))
	b.WriteString(head)
	b.WriteString(strings.Repeat(" ", pad))
	b.WriteString(mutedStyle().Render(disclosure))
	b.WriteString("\n")
	if expanded {
		b.WriteString(renderCompletionReport(c, width, workdir))
	} else if c.Summary != "" {
		b.WriteString(strings.Repeat(" ", nestedBodyIndent))
		b.WriteString(mutedStyle().Render(ansi.Truncate(viewmodel.ScrubText(c.Summary), max(width-nestedBodyIndent, 1), "…")))
		b.WriteString("\n")
	}
	return b.String()
}

// completionReportKeyWidth is the column the report's values start at: the key
// is muted and padded to this width, so every value lines up.
const completionReportKeyWidth = 10

// renderCompletionReport draws an expanded completion event's issue report:
// one line per field, the key muted and padded, the value in the default
// foreground. It is what a reader opens when the head's outcome is not enough.
func renderCompletionReport(c *viewmodel.CompletionInfo, width int, workdir string) string {
	th := theme.Current()
	valueStyle := lipgloss.NewStyle().Foreground(th.FGDefault)
	valueIndent := nestedBodyIndent + completionReportKeyWidth
	valueWidth := max(width-valueIndent, 1)
	lineWidth := max(width-nestedBodyIndent, 1)

	var b strings.Builder
	// field writes one key/value line, truncated to the report's width.
	field := func(key, value string, style lipgloss.Style) {
		if value == "" {
			return
		}
		line := mutedStyle().Render(padReportKey(key)) + style.Render(value)
		b.WriteString(strings.Repeat(" ", nestedBodyIndent))
		b.WriteString(ansi.Truncate(line, lineWidth, "…"))
		b.WriteString("\n")
	}
	// block writes a value's continuation lines under the value column.
	block := func(lines []string, style lipgloss.Style) {
		for _, l := range lines {
			b.WriteString(strings.Repeat(" ", valueIndent))
			b.WriteString(style.Render(ansi.Truncate(l, valueWidth, "…")))
			b.WriteString("\n")
		}
	}

	outcome := c.Outcome
	if c.StopCause != "" {
		outcome += dimSeparator + "stop: " + c.StopCause
	}
	field("outcome", outcome, valueStyle)

	if c.Source == viewmodel.CompletionSubagent {
		field("task", viewmodel.ScrubText(viewmodel.ElideWorkdir(c.Label, workdir)), valueStyle)
	} else {
		field("command", viewmodel.ScrubText(viewmodel.ElideWorkdir(firstLine(c.Command), workdir)), valueStyle)
	}
	field("cwd", viewmodel.ScrubText(viewmodel.ElideWorkdir(c.Cwd, workdir)), valueStyle)

	duration := ""
	switch {
	case c.Outcome == viewmodel.OutcomeLost:
		duration = "ended with session"
	case c.Duration > 0:
		duration = formatElapsed(c.Duration)
	}
	field("duration", duration, valueStyle)
	field("progress", c.Measure, valueStyle)

	// The output tail: the first line sits on the key's line, the rest under
	// the value column. Every line is model-authored, so each is scrubbed.
	lines := viewmodel.ReportTail(c.Output)
	if len(lines) > 0 {
		line := mutedStyle().Render(padReportKey("output")) + mutedStyle().Render(viewmodel.ScrubText(lines[0]))
		b.WriteString(strings.Repeat(" ", nestedBodyIndent))
		b.WriteString(ansi.Truncate(line, lineWidth, "…"))
		b.WriteString("\n")
	}
	if len(lines) > 1 {
		rest := make([]string, 0, len(lines)-1)
		for _, l := range lines[1:] {
			rest = append(rest, viewmodel.ScrubText(l))
		}
		block(rest, mutedStyle())
	}
	if c.Truncated {
		block([]string{"earlier output dropped"}, mutedStyle())
	}

	// A subagent's report is prose, so it renders as markdown under the value
	// column; plain prose is the fallback when glamour is unavailable.
	if c.Report != "" {
		rw := max(width-nestedBodyIndent-completionReportKeyWidth, 1)
		body, ok := renderMarkdown(c.Report, rw)
		if !ok {
			body = renderPlainProse(c.Report, rw)
		}
		rendered := strings.Split(strings.Trim(body, "\n"), "\n")
		if len(rendered) > 0 {
			line := mutedStyle().Render(padReportKey("report")) + rendered[0]
			b.WriteString(strings.Repeat(" ", nestedBodyIndent))
			b.WriteString(ansi.Truncate(line, lineWidth, "…"))
			b.WriteString("\n")
			block(rendered[1:], lipgloss.NewStyle())
		}
	}

	field("error", firstLine(c.Error), errorStyle())
	return b.String()
}

// padReportKey pads a report key to the value column.
func padReportKey(key string) string {
	if n := completionReportKeyWidth - ansi.StringWidth(key); n > 0 {
		return key + strings.Repeat(" ", n)
	}
	return key
}

// countToolCalls is the number of tool calls a step made, counting each call
// of a merged run.
func countToolCalls(rows []*viewmodel.Node) int {
	n := 0
	for _, r := range rows {
		n += len(r.Tools)
		if r.Active != nil {
			n++
		}
	}
	return n
}

// indentLines prefixes every non-empty line with n spaces.
func indentLines(s string, n int) string {
	if s == "" || n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", n)
	parts := strings.Split(s, "\n")
	for i, p := range parts {
		if p != "" {
			parts[i] = pad + p
		}
	}
	return strings.Join(parts, "\n")
}

// ---- header ------------------------------------------------------------

// stepGlyph picks the state glyph: ✗ if any row failed, the spinner while
// live, and a muted ● once settled.
func stepGlyph(n *viewmodel.Node, failed bool, c *stepRenderCtx) (string, color.Color) {
	th := theme.Current()
	switch {
	case failed:
		return glyph.Error, th.StatusError
	case n.Live:
		g := c.spinner
		if g == "" {
			g = glyph.Running
		}
		return g, accentColor
	}
	// A settled beat wears a muted ●, not a check: a ✓ on every narration
	// turns the transcript into a column of badges. Only the states that need
	// attention (running, failed) get a marker with colour.
	return glyph.Beat, th.FGMuted
}

// stepFailed reports whether any tool row in the step failed: an error, a
// denied approval, or a non-zero exit.
func stepFailed(rows []*viewmodel.Node) bool {
	for _, r := range rows {
		for _, ev := range r.Tools {
			if viewmodel.EventFailed(ev) {
				return true
			}
		}
	}
	return false
}

func stepOwner(st session.Step) string { return viewmodel.StepOwner(st) }

// stepRoleWord is the owner shortened to its role word ("reviewer #1" →
// "reviewer"), the form that survives a narrow header.
func stepRoleWord(st session.Step) string { return viewmodel.StepRoleWord(st) }

// stepModel is "model @ provider" only when it differs from the active route:
// a step on the session's own model adds nothing the status line lacks.
func stepModel(a session.Actor, routeModel, routeProvider string) string {
	if a.Model == "" || (a.Model == routeModel && (a.Provider == "" || a.Provider == routeProvider)) {
		return ""
	}
	if a.Provider != "" && a.Provider != routeProvider {
		return a.Model + " @ " + a.Provider
	}
	return a.Model
}

// stepDuration is the step's wall time: end minus start once settled, elapsed
// while live, and nothing for steps whose end is unknown (heuristic steps, or
// an open step in an idle session).
func stepDuration(n *viewmodel.Node, now time.Time) string {
	st := n.Step.Step
	if st.StartedAt.IsZero() {
		return ""
	}
	end := st.EndedAt
	if end.IsZero() {
		if !n.Live {
			return ""
		}
		end = now
	}
	d := max(end.Sub(st.StartedAt), 0)
	if d < notableStepDuration {
		return ""
	}
	return formatElapsed(d)
}

// notableStepDuration is how long a step has to run before its header says
// so. Most steps take a few seconds, and a "3s" on each only adds noise (and
// a live counter that ticks while the reader is looking at the text).
const notableStepDuration = 15 * time.Second

// metaParts are the pieces of a step header's right-aligned meta.
type metaParts struct {
	owner      string
	role       string // owner shortened to the role word
	model      string
	thought    string // "thought 2s" when the beat reasoned
	dur        string
	tag        string // "▸ 2/4" on the beat that starts a todo
	ownerColor color.Color
}

// rightMeta lays out the head's right-aligned meta so the headline keeps at
// least min(headlineWidth, minHeadlineCols) columns. Under pressure it drops
// the model first, then the duration, then the thinking time, then shortens
// the owner to its role word and drops it. The todo tag goes last. It
// returns the styled text and its visible width.
func rightMeta(p metaParts, headlineWidth, width int) (string, int) {
	budget := width - gutterWidth - 2 - min(headlineWidth, minHeadlineCols)
	cands := []metaParts{
		{owner: p.owner, model: p.model, thought: p.thought, dur: p.dur, tag: p.tag},
		{owner: p.owner, thought: p.thought, dur: p.dur, tag: p.tag},
		{owner: p.owner, thought: p.thought, tag: p.tag},
		{owner: p.owner, tag: p.tag},
		{owner: p.role, tag: p.tag},
		{tag: p.tag},
		{},
	}
	for _, cd := range cands {
		cd.ownerColor = p.ownerColor
		text, w := renderMeta(cd)
		if w <= budget {
			return text, w
		}
	}
	return "", 0
}

func renderMeta(p metaParts) (string, int) {
	var plain, styled []string
	add := func(s string, style lipgloss.Style) {
		if s == "" {
			return
		}
		plain = append(plain, s)
		styled = append(styled, style.Render(s))
	}
	ownerStyle := mutedStyle()
	if p.ownerColor != nil {
		ownerStyle = lipgloss.NewStyle().Foreground(p.ownerColor)
	}
	add(p.owner, ownerStyle)
	add(p.model, mutedStyle())
	add(p.thought, mutedStyle())
	add(p.dur, mutedStyle())
	add(p.tag, lipgloss.NewStyle().Foreground(accentColor))
	sep := " · "
	return strings.Join(styled, mutedStyle().Render(sep)), ansi.StringWidth(strings.Join(plain, sep))
}

// stepHeadline picks the header text. A narrated beat uses the first sentence
// of its first narration; the remainder (and any later narration) becomes the
// continuation. A beat without narration is headless (head == ""): its rows
// render on their own, with no invented title.
func stepHeadline(si *viewmodel.StepInfo) (head, rest string) {
	return viewmodel.StepHeadline(si)
}

// firstSentence splits s at the first ". ", "! ", "? " or newline that comes
// after at least 8 runes. A sentence-final mark at the very end of s stays
// with the head.
func firstSentence(s string) (head, rest string) { return viewmodel.FirstSentence(s) }

// stripEmphasis removes markdown emphasis and code markers from a headline,
// which is rendered as plain text.
func stripEmphasis(s string) string { return viewmodel.StripEmphasis(s) }

// thoughtMeta is the head's "thought 2s": how long the beat reasoned, or
// nothing when it did not.
func thoughtMeta(ts []*session.ThinkingEntry) string {
	if len(ts) == 0 {
		return ""
	}
	var d time.Duration
	for _, t := range ts {
		d += t.Duration
	}
	return "thought " + formatThinkDuration(d)
}

// todoTag is "▸ 2/4" on the beat where work moves to the second of four
// todos, and nothing on every other beat.
func todoTag(l *viewmodel.TodoLabel) string {
	if l == nil {
		return ""
	}
	return fmt.Sprintf("%s %d/%d", glyph.Running, l.Index, l.Total)
}

// headlessHead is the head of a beat with no narration: the state glyph and a
// muted call count, with the meta right-aligned.
func headlessHead(g string, gc color.Color, calls int, meta metaParts, width int) string {
	text := ""
	if calls > 0 {
		text = fmt.Sprintf("%d %s", calls, plural(calls, "call", "calls"))
	}
	line := gutterPrefix(g, gc) + mutedStyle().Render(text)
	if metaText, metaW := rightMeta(meta, ansi.StringWidth(text), width); metaW > 0 {
		line += strings.Repeat(" ", max(width-gutterWidth-ansi.StringWidth(text)-metaW, 1)) + metaText
	}
	return line + "\n"
}

// renderBeatReasoning is an expanded beat's reasoning, behind the nested rail
// under its head: every thinking block in order, a blank rail between them.
func renderBeatReasoning(ts []*session.ThinkingEntry, width int) string {
	var b strings.Builder
	cw := nestedContentWidth(width)
	for i, t := range ts {
		text := strings.TrimSpace(t.Text)
		if text == "" {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteString(nestedRail() + "\n")
		}
		for _, line := range strings.Split(text, "\n") {
			for _, wl := range strings.Split(ansi.Wrap(line, cw, WrapBreakpoints), "\n") {
				b.WriteString(nestedRail())
				b.WriteString(thinkingLineStyle().Render(wl))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---- owner colour ------------------------------------------------------

// actorColor gives a non-orchestrator role a stable colour from a small
// palette, chosen by FNV hash of the role name. The label text is always
// rendered, so NO_COLOR loses the tint and nothing else. The orchestrator
// (empty role) has no colour.
func actorColor(role string) color.Color {
	if role == "" {
		return nil
	}
	th := theme.Current()
	palette := []color.Color{th.AccentSecondary, th.AccentTertiary, th.StatusInfo}
	h := fnv.New32a()
	_, _ = h.Write([]byte(role))
	return palette[int(h.Sum32())%len(palette)]
}

// approvalWhyFor looks up who is asking and why for a pending approval: the
// owner of the requesting step and the first sentence of that step's
// narration. A call from a step with no narration gets no why line.
//
// A step ID is only meaningful in the State that issued it, so owner is the
// state holding the pending call: the parent's, or a running child's.
func (m Model) approvalWhyFor(owner *session.State, tc *session.PendingToolCall) approvalWhy {
	if tc == nil || tc.StepID == 0 || owner == nil {
		return approvalWhy{}
	}
	st, ok := owner.Step(tc.StepID)
	if !ok {
		return approvalWhy{}
	}
	w := approvalWhy{owner: stepOwner(st)}
	if head, _ := firstSentence(owner.StepNarration(tc.StepID)); head != "" {
		w.why = stripEmphasis(head)
	}
	return w
}

// subagentHeadline is the first sentence of a child's latest narration: what
// it says it is doing, for the now bar's agent rows.
func subagentHeadline(child *session.State) string {
	if child == nil {
		return ""
	}
	line := strings.TrimSpace(child.LatestNarrationLine())
	if line == "" {
		return ""
	}
	head, _ := firstSentence(line)
	return stripEmphasis(head)
}
