package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/viewmodel"
)

// livebox_view.go wires the live box to the model: it snapshots the model for
// livebox.Plan, plans once per frame, and routes the box's height and clicks.

// startHeldWindow opens a new held window: a finished item that ended after
// this instant keeps its tray row until the next prompt. A finished plan seen
// now is dismissed, so it is one row until the next prompt.
func (m *Model) startHeldWindow() {
	m.heldSince = m.now()
	if allTasksCompleted(m.band.Tasks) {
		m.planDismissed = planFingerprint(m.band.Tasks)
	}
}

// allTasksCompleted reports whether every item of a non-empty list is done.
func allTasksCompleted(tasks []viewmodel.BandTask) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if t.Status != "completed" {
			return false
		}
	}
	return true
}

// planFingerprint identifies a plan list by its items' identity and status, so
// a finished list seen at the last turn start can be left out of the tray.
func planFingerprint(tasks []viewmodel.BandTask) string {
	var b strings.Builder
	for _, t := range tasks {
		b.WriteString(t.ID)
		b.WriteByte(0)
		b.WriteString(t.Content)
		b.WriteByte(0)
		b.WriteString(t.Status)
		b.WriteByte('\n')
	}
	return b.String()
}

// boxInput snapshots the model for livebox.Plan.
func (m Model) boxInput() livebox.Input {
	now := m.now()
	st := m.sess.Status()
	in := livebox.Input{
		Width:    max(m.leftWidth, 1),
		MaxRows:  m.boxMaxRows(),
		Now:      now,
		Spinner:  m.turnSpinnerFrame(),
		Previous: m.workPrevious,
		Expanded: m.workExpanded,
		Cursor:   m.boxCursor(),
	}
	// An active SDD run or swarm is the tray's first row. The turn state on the
	// status line still says "working"; the run's own summary is this row.
	if m.busy && !m.turnStartedAt.IsZero() {
		sdd := m.state.SDDProgress()
		swarm := m.state.SwarmProgress()
		switch {
		case sdd.Active:
			title := sdd.PlanName
			if title == "" {
				title = "plan run"
			}
			in.Run = &livebox.RunRow{
				Title:     title,
				Summary:   runPanelSummaryText(sdd, now, 60),
				StartedAt: m.turnStartedAt,
			}
		case swarm.Active:
			in.Run = &livebox.RunRow{
				Title:     "swarm",
				Summary:   swarmStripText(swarm),
				StartedAt: m.turnStartedAt,
			}
		}
	}
	for _, it := range m.workSnapshot.Items {
		if viewmodel.WorkActive(it) {
			in.Work = append(in.Work, it)
			continue
		}
		if h, ok := m.heldItem(it); ok {
			in.Held = append(in.Held, h)
		}
	}
	// A subagent's card carries its route provenance and, while it runs, its
	// child's newest narration headline. Held agents keep the provenance too:
	// the finished row still says which model ran the work.
	agents := make([]viewmodel.WorkItem, 0, len(in.Work)+len(in.Held))
	agents = append(agents, in.Work...)
	for _, h := range in.Held {
		agents = append(agents, h.Item)
	}
	for _, it := range agents {
		if it.Kind != "agent" {
			continue
		}
		// The item's SourceID is the child session's subagent ID.
		id, err := strconv.ParseInt(it.SourceID, 10, 64)
		if err != nil {
			continue
		}
		var v acpwire.Subagent
		found := false
		for _, view := range st.Subagents {
			if view.ID == id {
				v, found = view, true
				break
			}
		}
		if !found {
			continue
		}
		if in.Agents == nil {
			in.Agents = map[string]livebox.AgentMeta{}
		}
		in.Agents[it.TaskKey] = livebox.AgentMeta{
			Model:    v.Model,
			Provider: v.Provider,
			Fallback: v.Fallback,
		}
		if !viewmodel.WorkActive(it) || v.Headline == "" {
			continue
		}
		if in.Headlines == nil {
			in.Headlines = map[string]string{}
		}
		in.Headlines[it.TaskKey] = v.Headline
	}
	// A finished plan is one row until the next prompt: once the list seen at
	// the last turn start is dismissed, the tray leaves it out entirely.
	if len(m.band.Tasks) > 0 && !(allTasksCompleted(m.band.Tasks) && planFingerprint(m.band.Tasks) == m.planDismissed) {
		for _, t := range m.band.Tasks {
			in.Plan = append(in.Plan, livebox.PlanItem{
				ID:        t.ID,
				Content:   t.Content,
				Status:    t.Status,
				StartedAt: t.StartedAt,
				First:     t.First,
			})
		}
	}
	if m.band.Stale {
		in.Stale = m.band.StaleAge
	}
	return in
}

// activateWorkRow acts on a work row: a running item toggles its card, and a
// held one scrolls to the completion event that finished it. When that event
// has been trimmed or rolled out of the transcript, the row says so rather
// than swallowing the key.
func (m *Model) activateWorkRow(key string) tea.Cmd {
	if it, ok := m.workItemByKey(key); ok && viewmodel.WorkActive(it) {
		if m.workExpanded == key {
			m.workExpanded = ""
		} else {
			m.workExpanded = key
		}
		m.invalidateTranscript()
		m.refreshViewport()
		return nil
	}
	if ref, ok := m.completions[key]; ok && m.scrollToNode(ref.node) {
		return nil
	}
	return m.setFlash("That event is no longer in the transcript")
}

// heldItem is a finished item's held row, when it ended inside the current
// held window. A foreground-attached command is never held: it stays an
// ordinary tool row. The row's words come from the completion event, so the
// tray and history always agree; until the event exists it falls back to the
// item's own state, with no summary.
func (m Model) heldItem(it viewmodel.WorkItem) (livebox.HeldItem, bool) {
	if it.Kind == "command" && it.Attachment == "foreground" {
		return livebox.HeldItem{}, false
	}
	switch it.State {
	case "completed", "failed", "stopped", "unknown":
	default:
		return livebox.HeldItem{}, false
	}
	if !it.EndedAt.After(m.heldSince) {
		return livebox.HeldItem{}, false
	}
	h := livebox.HeldItem{
		Item:     it,
		Duration: max(it.EndedAt.Sub(it.StartedAt), 0),
	}
	if ref, ok := m.completions[it.TaskKey]; ok && ref.info != nil {
		h.Outcome = ref.info.Outcome
		h.Failed = ref.info.Failed
		h.Summary = ref.info.Summary
		return h, true
	}
	switch it.State {
	case "completed":
		h.Outcome = "done"
	case "failed":
		h.Outcome = "failed"
	case "stopped":
		h.Outcome = "stopped"
	default:
		h.Outcome = "outcome unknown"
	}
	h.Failed = h.Outcome != "done"
	return h, true
}

// boxCursor is the browse cursor as a box target. A box stop has no transcript
// node, so the box is told directly which row is the cursor and paints it.
func (m Model) boxCursor() livebox.Target {
	if !m.browsing {
		return livebox.Target{}
	}
	t, _ := boxTargetOf(m.cursor)
	return t
}

// boxStopID is the synthetic browse identity of a box stop. The box's stops
// are not transcript nodes, so the ID carries the stop's kind and key instead
// of a node key.
func boxStopID(t livebox.Target) viewmodel.NodeID {
	return viewmodel.NodeID{Kind: viewmodel.KindPassthrough, Key: "box:" + boxKindName(t.Kind) + ":" + t.Key}
}

// boxTargetOf recovers the box stop a browse cursor ID names.
func boxTargetOf(id viewmodel.NodeID) (livebox.Target, bool) {
	if id.Kind != viewmodel.KindPassthrough {
		return livebox.Target{}, false
	}
	if !strings.HasPrefix(id.Key, "box:") {
		return livebox.Target{}, false
	}
	name, key, ok := strings.Cut(strings.TrimPrefix(id.Key, "box:"), ":")
	if !ok {
		return livebox.Target{}, false
	}
	switch name {
	case "work":
		return livebox.Target{Kind: livebox.TargetWork, Key: key}, true
	case "more":
		return livebox.Target{Kind: livebox.TargetMore, Key: key}, true
	case "plan":
		return livebox.Target{Kind: livebox.TargetPlan, Key: key}, true
	}
	return livebox.Target{}, false
}

// boxKindName is the word boxStopID writes into a stop's ID.
func boxKindName(k livebox.TargetKind) string {
	switch k {
	case livebox.TargetWork:
		return "work"
	case livebox.TargetMore:
		return "more"
	case livebox.TargetPlan:
		return "plan"
	}
	return "none"
}

// boxLayout returns this frame's layout: the one viewString planned, or a
// fresh plan outside a frame (key handling, clicks).
func (m Model) boxLayout() livebox.Layout {
	if m.boxMemo != nil {
		return *m.boxMemo
	}
	return livebox.Plan(m.boxInput())
}

// boxRows is the box's height for the frame budget, read from the same layout
// the renderer paints. A full-frame panel owns the whole left column, so the
// box takes nothing.
func (m Model) boxRows() int {
	if m.dock.FullFrameOpen() {
		return 0
	}
	return len(m.boxLayout().Rows)
}

// boxMaxRows is the row budget for the box: at most two fifths of the frame,
// and never so many that the transcript floor or the input area is lost.
func (m Model) boxMaxRows() int {
	if m.dock.FullFrameOpen() {
		return 0
	}
	return max(min(m.height*2/5, m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-statusLineRows-m.dockRows()-m.inputChromeRows()-1-minTranscriptRows), 1)
}

// boxTop is the screen row of the box's first line: the hint row, the
// breadcrumb row and the whole transcript viewport sit above it.
func (m *Model) boxTop() int {
	return m.scrollHintRows() + m.breadcrumbRows() + m.viewport.Height()
}

// handleBoxClick acts on the box row under a left click. Every click inside
// the box's rectangle is consumed — falling through to the transcript would
// toggle whatever block happens to be under the same screen row.
func (m *Model) handleBoxClick(msg tea.Mouse) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	if msg.X < 0 || msg.X >= m.leftWidth {
		return nil, false
	}
	if m.dock.FullFrameOpen() {
		return nil, false
	}
	layout := m.boxLayout()
	if len(layout.Rows) == 0 {
		return nil, false
	}
	top := m.boxTop()
	if msg.Y < top || msg.Y >= top+len(layout.Rows) {
		return nil, false
	}
	// A border line has no target, and an inner row's text starts past the
	// outline's left border.
	target := boxRowTarget(layout.Rows[msg.Y-top], msg.X-livebox.Inset)
	switch target.Kind {
	case livebox.TargetWork:
		return m.activateWorkRow(target.Key), true
	case livebox.TargetMore:
		m.openWork()
	case livebox.TargetPlan:
		m.scrollToPlanItem(target.Key)
	}
	return nil, true
}

// boxRowTarget is the target under a click at cell column col of the row's
// text: a stacked plan row draws two entries, and the click belongs to the
// last one that starts at or before it.
func boxRowTarget(r livebox.Row, col int) livebox.Target {
	for i := len(r.Also) - 1; i >= 0; i-- {
		if r.Also[i].Col <= col {
			return r.Also[i].Target
		}
	}
	return r.Target
}

// scrollToPlanItem jumps to the first beat bound to a plan item, keyed by the
// item's ID (or its "#<position>" slot, 1-based, when it has none).
func (m *Model) scrollToPlanItem(key string) {
	plan := m.boxInput().Plan
	for i, it := range plan {
		if it.ID != key && "#"+strconv.Itoa(i+1) != key {
			continue
		}
		m.scrollToNode(it.First)
		return
	}
}

// scrollToNode reveals a transcript node by scrolling the viewport to the
// first line of a region that targets it, and reports whether it was found.
func (m *Model) scrollToNode(id viewmodel.NodeID) bool {
	if id == (viewmodel.NodeID{}) {
		return false
	}
	for _, r := range m.nodeRegions {
		if r.target.node != id {
			continue
		}
		m.viewportFollow = false
		m.viewport.SetYOffset(r.startLine)
		return true
	}
	return false
}
