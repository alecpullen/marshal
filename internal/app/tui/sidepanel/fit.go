package sidepanel

// RenderState is how much room a section gets in the fitted rail.
type RenderState int

const (
	StateExpanded RenderState = iota
	StateClipped
	StateOneLine
	StateDropped
)

// SectionCost is a section's height in each state, plus its collapse
// priority. Natural and Clipped include the title row. Clipped is 0 for
// sections with no intermediate state.
type SectionCost struct {
	Priority int
	Natural  int
	Clipped  int
}

// height is the row cost of one section in the given state.
func (c SectionCost) height(s RenderState) int {
	switch s {
	case StateExpanded:
		return c.Natural
	case StateClipped:
		return c.Clipped
	case StateOneLine:
		return 1
	default:
		return 0
	}
}

// fit assigns each section a render state so the stack fits budget rows.
// Sections are stepped down (expanded → clipped → one-line) worst-priority
// first, then dropped worst-priority first. At least one section always
// survives, so the rail is never empty.
//
// This is statusLeftSegments' priority drop rotated ninety degrees, with an
// intermediate clipped state because vertical space is coarser than
// horizontal.
func fit(costs []SectionCost, budget int) []RenderState {
	states := make([]RenderState, len(costs))
	if len(costs) == 0 {
		return states
	}

	// Step down: worst priority (highest number) first.
	for total(costs, states) > budget {
		i := worst(costs, states, func(s RenderState) bool { return s < StateOneLine })
		if i < 0 {
			break
		}
		states[i] = next(states[i], costs[i].Clippable())
	}

	// Drop: worst priority first, but never the last survivor.
	for total(costs, states) > budget && survivors(states) > 1 {
		i := worst(costs, states, func(s RenderState) bool { return s != StateDropped })
		if i < 0 {
			break
		}
		states[i] = StateDropped
	}
	return states
}

// Clippable reports whether this cost has a distinct clipped height.
func (c SectionCost) Clippable() bool { return c.Clipped > 0 && c.Clipped < c.Natural }

// next is the state one step down from s.
func next(s RenderState, clippable bool) RenderState {
	if s == StateExpanded && clippable {
		return StateClipped
	}
	return StateOneLine
}

// total is the stack height: section heights plus one blank row between
// each pair of surviving sections.
func total(costs []SectionCost, states []RenderState) int {
	sum, n := 0, 0
	for i, s := range states {
		if s == StateDropped {
			continue
		}
		sum += costs[i].height(s)
		n++
	}
	if n > 1 {
		sum += n - 1
	}
	return sum
}

// worst returns the index of the highest-priority-number section matching
// eligible, or -1. Ties break toward the later index, making the result
// independent of iteration direction.
func worst(costs []SectionCost, states []RenderState, eligible func(RenderState) bool) int {
	best := -1
	for i := range costs {
		if !eligible(states[i]) {
			continue
		}
		if best < 0 || costs[i].Priority >= costs[best].Priority {
			best = i
		}
	}
	return best
}

func survivors(states []RenderState) int {
	n := 0
	for _, s := range states {
		if s != StateDropped {
			n++
		}
	}
	return n
}
