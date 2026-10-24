package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/livebox"
	"marshal/internal/viewmodel"
)

// livebox_view.go wires the live box to the model: it snapshots the model for
// livebox.Plan, plans once per frame, renders the live part of the transcript
// into the box, and routes the box's height and clicks.
//
// History renders in the viewport (refreshViewport renders it); the live part
// — the beat in flight and the reasoning streaming under it — never enters the
// viewport's content, because it changes with the clock and the spinner.

// renderLive renders the live part of the transcript at width, in order, and
// records which node owns each line. It goes through renderNode so a live beat
// renders exactly as history would draw it — and so nodeRenderHook still fires
// for it, which is what keeps an unchanged history from being re-rendered on a
// spinner tick.
func (m *Model) renderLive(live []*viewmodel.Node, c *stepRenderCtx, width int, themeSig uint64) livebox.Live {
	var out livebox.Live
	sawBeat := false
	for _, n := range live {
		text, subs := m.renderNode(n, c, width, themeSig)
		if text == "" {
			continue
		}
		lines := strings.Split(text, "\n")
		targets := make([]viewmodel.NodeID, len(lines))
		for i := range targets {
			targets[i] = n.ID
		}
		for _, sr := range subs {
			for i := max(sr.start, 0); i < min(sr.end, len(lines)); i++ {
				targets[i] = sr.id
			}
		}
		if n.Kind == viewmodel.KindStep && !sawBeat {
			sawBeat = true
			// The compact form is the beat's first line and the call it is
			// running right now: the last tool row it drew.
			out.Short = append(out.Short, lines[0])
			out.ShortTargets = append(out.ShortTargets, targets[0])
			if id, ok := lastToolChild(n); ok {
				for _, sr := range subs {
					if sr.id != id {
						continue
					}
					for i := max(sr.start, 0); i < min(sr.end, len(lines)); i++ {
						out.Short = append(out.Short, lines[i])
						out.ShortTargets = append(out.ShortTargets, targets[i])
					}
				}
			}
		}
		if n.ID == viewmodel.LiveThinkingID {
			// The streaming reasoning's head line, so the compact form keeps
			// saying what the model is thinking about. With no beat, that
			// line is the whole of Short.
			out.Short = append(out.Short, lines[0])
			out.ShortTargets = append(out.ShortTargets, targets[0])
		}
		out.Full = append(out.Full, lines...)
		out.Targets = append(out.Targets, targets...)
	}
	return out
}

// lastToolChild is the newest call the beat drew a row for.
func lastToolChild(n *viewmodel.Node) (viewmodel.NodeID, bool) {
	for i := len(n.Children) - 1; i >= 0; i-- {
		if n.Children[i].Kind == viewmodel.KindTool {
			return n.Children[i].ID, true
		}
	}
	return viewmodel.NodeID{}, false
}

// boxInput snapshots the model for livebox.Plan.
func (m Model) boxInput() livebox.Input {
	now := m.now()
	in := livebox.Input{
		Width:    max(m.leftWidth, 1),
		MaxRows:  m.boxMaxRows(),
		Now:      now,
		Busy:     m.busy && !m.turnStartedAt.IsZero(),
		Spinner:  m.turnSpinnerFrame(),
		Live:     m.live,
		Previous: m.workPrevious,
		Expanded: m.workExpanded,
		Mode:     m.liveBoxMode,
		Cursor:   m.boxCursor(),
	}
	if in.Busy {
		elapsed := formatElapsed(max(now.Sub(m.turnStartedAt), 0))
		sdd := m.state.SDDProgress()
		swarm := m.state.SwarmProgress()
		switch {
		case sdd.Active:
			in.State = spinnerLabel(in.Spinner, runPanelSummaryText(sdd, now, 60)) + " " + elapsed
		case swarm.Active:
			in.State = spinnerLabel(in.Spinner, swarmStripText(swarm)) + " " + elapsed
		default:
			in.State = spinnerLabel(in.Spinner, "working "+elapsed)
			if act := m.state.Activity(); spinnerShowsLabel(act.Kind) && act.Label != "" {
				if label := m.state.PinnedSpinnerLabel(act); label != "" {
					in.State += " · " + label
				}
			}
		}
	}
	if len(m.band.Tasks) > 0 {
		in.Hints = append(in.Hints, "Ctrl+T plan")
	}
	if in.Busy {
		in.Hints = append(in.Hints, "Ctrl+C ×2 stop")
	}
	for _, it := range m.workSnapshot.Items {
		if viewmodel.WorkActive(it) {
			in.Work = append(in.Work, it)
		}
	}
	for _, it := range in.Work {
		// Only a subagent's row carries a headline; its TaskKey names the
		// item, its SourceID the child session.
		if it.Kind != "agent" {
			continue
		}
		id, err := strconv.ParseInt(it.SourceID, 10, 64)
		if err != nil {
			continue
		}
		v, ok := m.state.Subagent(id)
		if !ok || v.Child == nil {
			continue
		}
		if in.Headlines == nil {
			in.Headlines = map[string]string{}
		}
		in.Headlines[it.TaskKey] = subagentHeadline(v.Child)
	}
	for _, t := range m.band.Tasks {
		in.Plan = append(in.Plan, livebox.PlanItem{
			ID:        t.ID,
			Content:   t.Content,
			Status:    t.Status,
			StartedAt: t.StartedAt,
			First:     t.First,
		})
	}
	if m.band.Stale {
		in.Stale = m.band.StaleAge
	}
	return in
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
	target := layout.Rows[msg.Y-top].Target
	switch target.Kind {
	case livebox.TargetLive:
		// The reasoning region scrolls with the wheel; a click on it does
		// not fold the stream away mid-thought.
		if target.Node == viewmodel.LiveThinkingID {
			return nil, true
		}
		m.toggleExpanded(target.Node)
		m.invalidateTranscript()
		m.refreshViewport()
	case livebox.TargetWork:
		if m.workExpanded == target.Key {
			m.workExpanded = ""
		} else {
			m.workExpanded = target.Key
		}
		m.invalidateTranscript()
		m.refreshViewport()
	case livebox.TargetMore:
		m.openWork()
	case livebox.TargetPlan:
		m.scrollToPlanItem(target.Key)
	}
	return nil, true
}

// scrollToPlanItem jumps to the first beat bound to a plan item, keyed by the
// item's ID (or its "#<index>" slot when it has none).
func (m *Model) scrollToPlanItem(key string) {
	plan := m.boxInput().Plan
	for i, it := range plan {
		if it.ID != key && "#"+strconv.Itoa(i) != key {
			continue
		}
		m.scrollToNode(it.First)
		return
	}
}

// scrollToNode reveals a transcript node by scrolling the viewport to the
// first line of a region that targets it, and reports whether it was found.
// The live part has no viewport region any more: it is in the box.
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
