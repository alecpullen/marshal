package tui

import (
	"fmt"
	"hash/fnv"
	"image/color"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/stack"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/registry"
)

// stepRowIndent is how far a row inside a step sits to the right of the same
// row at top level. A top-level row's glyph is at column 1 (gutterPrefix is
// " ⌗ "); inside a step it sits at nestedBodyIndent, column 5.
const stepRowIndent = nestedBodyIndent - 1

// minHeadlineCols is the room the right-aligned meta must leave the headline.
const minHeadlineCols = 24

// subRegion is a clickable range inside a rendered step, in lines relative to
// the step's first line. Rows record their own so a click on a tool row
// toggles that row rather than the whole step.
type subRegion struct {
	start, end int
	id         stack.NodeID
	subagent   *session.SubagentView
	live       bool // bounded live region (wheel scrolls it)
}

// stepRenderCtx carries everything renderStep reads from the Model.
type stepRenderCtx struct {
	// density resolves a node's level: its own override, else inherited.
	density func(id stack.NodeID, inherited density) density
	// record is told the level each node is drawn at.
	record func(id stack.NodeID, d density)
	// foldTasks is the session fold toggle; hasOverride says a user override
	// exists for a node (which unfolds it).
	foldTasks   bool
	hasOverride func(stack.NodeID) bool
	// liveExpanded is expanded for an in-flight call, which stays collapsed
	// until clicked whatever the global default says.
	liveExpanded func(stack.NodeID) bool
	region       func(stack.NodeID) regionView
	noteRows     func(id stack.NodeID, rows int)
	callers      func(stack.NodeID) []string

	spinner       string // live step header glyph
	toolSpinner   string // live tool row glyph
	thinkSpinner  string
	thinkElapsed  time.Duration
	now           time.Time
	routeModel    string
	routeProvider string
	sandbox       session.SandboxInfo
	allowNetwork  bool
}

// level resolves and records a node's density.
func (c *stepRenderCtx) level(id stack.NodeID, inherited density) density {
	d := inherited
	if c.density != nil {
		d = c.density(id, inherited)
	}
	if c.record != nil {
		c.record(id, d)
	}
	return d
}

func (c *stepRenderCtx) liveToolExpanded(id stack.NodeID) bool {
	return c.liveExpanded != nil && c.liveExpanded(id)
}

// renderStep renders one step: a header (state glyph, headline, right-aligned
// owner meta), the narration continuation, thinking rows, then tool rows and
// subagent cards at nested indent. Every piece ends in a newline, so the
// block's line count is strings.Count(out, "\n").
func renderStep(n *stack.Node, c *stepRenderCtx, width int, inherited density) (string, []subRegion) {
	si := n.Step
	rows := n.Children
	sd := c.level(n.ID, inherited)
	var b strings.Builder
	var subs []subRegion
	lines := 0
	write := func(s string) {
		b.WriteString(s)
		lines += strings.Count(s, "\n")
	}
	row := func(id stack.NodeID, s string, sub subRegion) {
		if s == "" {
			return
		}
		sub.id = id
		sub.start = lines
		write(s)
		sub.end = lines
		subs = append(subs, sub)
	}

	head, rest, inferred := stepHeadline(si, rows)
	// A headline too long for even the bare header is cut with an ellipsis;
	// keep the whole sentence reachable in the continuation.
	if !inferred && ansi.StringWidth(head) > width-gutterWidth {
		rest = strings.TrimSpace(head + " " + rest)
	}
	failed := stepFailed(rows)

	// Header.
	g, gc := stepGlyph(n, rows, failed, c)
	meta := metaParts{
		owner:      stepOwner(si.Step),
		role:       stepRoleWord(si.Step),
		model:      stepModel(si.Step.Actor, c.routeModel, c.routeProvider),
		dur:        stepDuration(n, c.now),
		ownerColor: actorColor(si.Step.Actor.Role),
	}
	if inferred {
		meta.tag = "inferred"
	}
	if sd == densityOutline {
		meta.tools = countToolCalls(rows)
	}
	write(stepHeaderLine(g, gc, head, inferred, meta, width) + "\n")
	if sd == densityOutline {
		// One row per step: narration, reasoning and tool rows are all
		// behind a density change.
		return b.String(), subs
	}

	// Narration continuation.
	if rest != "" {
		write(renderStepContinuation(rest, sd == densityFull, width))
	}

	// Thinking rows.
	for _, t := range si.Thinking {
		id := stack.ThinkingID(t)
		td := c.level(id, sd)
		row(id, renderNestedThinking(t, td == densityFull, width), subRegion{})
	}
	if si.LiveThinking != "" {
		rv := c.region(stack.LiveThinkingID)
		box := renderThinkingBox(si.LiveThinking, c.thinkSpinner, c.thinkElapsed, rv, nestedContentWidth(width))
		if box != "" {
			if cnt := strings.Count(box, "\n"); cnt > rv.minRows {
				c.noteRows(stack.LiveThinkingID, cnt)
			}
			row(stack.LiveThinkingID, indentLines(box, continuationIndent), subRegion{live: true})
		}
	}

	// Tool rows and subagent cards.
	for _, ch := range rows {
		switch {
		case ch.Kind == stack.KindTool && ch.Active != nil:
			row(ch.ID, renderActiveToolRow(*ch.Active, c, c.liveToolExpanded(ch.ID), width), subRegion{})
		case ch.Kind == stack.KindTool && len(ch.Tools) > 1:
			rd := c.level(ch.ID, sd)
			if rd == densityOutline {
				continue
			}
			row(ch.ID, renderToolGroupRow(ch.Tools, rd == densityFull, width), subRegion{})
		case ch.Kind == stack.KindTool && len(ch.Tools) == 1:
			rd := c.level(ch.ID, sd)
			if rd == densityOutline {
				continue
			}
			row(ch.ID, renderToolRow(ch.Tools[0], rd == densityFull, c.callers(ch.ID), width), subRegion{})
		case ch.Kind == stack.KindSubagent && ch.Item != nil && ch.Item.Subagent != nil:
			v := *ch.Item.Subagent
			rv := c.region(ch.ID)
			cd := c.level(ch.ID, sd)
			card := renderSubagentCard(v, cd == densityFull, c.toolSpinner, rv, width-stepRowIndent)
			if cd == densityOutline {
				card = headRow(card)
			}
			if cnt := strings.Count(card, "\n"); cnt > rv.minRows && cd != densityOutline {
				c.noteRows(ch.ID, cnt)
			}
			sub := subRegion{live: v.Status == session.SubagentRunning && cd != densityOutline}
			if v.Child != nil {
				sub.subagent = &v
			}
			row(ch.ID, indentLines(card, stepRowIndent), sub)
		}
	}
	return b.String(), subs
}

// headRow keeps only the first line of a rendered block, newline included.
func headRow(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i+1]
	}
	return s
}

// countToolCalls is the number of tool calls a step made, counting each call
// of a merged run.
func countToolCalls(rows []*stack.Node) int {
	n := 0
	for _, r := range rows {
		n += len(r.Tools)
		if r.Active != nil {
			n++
		}
	}
	return n
}

// renderToolRow renders a settled tool call inside a step. It is the
// top-level row, indented: width is reduced by the indent so wrapping stays
// inside the frame, and every line (continuations included) shifts with it.
func renderToolRow(ev registry.AuditEvent, expanded bool, callers []string, width int) string {
	return indentLines(renderCompletedToolCall(ev, expanded, callers, width-stepRowIndent), stepRowIndent)
}

// renderToolGroupRow is renderToolRow for a merged same-tool run.
func renderToolGroupRow(evs []registry.AuditEvent, expanded bool, width int) string {
	return indentLines(renderToolGroup(evs, expanded, width-stepRowIndent), stepRowIndent)
}

// renderActiveToolRow renders an in-flight tool call inside a step.
func renderActiveToolRow(atc session.ActiveToolCall, c *stepRenderCtx, expanded bool, width int) string {
	return indentLines(renderActiveToolCall(atc, c.sandbox, c.allowNetwork, c.toolSpinner, c.now, expanded, width-stepRowIndent), stepRowIndent)
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
// live, ✓ once settled with tool rows, · for a step with none.
func stepGlyph(n *stack.Node, rows []*stack.Node, failed bool, c *stepRenderCtx) (string, color.Color) {
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
	case hasToolRows(rows):
		return glyph.OK, th.StatusSuccess
	}
	return glyph.Ambient, th.FGMuted
}

func hasToolRows(rows []*stack.Node) bool {
	for _, r := range rows {
		if r.Kind == stack.KindTool {
			return true
		}
	}
	return false
}

// stepFailed reports whether any tool row in the step failed: an error, a
// denied approval, or a non-zero exit.
func stepFailed(rows []*stack.Node) bool {
	for _, r := range rows {
		for _, ev := range r.Tools {
			if ev.Error != "" || ev.Approval == registry.ApprovalDenied ||
				(ev.CommandExitCode != nil && *ev.CommandExitCode != 0) {
				return true
			}
		}
	}
	return false
}

func stepOwner(st session.Step) string {
	if st.Actor.Role == "" && st.Actor.Label == "" {
		return ""
	}
	if st.Actor.Label != "" {
		return st.Actor.Label
	}
	return strings.ReplaceAll(strings.TrimPrefix(st.Actor.Role, "sdd_"), "_", " ")
}

// stepRoleWord is the owner shortened to its role word ("reviewer #1" →
// "reviewer"), the form that survives a narrow header.
func stepRoleWord(st session.Step) string {
	o := stepOwner(st)
	if i := strings.IndexAny(o, " #"); i > 0 {
		o = o[:i]
	}
	if st.Actor.Role != "" && strings.Contains(st.Actor.Role, "branch") {
		return "branch reviewer"
	}
	return o
}

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
func stepDuration(n *stack.Node, now time.Time) string {
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
	return formatElapsed(max(end.Sub(st.StartedAt), 0))
}

// metaParts are the pieces of a step header's right-aligned meta.
type metaParts struct {
	tag        string // "inferred"
	tools      int    // outline density: how many calls the collapsed step hides
	owner      string
	role       string // owner shortened to the role word
	model      string
	dur        string
	ownerColor color.Color
}

// rightMeta lays out the header's right-aligned meta so the headline keeps at
// least min(headlineWidth, minHeadlineCols) columns. Under pressure it drops
// the model first, then the duration, then shortens the owner to its role
// word, and drops the owner only when even that does not fit. It returns the
// styled text and its visible width.
func rightMeta(p metaParts, headlineWidth, width int) (string, int) {
	budget := width - gutterWidth - 2 - min(headlineWidth, minHeadlineCols)
	type cand struct{ tag, owner, model, tools, dur string }
	tools := ""
	switch {
	case p.tools == 1:
		tools = "1 tool"
	case p.tools > 1:
		tools = fmt.Sprintf("%d tools", p.tools)
	}
	cands := []cand{
		{p.tag, p.owner, p.model, tools, p.dur},
		{p.tag, p.owner, "", tools, p.dur},
		{p.tag, p.owner, "", tools, ""},
		{p.tag, p.role, "", tools, ""},
		{p.tag, "", "", tools, ""},
		{"", "", "", tools, ""},
		{"", "", "", "", ""},
	}
	for _, cd := range cands {
		text, w := renderMeta(cd.tag, cd.owner, cd.model, cd.tools, cd.dur, p.ownerColor)
		if w <= budget {
			return text, w
		}
	}
	return "", 0
}

func renderMeta(tag, owner, model, tools, dur string, ownerColor color.Color) (string, int) {
	var plain, styled []string
	add := func(s string, style lipgloss.Style) {
		if s == "" {
			return
		}
		plain = append(plain, s)
		styled = append(styled, style.Render(s))
	}
	add(tag, thinkingLineStyle())
	ownerStyle := mutedStyle()
	if ownerColor != nil {
		ownerStyle = lipgloss.NewStyle().Foreground(ownerColor)
	}
	add(owner, ownerStyle)
	add(model, mutedStyle())
	add(tools, mutedStyle())
	add(dur, mutedStyle())
	sep := " · "
	return strings.Join(styled, mutedStyle().Render(sep)), ansi.StringWidth(strings.Join(plain, sep))
}

// stepHeaderLine assembles " ✓ headline        owner · 12s" to exactly the
// frame width at most: the headline is truncated to what the meta leaves.
func stepHeaderLine(g string, gc color.Color, head string, inferred bool, meta metaParts, width int) string {
	headStyle := lipgloss.NewStyle().Foreground(theme.Current().FGEmphasis)
	if inferred {
		headStyle = thinkingLineStyle().Italic(true)
	}
	metaText, metaW := rightMeta(meta, ansi.StringWidth(head), width)
	avail := max(width-gutterWidth, 1)
	headRoom := avail
	if metaW > 0 {
		headRoom = max(avail-metaW-2, 1)
	}
	head = ansi.Truncate(head, headRoom, "…")
	line := gutterPrefix(g, gc) + headStyle.Render(head)
	if metaW > 0 {
		pad := max(avail-ansi.StringWidth(head)-metaW, 1)
		line += strings.Repeat(" ", pad) + metaText
	}
	return line
}

// stepHeadline picks the header text. A narrated step uses the first sentence
// of its first narration; the remainder (and any later narration) becomes the
// continuation. Without narration the headline is inferred from the tool rows
// and rendered as such.
func stepHeadline(si *stack.StepInfo, rows []*stack.Node) (head, rest string, inferred bool) {
	var texts []string
	for _, m := range si.Narration {
		if t := strings.TrimSpace(m.Content); t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) > 0 {
		h, r := firstSentence(texts[0])
		parts := []string{}
		if r != "" {
			parts = append(parts, r)
		}
		parts = append(parts, texts[1:]...)
		return stripEmphasis(h), strings.Join(parts, "\n\n"), false
	}
	if h := inferHeadline(rows); h != "" {
		return h, "", true
	}
	if len(si.Thinking) > 0 || si.LiveThinking != "" {
		return "thinking", "", true
	}
	return "working", "", true
}

// firstSentence splits s at the first ". ", "! ", "? " or newline that comes
// after at least 8 runes. A sentence-final mark at the very end of s stays
// with the head.
func firstSentence(s string) (head, rest string) {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\n':
			if i >= 8 {
				return strings.TrimSpace(string(runes[:i])), strings.TrimSpace(string(runes[i+1:]))
			}
		case (r == '.' || r == '!' || r == '?') && i >= 7:
			if i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
				return string(runes[:i+1]), strings.TrimSpace(string(runes[i+1:]))
			}
		}
	}
	return s, ""
}

// stripEmphasis removes markdown emphasis and code markers from a headline,
// which is rendered as plain text.
func stripEmphasis(s string) string {
	return strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "~~", "").Replace(s)
}

// renderStepContinuation renders the muted continuation under the header: one
// truncated line with a disclosure marker while collapsed, the markdown in
// full when expanded.
func renderStepContinuation(rest string, expanded bool, width int) string {
	cw := nestedContentWidth(width)
	pad := strings.Repeat(" ", nestedBodyIndent)
	if !expanded {
		oneLine := strings.Join(strings.Fields(rest), " ")
		line := ansi.Truncate(oneLine, max(cw-2, 1), "…") + " " + glyph.DisclosureCollapsed
		return pad + mutedStyle().Render(line) + "\n"
	}
	body, ok := renderMarkdown(rest, cw)
	if !ok {
		body = renderPlainProse(rest, cw)
	}
	var b strings.Builder
	for _, l := range strings.Split(strings.Trim(body, "\n"), "\n") {
		b.WriteString(pad)
		b.WriteString(strings.TrimLeft(l, " "))
		b.WriteString("\n")
	}
	return b.String()
}

// renderNestedThinking renders a step's reasoning as a row at nested indent:
// "⚙ thought for 2s ▹", and when expanded the reasoning behind the nested
// rail.
func renderNestedThinking(t *session.ThinkingEntry, expanded bool, width int) string {
	disclosure := ""
	if strings.TrimSpace(t.Text) != "" {
		disclosure = glyph.DisclosureCollapsed
		if expanded {
			disclosure = glyph.DisclosureExpanded
		}
	}
	label := fmt.Sprintf("thought for %s", formatThinkDuration(t.Duration))
	if disclosure != "" {
		label += " " + disclosure
	}
	var b strings.Builder
	b.WriteString(indentLines(gutterPrefix(glyph.Thinking, dimColor)+thinkingLineStyle().Render(label)+"\n", stepRowIndent))
	if expanded && strings.TrimSpace(t.Text) != "" {
		cw := nestedContentWidth(width)
		for _, line := range strings.Split(strings.TrimSpace(t.Text), "\n") {
			for _, wl := range strings.Split(ansi.Wrap(line, cw, WrapBreakpoints), "\n") {
				b.WriteString(nestedRail())
				b.WriteString(thinkingLineStyle().Render(wl))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// ---- inferred headline -------------------------------------------------

// inferHeadline describes what a step did from its tool rows when the model
// did not narrate: up to two clauses joined by " · ", then "…" if more
// follow. It is shown italic and tagged "inferred" because it is a guess at
// intent, not the agent's own words.
func inferHeadline(rows []*stack.Node) string {
	type clause struct {
		kind  string
		count int
		text  string
	}
	var order []string
	byKind := map[string]*clause{}
	touch := func(kind string) *clause {
		c, ok := byKind[kind]
		if !ok {
			c = &clause{kind: kind}
			byKind[kind] = c
			order = append(order, kind)
		}
		return c
	}
	visit := func(name, target string, n int) {
		switch {
		case name == "file.read":
			touch("read").count += n
		case isSearchTool(name):
			c := touch("search")
			c.count += n
			if c.text == "" && target != "" {
				c.text = target
			}
		case name == "shell.run" || name == "test.run":
			c := touch("ran:" + name)
			c.count += n
			if c.text == "" {
				if f := strings.Fields(target); len(f) > 0 {
					c.text = f[0]
				}
			}
		case name == "file.write_patch" || name == "patch.apply" || strings.HasPrefix(name, "file.write"):
			c := touch("edit")
			c.count += n
			if c.text == "" && target != "" {
				c.text = filepath.Base(target)
			}
		case name == "agent.run":
			touch("agents").count += n
		default:
			c := touch("other:" + name)
			c.count += n
			c.text = DisplayToolName(name)
		}
	}
	for _, r := range rows {
		switch {
		case r.Kind == stack.KindSubagent:
			visit("agent.run", "", 1)
		case r.Active != nil:
			visit(r.Active.Name, strings.TrimPrefix(r.Active.Args, "$ "), 1)
		default:
			for _, ev := range r.Tools {
				visit(ev.ToolName, toolTarget(ev), 1)
			}
		}
	}
	var parts []string
	for _, k := range order {
		c := byKind[k]
		switch {
		case k == "read":
			parts = append(parts, fmt.Sprintf("read %d %s", c.count, plural(c.count, "file", "files")))
		case k == "search":
			if c.text != "" {
				parts = append(parts, fmt.Sprintf("searched %q", c.text))
			} else {
				parts = append(parts, "searched")
			}
		case strings.HasPrefix(k, "ran:"):
			if c.text != "" {
				parts = append(parts, "ran "+c.text+" …")
			} else {
				parts = append(parts, "ran a command")
			}
		case k == "edit":
			if c.text != "" {
				parts = append(parts, "edited "+c.text)
			} else {
				parts = append(parts, fmt.Sprintf("edited %d %s", c.count, plural(c.count, "file", "files")))
			}
		case k == "agents":
			parts = append(parts, fmt.Sprintf("dispatched %d %s", c.count, plural(c.count, "agent", "agents")))
		default:
			parts = append(parts, c.text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts[:min(len(parts), 2)], " · ")
	if len(parts) > 2 {
		out += " …"
	}
	return out
}

func isSearchTool(name string) bool {
	return strings.HasPrefix(name, "repo.search") || strings.HasPrefix(name, "codebase.search") ||
		strings.HasPrefix(name, "symbols.") || name == "web.search" || strings.HasPrefix(name, "search.")
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
// it says it is doing, for the subagent card and the now bar's agent rows.
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

// subagentBodyLines is the live card's body: the child's headline and what
// tool it is running. Before the child has narrated anything it falls back to
// the raw activity tail, which agent.output also reads.
func subagentBodyLines(child *session.State, n int) []string {
	head := subagentHeadline(child)
	if head == "" {
		return subagentTailLines(child, n)
	}
	lines := []string{head}
	if label := child.CurrentToolLabel(); label != "" {
		g := toolCategoryGlyph(label)
		text := DisplayToolName(label)
		if strings.HasPrefix(label, "editing ") {
			g, text = glyph.Edit, label
		}
		lines = append(lines, g+" "+text)
	}
	return lines
}

// subagentSummaryHeadline is the first sentence of a settled child's final
// summary.
func subagentSummaryHeadline(summary string) string {
	head, _ := firstSentence(strings.TrimSpace(summary))
	return stripEmphasis(head)
}

// liveStepSummary describes the step now running — its narrated headline, or
// an inferred one — and the category glyph of its running tool, for the now
// bar's live-mirror row.
func (m Model) liveStepSummary() (headline, toolGlyph string) {
	state, _ := m.transcriptSource()
	live, ok := state.OpenStep()
	if !ok {
		return "", ""
	}
	if h, _ := firstSentence(state.StepNarration(live.ID)); h != "" {
		headline = stripEmphasis(h)
	}
	var rows []*stack.Node
	for _, ev := range state.StepAudits(live.ID) {
		rows = append(rows, &stack.Node{Kind: stack.KindTool, Tools: []registry.AuditEvent{ev}})
	}
	for _, atc := range state.ActiveToolCalls() {
		if atc.StepID == live.ID {
			a := atc
			rows = append(rows, &stack.Node{Kind: stack.KindTool, Active: &a})
			toolGlyph = toolCategoryGlyph(atc.Name)
		}
	}
	if headline == "" {
		headline = inferHeadline(rows)
	}
	return headline, toolGlyph
}
