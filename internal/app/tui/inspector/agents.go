package inspector

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// AgentStatus is the Agents tab's own classification of an agent's state.
//
// It is a local enum rather than the session package's, because this package is
// presentation-only and must not depend on runtime types: the caller copies a
// `session.SubagentView` into an `Agent`, and the mapping between "what the
// runtime says" and "what the reader is shown" belongs at that boundary, where
// it can be tested without a runtime.
type AgentStatus int

const (
	// AgentRunning means the child is still working. It is the only status for
	// which the stop action is offered.
	AgentRunning AgentStatus = iota
	// AgentCompleted means the child finished. A completed child that was
	// canceled is reported by the runtime as failed with an error, so there is
	// deliberately no separate "cancelled" value to invent here.
	AgentCompleted
	// AgentFailed means the child did not finish its work. Error carries why.
	AgentFailed
)

// Agent is the copied, presentation-only view of one runtime agent.
//
// It is a COPY of the metadata the caller hands in, never a pointer into live
// session state. A panel holding a live pointer would render whatever the
// child happened to be doing at draw time, which makes the panel's scroll and
// selection meaningless and couples rendering to the runtime's locking.
type Agent struct {
	// ID is the runtime agent's ID. It is the SELECTION IDENTITY: the row
	// cursor is stored as an ID, not an index, because a status change or a
	// reorder must never move the reader onto a different child.
	ID    int64
	Label string
	// Status classifies the agent for rendering and for the stop action.
	Status AgentStatus
	// Role, Provider, Model and Fallback are the dispatched route's provenance.
	// They are empty for agents that were not dispatched by a role router.
	Role     string
	Provider string
	Model    string
	Fallback bool
	// Elapsed is the pre-formatted duration ("12s", "1m 4s"), or "" when the
	// runtime did not record one. Formatting lives with the caller because the
	// duration depends on the clock, and a panel that called time.Now would
	// re-render differently on every frame.
	Elapsed string
	// ToolCalls counts completed tool calls, and CurrentTool names the
	// in-flight one. CurrentTool is meaningful only while running.
	ToolCalls   int
	CurrentTool string
	// Summary is the child's final report, once it has finished.
	Summary string
	// Error carries the failure text when Status == AgentFailed.
	Error string
	// SalvagedReason is non-empty when the child hit a budget ceiling and its
	// summary is therefore partial. It is shown rather than folded into Error:
	// "ran out of budget" and "failed" call for different reactions.
	SalvagedReason string

	// HasChild reports whether a child conversation exists to open.
	//
	// It is the flag the detail view keys on. Rendering the PARENT's transcript
	// when there is no child is the one failure this panel must never commit:
	// on screen, a parent's conversation shown under a child's name is
	// indistinguishable from the child's.
	HasChild bool
	// ChildBody is the child's rendered conversation, handed in by the caller.
	//
	// This package does not build it. Building it means walking the child's
	// transcript through the conversation document adapter, which lives in the
	// tui package — and this package is deliberately free of it, so the
	// inspector can be tested without a session, a provider, or a terminal.
	ChildBody string
	// ChildTruncated reports that ChildBody is a bounded prefix of the child's
	// conversation, so the detail can say the rest exists and is not shown.
	ChildTruncated bool
}

// agentsState is the Agents tab's navigation state.
type agentsState struct {
	// roster is the copied list, in the caller's order.
	roster []Agent
	// cursor is the row index, derived from selectedID on every SetAgents.
	cursor int
	// selectedID is the SOURCE OF TRUTH for selection. The cursor is a
	// presentation detail recomputed from it.
	selectedID int64
	// vanished records that the selected ID left the roster. The reader must be
	// told, because the roster on screen no longer contains the agent whose
	// detail they may be reading.
	vanished bool

	// detail is THIS tab's own scrollable body, and detailLabel is the heading
	// it renders under.
	//
	// Both are per-tab rather than shared with Changes and Context because the
	// three tabs write their content at different moments: a single shared view
	// holds whichever tab wrote last, so a child transcript could appear under
	// a diff's path, or be overwritten by a Context body before the Agents tab
	// drew again. One body per tab makes that mislabelling impossible.
	detail      *DetailView
	detailLabel string
}

// SetAgents replaces the roster, preserving a selection that still exists.
//
// The rules, in order:
//
//  1. A selected ID that is still present stays selected, whatever row it now
//     occupies. A status change is not navigation, and neither is a reorder.
//  2. A selected ID that is gone falls to the row now at the same index
//     (clamped), and is FLAGGED. Silently selecting a neighbour is how a reader
//     ends up studying an unrelated child.
//  3. With nothing selected before, the first row is selected so Enter does
//     something predictable.
//
// The roster is copied. Holding the caller's slice would let a later mutation
// change what is on screen without a refresh.
func (m *Model) SetAgents(agents []Agent) {
	roster := make([]Agent, len(agents))
	copy(roster, agents)
	m.agents.roster = roster

	prev := m.agents.selectedID
	switch {
	case len(roster) == 0:
		m.agents.selectedID = 0
		m.agents.cursor = 0
		m.agents.vanished = false
	case prev == 0:
		m.agents.cursor = 0
		m.agents.selectedID = roster[0].ID
		m.agents.vanished = false
	default:
		if idx := m.agentRowForID(prev); idx >= 0 {
			m.agents.cursor = idx
			m.agents.vanished = false
			return
		}
		idx := min(m.agents.cursor, len(roster)-1)
		idx = max(idx, 0)
		m.agents.cursor = idx
		m.agents.selectedID = roster[idx].ID
		m.agents.vanished = true
	}
}

// AgentsSnapshot returns the roster as held, copied so a caller cannot mutate
// the panel's own state through the returned slice.
func (m *Model) AgentsSnapshot() []Agent {
	out := make([]Agent, len(m.agents.roster))
	copy(out, m.agents.roster)
	return out
}

// agentRowForID returns the row index holding id, or -1.
func (m *Model) agentRowForID(id int64) int {
	for i, a := range m.agents.roster {
		if a.ID == id {
			return i
		}
	}
	return -1
}

// SelectedAgent returns the selected agent and whether one is selected.
func (m *Model) SelectedAgent() (Agent, bool) {
	if m.agents.selectedID == 0 {
		return Agent{}, false
	}
	if idx := m.agentRowForID(m.agents.selectedID); idx >= 0 {
		return m.agents.roster[idx], true
	}
	return Agent{}, false
}

// AgentIDSelected returns the selected runtime ID, or 0 when nothing is
// selected.
//
// The stop action needs the ID rather than the row: it dispatches into the
// runtime, and an index would have to be resolved again there against a roster
// that may have changed in between.
func (m *Model) AgentIDSelected() int64 { return m.agents.selectedID }

// AgentSelectionVanished reports that the selected agent left the roster, so
// the caller can label the detail as describing something no longer present.
func (m *Model) AgentSelectionVanished() bool { return m.agents.vanished }

// MoveAgentSelection moves the row cursor by delta, clamped to the roster.
//
// Landing on a real row clears the vanished flag: the reader is now on an agent
// that exists, and a stale "no longer present" label over a live row is its own
// kind of lie.
func (m *Model) MoveAgentSelection(delta int) {
	if delta == 0 || len(m.agents.roster) == 0 {
		return
	}
	idx := min(max(m.agents.cursor+delta, 0), len(m.agents.roster)-1)
	m.agents.cursor = idx
	m.agents.selectedID = m.agents.roster[idx].ID
	m.agents.vanished = false
}

// SelectedAgentRunning reports whether the SELECTED agent is still running.
//
// The stop action reads this. Binding stop to "any agent is running" is how a
// user stops an agent they were not looking at: the runtime must only ever be
// asked to cancel the child whose row is selected. It deliberately does not
// depend on a child transcript existing — a pipeline card with no child state
// is still live and cancellable.
func (m *Model) SelectedAgentRunning() bool {
	a, ok := m.SelectedAgent()
	return ok && a.Status == AgentRunning
}

// EnterAgent opens the selected agent's detail.
//
// It reports false when there is nothing to open, so a key handler can fall
// through to a different Enter meaning rather than swallowing the key.
//
// The target is scoped and identified by runtime ID, so re-entering the same
// agent is recognised as the same view: a second level for one view means Esc
// needs two presses to leave something one press entered.
func (m *Model) EnterAgent() bool {
	agent, ok := m.SelectedAgent()
	if !ok {
		return false
	}
	m.renderAgentDetail(agent)
	m.OpenTarget(Target{
		Kind:  TargetAgent,
		Scope: m.scope,
		ID:    strconv.FormatInt(agent.ID, 10),
	})
	return true
}

// AgentDetailOpen reports whether an agent's detail is on screen.
func (m *Model) AgentDetailOpen() bool {
	target, ok := m.ActiveTarget()
	return ok && target.Kind == TargetAgent
}

// SyncAgentDetail refreshes an OPEN agent detail to describe whichever agent is
// now selected, and reports whether it did anything.
//
// It is a no-op when no detail is open, and that is the whole reason it exists
// rather than the caller simply calling EnterAgent: moving the roster cursor
// must not OPEN a transcript. A key that both moved the cursor and opened a
// panel would make the panel impossible to avoid.
//
// The stack's top target is REPLACED rather than pushed. The detail is one
// view showing one child; following the cursor is not a sequence of views, and
// pushing would make Esc walk back through agents the reader only scrolled past.
func (m *Model) SyncAgentDetail() bool {
	if !m.AgentDetailOpen() {
		return false
	}
	agent, ok := m.SelectedAgent()
	if !ok {
		return false
	}
	m.renderAgentDetail(agent)
	if n := len(m.stack); n > 0 {
		m.stack[n-1] = Target{
			Kind:  TargetAgent,
			Scope: m.scope,
			ID:    strconv.FormatInt(agent.ID, 10),
		}
		m.active = m.stack[n-1]
	}
	return true
}

// agentsDetail reports the Agents tab's own body, allocating it on first use.
func (m *Model) agentsDetail() *DetailView {
	if m.agents.detail == nil {
		m.agents.detail = NewDetailView()
	}
	return m.agents.detail
}

// PageAgentDetail moves the child transcript body by whole viewports.
func (m *Model) PageAgentDetail(delta int) {
	m.syncAgentDetail()
	m.agentsDetail().Page(delta)
}

// AgentDetailTop jumps the child transcript to its first line.
func (m *Model) AgentDetailTop() {
	m.syncAgentDetail()
	m.agentsDetail().Top()
}

// AgentDetailBottom jumps the child transcript to its last line.
func (m *Model) AgentDetailBottom() {
	m.syncAgentDetail()
	m.agentsDetail().Bottom()
}

// AgentDetailScroll reports the body's scroll offset, so a caller can tell
// whether a key actually moved the transcript it was looking at.
func (m *Model) AgentDetailScroll() int { return m.agentsDetail().ScrollOffset() }

// syncAgentDetail sizes the detail body from the model's own recorded area.
//
// The View arm does this too, for the running app. It is repeated here because
// a caller driving the body directly (a key handler in a test, or a caller that
// scrolls before the first frame) must not be paging a viewport with no height
// — which would silently move nothing and look like a broken key.
func (m *Model) syncAgentDetail() {
	m.agentsDetail().Resize(m.width, m.height)
}

// renderAgentDetail fills the shared detail view with one agent.
//
// The body is NEVER the parent's conversation. When there is no child state,
// the panel says so in as many words and shows the pipeline metadata it does
// have — "no child transcript" is not "nothing to see".
func (m *Model) renderAgentDetail(agent Agent) {
	var b strings.Builder
	if agent.HasChild && agent.ChildBody != "" {
		b.WriteString(agent.ChildBody)
		if !strings.HasSuffix(agent.ChildBody, "\n") {
			b.WriteString("\n")
		}
	} else {
		b.WriteString("No child transcript available.\n")
		b.WriteString("\n")
		b.WriteString(agentMetadata(agent))
	}
	// Marked as no-longer-present when the roster dropped it, so a reader
	// returning to an already-open detail is told the agent is gone rather
	// than reading it as current.
	detail := m.agentsDetail()
	detail.SetNoLongerChanged(m.agents.vanished)
	// A child body the caller bounded is disclosed through the detail's own
	// truncation flag: the two kinds of "more" (scroll for it / it does not
	// exist) must not be conflated, and the detail already draws the line.
	detail.SetContent(b.String(), agent.ChildTruncated)
	// A whole transcript opens at its beginning. Following the end would hide
	// what the agent was asked to do, which is the first thing a reader wants.
	detail.Top()
	m.agents.detailLabel = agentDetailLabel(agent)
}

// agentDetailLabel names the agent whose detail is on screen.
func agentDetailLabel(agent Agent) string {
	label := "agent #" + strconv.FormatInt(agent.ID, 10)
	if agent.Label != "" {
		label += " " + agent.Label
	}
	if agent.Status == AgentRunning {
		label += " (running)"
	}
	return label
}

// agentMetadata renders the route and result facts about an agent.
//
// It exists for the no-child case, but it is built from the same fields the
// roster row uses so the two cannot disagree about which model ran.
func agentMetadata(agent Agent) string {
	var lines []string
	add := func(k, v string) {
		if v != "" {
			lines = append(lines, fmt.Sprintf("%-10s %s", k, v))
		}
	}
	add("role", agent.Role)
	add("model", agent.Model)
	add("provider", agent.Provider)
	if agent.Fallback {
		add("fallback", "yes — the role's primary model was unavailable")
	}
	add("elapsed", agent.Elapsed)
	if agent.ToolCalls > 0 {
		add("tools", strconv.Itoa(agent.ToolCalls))
	}
	add("salvage", agent.SalvagedReason)
	add("error", agent.Error)
	add("summary", agent.Summary)
	if len(lines) == 0 {
		return "No metadata was recorded for this agent.\n"
	}
	return strings.Join(lines, "\n") + "\n"
}

// viewAgents renders the Agents tab.
func (m *Model) viewAgents() string {
	width := max(m.width, 20)

	// The header explains the distinction from /agents, which is the one thing
	// a reader arriving here can get wrong. It is wrapped to the panel rather
	// than truncated at one line, because a half-sentence explanation is worse
	// than none — but it is still bounded, since a header that reflows the list
	// off the panel is a header that costs the reader the content.
	//
	// The header WRAPS, so its row count depends on the panel's width and is
	// measured rather than assumed.
	headerLines := make([]string, 0, len(agentsHeaderLines()))
	for _, line := range agentsHeaderLines() {
		headerLines = append(headerLines, wrapToWidth(line, width)...)
	}

	// An UNMEASURED panel renders the whole roster, following the same rule the
	// detail body uses for an unmeasured width: a caller that has not laid its
	// frame out gets the content rather than a window off a height nobody
	// measured.
	if m.height <= 0 {
		var b strings.Builder
		for _, line := range headerLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
		if len(m.agents.roster) == 0 {
			b.WriteString("\n")
			b.WriteString("No agents have run in this conversation yet.\n")
			return b.String()
		}
		b.WriteString("\n")
		for i, a := range m.agents.roster {
			cursor := "  "
			if i == m.agents.cursor {
				cursor = "▸ "
			}
			b.WriteString(cursor)
			b.WriteString(agentRowText(a, m.width))
			b.WriteString("\n")
		}
		if m.agents.vanished {
			b.WriteString("\n")
			b.WriteString("The agent you were on is no longer in the roster; showing its nearest neighbour.\n")
		}
		return b.String()
	}

	// The tab is BUDGETED end to end: the panel is joined into the frame as a
	// second column, and a join pads the shorter column to the taller one, so
	// anything emitted beyond m.height escapes into the frame and pushes the
	// status line off the bottom. Every row goes through a budget rather than
	// being counted by hand — the blank line before the body is a ROW, and an
	// uncounted one is how this panel used to emit more rows than the height it
	// recorded.
	//
	// The budget is WIDTH-aware too, so the empty-roster note and the vanished
	// note are clamped to the panel rather than wrapping into rows this budget
	// has already spent.
	rb := newRowBudget(m.height, m.width)
	// Rows are composed against the SAME width the budget bounds them to, so a
	// note cannot collapse to a single ellipsis cell on an unmeasured panel.
	noteWidth := budgetWidth(m.width)

	// The header WRAPS, so at a degenerate height it may not fully fit. A header
	// that is cut by the budget is still honest — the alternative is emitting
	// past the frame — but it must not consume the roster's or the body's rows.
	for _, line := range headerLines {
		rb.line(line)
	}
	if len(m.agents.roster) == 0 {
		switch {
		case rb.left() >= 2:
			rb.blank()
			rb.line("No agents have run in this conversation yet.")
		case rb.left() >= 1:
			// No room for the separator, but the sentence still fits. An empty
			// roster that renders as a blank panel under a header is
			// indistinguishable from a panel that failed to draw.
			rb.line("No agents have run in this conversation yet.")
		}
		return rb.String()
	}
	if rb.left() >= 2 {
		// The blank line before the roster, plus at least one roster row: a
		// separator with nothing under it is worse than no separator.
		rb.blank()
	}

	// The roster and the detail body SHARE what is left. The trailing notes are
	// reserved FIRST, because they are conditional and their rows must not be
	// handed to the list or the body.
	//
	// The reservation is a FLOOR: with fewer than three rows left the note
	// cannot be drawn whatever happens, so deducting its two rows as well would
	// cost the reader two rows of roster for a sentence they never see.
	rows := rb.left()
	if m.agents.vanished && rows >= 3 {
		rows -= 2 // blank + the note line
	}
	rows = max(rows, 1)

	bodyRows := 0
	if m.AgentDetailOpen() && rows >= 3 {
		bodyShare := max((rows-1)/2, 1)
		bodyRows = min(bodyShare, rows-2)
		rows -= bodyRows + 1
	}
	listRows := max(rows, 1)

	// The window reserves its own note rows, so they ride INSIDE the list's
	// share rather than being added to it.
	w := windowList(len(m.agents.roster), listRows, m.agents.cursor, m.State(TabAgents).Scroll, 2)
	if w.ShowAbove() {
		rb.line(aboveNote(w.Above(), noteWidth))
	}
	for i := w.Start; i < w.End; i++ {
		cursor := "  "
		if i == m.agents.cursor {
			cursor = "▸ "
		}
		rb.line(cursor + agentRowText(m.agents.roster[i], noteWidth))
	}
	if w.ShowBelow() {
		rb.line(belowNote(w.Below(), noteWidth))
	}

	if m.agents.vanished && rb.left() >= 2 {
		// The reader's agent left the roster. Say so rather than letting them
		// study a row that is no longer there. A separator plus its line is two
		// rows, so at a degenerate height the note is dropped rather than left
		// as an empty gap.
		rb.blank()
		rb.line("The agent you were on is no longer in the roster; showing its nearest neighbour.")
	}

	if m.AgentDetailOpen() && bodyRows > 0 && rb.left() >= 2 {
		detail := m.agentsDetail()
		detail.Resize(m.width, bodyRows)
		rb.blank()
		rb.body(detail.View(m.agents.detailLabel))
	}
	return rb.String()
}

// agentsHeaderLines is the header the Agents tab renders, in one place so its
// height can be MEASURED rather than guessed.
//
// It is a function rather than a package variable because the text is a product
// claim, not configuration: a caller that mutated a shared slice in place would
// silently change what the panel says.
func agentsHeaderLines() []string {
	return []string{
		"Runtime agents — what actually ran this session.",
		"(/agents shows the configured role routes, which is a different list.)",
	}
}

// agentRowText renders one roster row: status marker, id, label, route, timing
// and live activity.
//
// Every variable-length part is clamped to the panel's width in display CELLS.
// A row that overflows wraps, which shifts every row below it and pushes the
// panel's own chrome off the bottom — the reader then loses the thing they were
// reading because an agent had a long name.
//
// clampToWidth, not strutil.Truncate. Truncate cuts to N RUNES and THEN appends
// the ellipsis, so it returns N+1 cells and counts a CJK ideograph as one when
// it occupies two: a label of wide characters produced a roster row twice its
// budget, which is the exact overflow this bound exists to prevent.
func agentRowText(a Agent, width int) string {
	// Two cells are reserved for the cursor marker and its space. clampToWidth
	// budgets its own ellipsis INSIDE the width, so no third cell is needed —
	// the previous budget of width-3 was sized for strutil.Truncate's appended
	// ellipsis, which is no longer appended.
	budget := max(width-2, 1)
	parts := []string{
		agentStatusMarker(a.Status),
		"#" + strconv.FormatInt(a.ID, 10),
	}
	if a.Label != "" {
		parts = append(parts, a.Label)
	}
	if a.Role != "" {
		parts = append(parts, a.Role)
	}
	if a.Model != "" {
		model := a.Model
		if a.Provider != "" {
			model += " @ " + a.Provider
		}
		parts = append(parts, model)
	}
	if a.Fallback {
		parts = append(parts, "fallback")
	}
	if a.Status == AgentRunning {
		if a.CurrentTool != "" {
			parts = append(parts, "→"+a.CurrentTool)
		} else if a.ToolCalls > 0 {
			parts = append(parts, pluralTools(a.ToolCalls))
		}
		if a.Elapsed != "" {
			parts = append(parts, a.Elapsed)
		}
	} else {
		if a.ToolCalls > 0 {
			parts = append(parts, pluralTools(a.ToolCalls))
		}
		if a.Elapsed != "" {
			parts = append(parts, a.Elapsed)
		}
		if a.Error != "" {
			parts = append(parts, a.Error)
		} else if a.SalvagedReason != "" {
			parts = append(parts, "partial: "+a.SalvagedReason)
		}
	}
	line := strings.Join(parts, "  ")
	// An UNMEASURED panel (width 0) is not clamped at all, following the rule
	// every other renderer in this package uses.
	if width <= 0 {
		return line
	}
	return clampToWidth(line, budget)
}

// wrapToWidth breaks a line into display rows that each fit width CELLS.
//
// Cells, not runes. A rune is not a column: a CJK ideograph, an emoji and many
// combining forms occupy two, and a combining mark occupies none of its own. The
// earlier version measured `len([]rune(s))`, so ten CJK characters were treated
// as ten cells and emitted as twenty — a header twice its budget, which wrapped
// in the terminal and pushed the panel's chrome off the bottom of the screen.
// That is precisely the overflow this function exists to prevent, so measuring
// in the wrong unit made it fail at its one job for any non-ASCII text.
//
// It wraps on spaces where it can and mid-token where it must, because a single
// unbreakable token (a long path, a model name) must still be bounded. A token
// is split on GRAPHEME boundaries, never mid-cluster, so a wide character is
// never halved into two invalid fragments.
func wrapToWidth(s string, width int) []string {
	width = max(width, 1)
	if ansi.StringWidth(s) <= width {
		return []string{s}
	}
	var out []string

	// current is the row being built, as a string, so cells and bytes cannot
	// drift apart.
	current := ""
	flush := func() {
		if current != "" {
			out = append(out, current)
			current = ""
		}
	}
	currentCells := func() int { return ansi.StringWidth(current) }

	for _, word := range strings.Fields(s) {
		// A token that cannot fit on a row of its own is cut into pieces. It is
		// cut, not dropped: silently losing text is worse than an ugly break.
		for ansi.StringWidth(word) > width {
			flush()
			head, rest := splitAtCells(word, width)
			if head == "" {
				// The FIRST cluster is wider than the whole budget, so no amount
				// of splitting makes this word fit. Emit that one cluster on its
				// own and continue with the remainder: emitting the WHOLE word
				// (the obvious fallback) would put the entire token on one row,
				// which is the overflow this function exists to prevent, and
				// dropping it loses content. One over-wide cluster is the
				// smallest honest unit.
				g, _ := ansi.FirstGraphemeCluster(word, ansi.GraphemeWidth)
				if g == "" {
					break
				}
				head, rest = g, word[len(g):]
			}
			out = append(out, head)
			word = rest
		}
		if word == "" {
			continue
		}
		gap := 0
		if current != "" {
			gap = 1
		}
		if currentCells()+gap+ansi.StringWidth(word) > width {
			flush()
			gap = 0
		}
		if gap == 1 {
			current += " "
		}
		current += word
	}
	flush()
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// splitAtCells splits s into a head of at most cells display cells and the rest,
// breaking only on grapheme boundaries.
//
// It reports an empty head when the FIRST cluster alone is wider than the budget,
// so the caller can decide (it renders the cluster whole rather than looping).
func splitAtCells(s string, cells int) (head, rest string) {
	used := 0
	i := 0
	for i < len(s) {
		g, w := ansi.FirstGraphemeCluster(s[i:], ansi.GraphemeWidth)
		if g == "" {
			break
		}
		if w < 0 {
			w = 0
		}
		if used+w > cells {
			break
		}
		used += w
		i += len(g)
	}
	return s[:i], s[i:]
}

// pluralTools renders a tool-call count. "1 tools" reads as a rendering bug.
func pluralTools(n int) string {
	if n == 1 {
		return "1 tool"
	}
	return strconv.Itoa(n) + " tools"
}

// agentStatusMarker is the glyph-plus-word that distinguishes the states.
//
// A word, not only a glyph: NO_COLOR and a monochrome terminal are supported
// configurations, and a status that is only a colour is a status those users
// cannot read. The three words are also what makes a failed agent findable by
// scanning rather than by opening each row.
func agentStatusMarker(s AgentStatus) string {
	switch s {
	case AgentRunning:
		return "running"
	case AgentFailed:
		return "failed"
	default:
		return "done"
	}
}
