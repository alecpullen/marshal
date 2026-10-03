package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/tui/stack"
)

// density is how much of a node the transcript shows. The global level is
// m.density, cycled with Ctrl+G; a node can carry its own override, and a node
// without one inherits its parent's effective level.
type density int

const (
	densityOutline density = iota // one row per step; tool rows hidden
	densitySteps                  // the default: step + collapsed tool rows
	densityFull                   // everything expanded
)

func (d density) String() string {
	switch d {
	case densityOutline:
		return "outline"
	case densityFull:
		return "full"
	}
	return "steps"
}

// parseDensity reads a configured level. Anything unrecognised is "steps": the
// config diagnostic names the bad value, the TUI just carries on.
func parseDensity(s string) density {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "outline":
		return densityOutline
	case "full":
		return densityFull
	}
	return densitySteps
}

// nextGlobal is the Ctrl+G ladder: outline → steps → full → outline.
func (d density) nextGlobal() density {
	switch d {
	case densityOutline:
		return densitySteps
	case densitySteps:
		return densityFull
	}
	return densityOutline
}

// nextOverride is Enter's cycle for one node: steps → full → outline → steps.
func (d density) nextOverride() density {
	switch d {
	case densitySteps:
		return densityFull
	case densityFull:
		return densityOutline
	}
	return densitySteps
}

// expandKey scopes a node ID to the transcript it is shown in. Every session
// numbers its own steps from 1, so without the scope, expanding step 3 of the
// main transcript would also expand step 3 of a subagent's, and toggling it
// there would flip the main one.
func (m *Model) expandKey(id stack.NodeID) stack.NodeID {
	if v, ok := m.drilledInto(); ok {
		return stack.NodeID{Kind: id.Kind, Key: fmt.Sprintf("sub%d/%s", v.ID, id.Key)}
	}
	return id
}

// override returns the node's own density, if the user set one.
func (m *Model) override(id stack.NodeID) (density, bool) {
	d, ok := m.nodeDensity[m.expandKey(id)]
	return d, ok
}

// densityOf resolves a node's effective level: its override, else what it
// inherits from its parent.
func (m *Model) densityOf(id stack.NodeID, inherited density) density {
	if d, ok := m.override(id); ok {
		return d
	}
	return inherited
}

// isExpanded reports whether a node is shown in full, ignoring ancestors'
// overrides. The renderer resolves inheritance itself; this is for callers
// that only have an ID.
func (m *Model) isExpanded(id stack.NodeID) bool {
	return m.densityOf(id, m.density) == densityFull
}

// isToolExpanded is isExpanded for an in-flight tool row. A running call
// stays collapsed unless clicked, whatever the global level says: its output
// is a live tail, not history. The override carries over when the call
// settles, because a call's node ID is the same before and after.
func (m *Model) isToolExpanded(id stack.NodeID, live bool) bool {
	if live {
		d, ok := m.override(id)
		return ok && d == densityFull
	}
	return m.isExpanded(id)
}

// shownDensity is the level a node was last drawn at: what a click or Enter
// cycles from.
func (m *Model) shownDensity(id stack.NodeID) density {
	if d, ok := m.override(id); ok {
		return d
	}
	if d, ok := m.effDensity[m.expandKey(id)]; ok {
		return d
	}
	return m.density
}

func (m *Model) setOverride(id stack.NodeID, d density) {
	if m.nodeDensity == nil {
		m.nodeDensity = map[stack.NodeID]density{}
	}
	m.nodeDensity[m.expandKey(id)] = d
}

// toggleExpanded is the click action. A leaf row (a tool call, a thinking
// row, a card) flips between steps and full: cycling it to outline would hide
// the row the user just clicked, leaving nothing to click again. Steps and
// tasks take the whole ladder.
func (m *Model) toggleExpanded(id stack.NodeID) {
	if m.isLiveToolNode(id) {
		d, ok := m.override(id)
		if ok && d == densityFull {
			m.setOverride(id, densitySteps)
		} else {
			m.setOverride(id, densityFull)
		}
		return
	}
	cur := m.shownDensity(id)
	if id.Kind == stack.KindStep || id.Kind == stack.KindTask {
		m.setOverride(id, cur.nextOverride())
		return
	}
	if cur == densityFull {
		m.setOverride(id, densitySteps)
	} else {
		m.setOverride(id, densityFull)
	}
}

// cycleDensity is Enter in browse mode: the node's own override walks
// steps → full → outline → steps.
func (m *Model) cycleDensity(id stack.NodeID) {
	if m.isLiveToolNode(id) {
		m.toggleExpanded(id)
		return
	}
	m.setOverride(id, m.shownDensity(id).nextOverride())
}

func (m *Model) isLiveToolNode(id stack.NodeID) bool {
	if id.Kind != stack.KindTool {
		return false
	}
	st, _ := m.transcriptSource()
	if st == nil {
		return false
	}
	for _, a := range st.ActiveToolCalls() {
		if stack.ActiveToolID(a) == id {
			return true
		}
	}
	return false
}

// recordDensity notes the level a node was just drawn at.
func (m *Model) recordDensity(id stack.NodeID, d density) {
	if m.effDensity == nil {
		m.effDensity = map[stack.NodeID]density{}
	}
	m.effDensity[m.expandKey(id)] = d
}
