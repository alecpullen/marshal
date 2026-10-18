package tui

import (
	"fmt"
	"slices"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/picker"
)

// effortLevels returns the selectable reasoning-effort levels for the active
// route's model, in the order the provider offers them. Nil when the route
// has no effort control.
func (m *Model) effortLevels() []string {
	opts := m.modelThinkingOptions(m.state.ActiveRoute().Preset)
	if opts == nil {
		return nil
	}
	return opts.Levels
}

// effortCycle is the ordered set the picker and alt+e walk: "default"
// (clearing the override) followed by the route's levels.
func (m *Model) effortCycle() []string {
	return append([]string{""}, m.effortLevels()...)
}

// applyEffort sets the session's reasoning-effort override through the
// runtime-facing setter and reports the outcome. The TUI does not validate:
// the runtime owns the valid-level set and the off-boundary rule, and reports
// when the change takes effect. An empty (or "default") level clears the
// override.
func (m *Model) applyEffort(level string) {
	if level == "default" {
		level = ""
	}
	if m.effortSetter == nil {
		m.state.AddMessage(session.RoleSystem, "Effort changes are unavailable.", session.ContentTypePlain)
		return
	}
	applies, err := m.effortSetter(level)
	if err != nil {
		m.state.AddMessage(session.RoleSystem, err.Error(), session.ContentTypePlain)
		return
	}
	label := level
	if label == "" {
		label = "default"
	}
	when := "from the next turn"
	if applies == session.EffortAppliesNextStep {
		when = "from the next step"
	}
	m.state.AddMessage(session.RoleSystem, fmt.Sprintf("Effort set to %s (%s).", label, when), session.ContentTypePlain)
}

// cycleEffort advances the reasoning-effort override through "default" and
// the active route's levels, wrapping around, and applies the next entry.
func (m *Model) cycleEffort() {
	cycle := m.effortCycle()
	cur := m.state.EffortOverride().Level
	idx := slices.Index(cycle, cur)
	if idx < 0 {
		idx = 0
	}
	m.applyEffort(cycle[(idx+1)%len(cycle)])
	m.refreshViewport()
}

// openEffortPicker lists the effort cycle for selection, marking the current
// override. The picked level re-enters applyEffort via the picker routing
// switch, so the set/clear semantics stay in one place.
func (m *Model) openEffortPicker() {
	cycle := m.effortCycle()
	cur := m.state.EffortOverride().Level
	items := make([]picker.Item, 0, len(cycle))
	for _, level := range cycle {
		label := level
		value := level
		if level == "" {
			label = "default"
			// The picker routing switch drops an empty Value before it
			// reaches the effort case, so the clearing row carries the
			// literal "default" that applyEffort maps back to "".
			value = "default"
		}
		item := picker.Item{Label: label, Value: value}
		if level == cur {
			item.Badge = "●"
		}
		items = append(items, item)
	}
	m.openPicker("effort", "Reasoning effort", "", items, "")
}
