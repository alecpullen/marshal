package inspector

import (
	"fmt"
	"strconv"
	"strings"

	"marshal/internal/strutil"
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

// PageAgentDetail moves the child transcript body by whole viewports.
func (m *Model) PageAgentDetail(delta int) {
	m.syncAgentDetail()
	m.detail.Page(delta)
}

// AgentDetailTop jumps the child transcript to its first line.
func (m *Model) AgentDetailTop() {
	m.syncAgentDetail()
	m.detail.Top()
}

// AgentDetailBottom jumps the child transcript to its last line.
func (m *Model) AgentDetailBottom() {
	m.syncAgentDetail()
	m.detail.Bottom()
}

// AgentDetailScroll reports the body's scroll offset, so a caller can tell
// whether a key actually moved the transcript it was looking at.
func (m *Model) AgentDetailScroll() int { return m.detail.ScrollOffset() }

// syncAgentDetail sizes the detail body from the model's own recorded area.
//
// The View arm does this too, for the running app. It is repeated here because
// a caller driving the body directly (a key handler in a test, or a caller that
// scrolls before the first frame) must not be paging a viewport with no height
// — which would silently move nothing and look like a broken key.
func (m *Model) syncAgentDetail() {
	m.detail.Resize(m.width, m.height)
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
	m.detail.SetNoLongerChanged(m.agents.vanished)
	// A child body the caller bounded is disclosed through the detail's own
	// truncation flag: the two kinds of "more" (scroll for it / it does not
	// exist) must not be conflated, and the detail already draws the line.
	m.detail.SetContent(b.String(), agent.ChildTruncated)
	// A whole transcript opens at its beginning. Following the end would hide
	// what the agent was asked to do, which is the first thing a reader wants.
	m.detail.Top()
	m.detailLabel = agentDetailLabel(agent)
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
	var b strings.Builder
	// The header explains the distinction from /agents, which is the one thing
	// a reader arriving here can get wrong. It is wrapped to the panel rather
	// than truncated at one line, because a half-sentence explanation is worse
	// than none — but it is still bounded, since a header that reflows the list
	// off the panel is a header that costs the reader the content.
	for _, line := range []string{
		"Runtime agents — what actually ran this session.",
		"(/agents shows the configured role routes, which is a different list.)",
	} {
		for _, wrapped := range wrapToWidth(line, width) {
			b.WriteString(wrapped)
			b.WriteString("\n")
		}
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
		// The reader's agent left the roster. Say so rather than letting them
		// study a row that is no longer there.
		b.WriteString("\n")
		b.WriteString("The agent you were on is no longer in the roster; showing its nearest neighbour.\n")
	}

	if m.AgentDetailOpen() {
		b.WriteString("\n")
		b.WriteString(m.detail.View(m.detailLabel))
	}
	return b.String()
}

// agentRowText renders one roster row: status marker, id, label, route, timing
// and live activity.
//
// Every variable-length part is truncated to the panel's width. A row that
// overflows wraps, which shifts every row below it and pushes the panel's own
// chrome off the bottom — the reader then loses the thing they were reading
// because an agent had a long name.
func agentRowText(a Agent, width int) string {
	// Three cells are reserved, not two: the cursor marker and its space, plus
	// the ellipsis strutil.Truncate appends when it cuts. Budgeting two would
	// let marker + cut text + ellipsis add up to width+1 — the exact overflow
	// the bound exists to prevent.
	budget := max(width-3, 20)
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
	return strutil.Truncate(line, budget, true)
}

// wrapToWidth breaks a line into display rows that each fit width cells.
//
// It wraps on spaces where it can and mid-word where it must, because a single
// unbreakable token (a long path, a model name) must still be bounded: letting
// it through would produce the one row the panel cannot render, and the row
// below it would be pushed off the bottom of the panel.
func wrapToWidth(s string, width int) []string {
	width = max(width, 1)
	if len([]rune(s)) <= width {
		return []string{s}
	}
	var out []string
	line := make([]rune, 0, width)
	flush := func() {
		if len(line) > 0 {
			out = append(out, string(line))
			line = line[:0]
		}
	}
	for _, word := range strings.Fields(s) {
		runes := []rune(word)
		// A word that cannot fit on a line of its own is cut into pieces. It is
		// cut, not dropped: silently losing part of the text is worse than an
		// ugly break.
		for len(runes) > width {
			flush()
			out = append(out, string(runes[:width]))
			runes = runes[width:]
		}
		need := len(runes)
		if len(line) > 0 {
			need++
		}
		if len(line)+need > width {
			flush()
		}
		if len(line) > 0 {
			line = append(line, ' ')
		}
		line = append(line, runes...)
	}
	flush()
	if len(out) == 0 {
		return []string{""}
	}
	return out
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
