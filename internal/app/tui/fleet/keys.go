package fleet

import (
	tea "charm.land/bubbletea/v2"

	"marshal/web/bridge/contract"
)

// handleKey applies one key press. While the filter input is open it owns the
// printable keys, so a letter typed into the filter never acts on the screen.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c quits from everywhere, the filter input included.
	if msg.String() == "ctrl+c" {
		return m, m.quit()
	}
	if m.filtering {
		if msg.Code == tea.KeyTab {
			// Tab leaves the filter, keeping its text, and moves the focus
			// on: the panel keys answer again.
			m.filtering = false
			m.cycleFocus(msg.Mod.Contains(tea.ModShift))
			return m, nil
		}
		return m.handleFilterKey(msg)
	}

	if msg.Code == tea.KeyTab {
		m.cycleFocus(msg.Mod.Contains(tea.ModShift))
		return m, nil
	}

	// The cursor keys move whichever of the two list panels has focus.
	switch msg.Code {
	case tea.KeyDown:
		m.moveCursor(1)
		return m, nil
	case tea.KeyUp:
		m.moveCursor(-1)
		return m, nil
	case tea.KeyEnter:
		// The filter has already taken Enter above, so this one opens.
		return m.openSelected()
	case tea.KeyEsc:
		m.zoom = false
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, m.quit()
	case "j":
		m.moveCursor(1)
	case "k":
		m.moveCursor(-1)
	case "1":
		m.tab = 1
	case "3":
		m.tab = 3
	case "z":
		m.zoom = !m.zoom
	case "/":
		// The filter is the Agents panel's, so it starts there.
		m.focus = focusAgents
		m.filtering = true
	case "?":
		m.help = !m.help
	}
	return m, nil
}

// openSelected ends the program with the selected row's target, so the loop
// can attach to it. A row with nothing to open — an intake item, or no
// selection — leaves the screen running: plan 5 gives those items approve and
// deny.
func (m Model) openSelected() (tea.Model, tea.Cmd) {
	_, rows := m.agentLines()
	target := m.openTarget(rows)
	if target == (Target{}) {
		return m, nil
	}
	m.target = target
	return m, m.quit()
}

// handleFilterKey applies one key press while the Agents filter input is open:
// printable characters extend the filter, backspace shortens it, enter keeps it
// and leaves the input, and esc clears it and leaves the input.
func (m Model) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEnter:
		m.filtering = false
		return m, nil
	case tea.KeyEsc:
		m.filter = ""
		m.filtering = false
		m.reconcile()
		return m, nil
	case tea.KeyBackspace:
		if m.filter != "" {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
			m.reconcile()
		}
		return m, nil
	}
	if msg.Text != "" {
		m.filter += msg.Text
		m.reconcile()
	}
	return m, nil
}

// cycleFocus moves the focus to the next panel, or to the previous one when
// shift is held.
func (m *Model) cycleFocus(back bool) {
	if back {
		m.focus = (m.focus + focusCount - 1) % focusCount
	} else {
		m.focus = (m.focus + 1) % focusCount
	}
}

// moveCursor moves the focused list panel's cursor by delta. Moving in Needs
// you selects that item's agent, so the right panel follows it.
func (m *Model) moveCursor(delta int) {
	switch m.focus {
	case focusAgents:
		m.moveAgents(delta)
	case focusNeedsYou:
		m.moveNeedsYou(delta)
	}
}

// moveAgents moves the Agents panel's cursor over its selectable rows — never
// onto a group header — and selects the row it lands on.
func (m *Model) moveAgents(delta int) {
	_, rows := m.agentLines()
	if len(rows) == 0 {
		return
	}
	m.agentCursor = clamp(m.agentCursor+delta, 0, len(rows)-1)
	m.selected = rows[m.agentCursor].id
}

// moveNeedsYou moves the Needs you panel's cursor over the queue and selects
// the agent of the item it lands on. An intake item names no agent, so the
// selection stays where it was.
func (m *Model) moveNeedsYou(delta int) {
	queue := m.state.Inbox.Queue
	if len(queue) == 0 {
		return
	}
	m.needsCursor = clamp(m.needsCursor+delta, 0, len(queue)-1)
	item := queue[m.needsCursor]
	if item.Kind != contract.InboxItemAgent || item.AgentID == "" {
		return
	}
	m.selected = item.AgentID
	// Keep the Agents panel on the same row, since the selection is by ID.
	if _, rows := m.agentLines(); len(rows) > 0 {
		if idx, ok := rowIndex(rows, item.AgentID); ok {
			m.agentCursor = idx
		}
	}
}
