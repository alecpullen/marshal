package fleet

import (
	"context"

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

	// An open modal takes every key while it is open (decision 5).
	if m.modal != nil {
		return m.modal.update(m, msg)
	}
	// An open approval answers y and n, and esc closes it (decision 4).
	// Everything else falls through to the screen. The prompt is drawn only
	// for the selected agent, so it answers only while its agent is selected.
	if m.answer != "" && m.answer == m.selected {
		if next, cmd, handled := m.answerKey(msg); handled {
			return next, cmd
		}
	}

	if msg.Code == tea.KeyTab {
		m.cycleFocus(msg.Mod.Contains(tea.ModShift))
		return m, nil
	}

	// The cursor keys move whichever of the two list panels has focus, or the
	// Diff tab's file while the right panel has focus.
	switch msg.Code {
	case tea.KeyDown:
		m.moveCursor(1)
		cmd := m.syncDiff()
		return m, cmd
	case tea.KeyUp:
		m.moveCursor(-1)
		cmd := m.syncDiff()
		return m, cmd
	case tea.KeyEnter:
		// ⏎ settles an intake item in Needs you (decision 8); on any other
		// row it opens that row. The filter has already taken its own Enter
		// above, so this one never types.
		if m.focus == focusNeedsYou {
			if _, ok := m.intakeItem(); ok {
				return m.openIntake()
			}
		}
		return m.openRow()
	case tea.KeyEsc:
		m.zoom = false
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, m.quit()
	case "a":
		return m.openSelected()
	case "n":
		return m.openSpawn()
	case "x":
		return m.cancelSelected()
	case "f":
		return m.openFinish()
	case "v":
		return m.verifySelected()
	case "j":
		m.moveCursor(1)
	case "k":
		m.moveCursor(-1)
	case "J":
		// The diff scrolls a row at a time while its tab is shown.
		if m.focus == focusRight && m.tab == diffTab {
			m.scrollDiff(1)
		}
	case "K":
		if m.focus == focusRight && m.tab == diffTab {
			m.scrollDiff(-1)
		}
	case "1":
		m.tab = 1
	case "2":
		m.tab = diffTab
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
	// The Diff tab follows the keys: a move to another agent reads its files,
	// and a move between files reads the one under the cursor. Every other key
	// leaves nothing to read.
	cmd := m.syncDiff()
	return m, cmd
}

// openRow ends the program with the selected row's target, so the loop can
// attach to it. A row with nothing to open — an intake item, which ⏎ settles
// instead, or no selection — leaves the screen running.
func (m Model) openRow() (tea.Model, tea.Cmd) {
	_, rows := m.agentLines()
	target := m.openTarget(rows)
	if target == (Target{}) {
		return m, nil
	}
	m.target = target
	return m, m.quit()
}

// cancelSelected stops the selected agent's turn (decision 7), with no
// confirmation, as the web UI's stop has none. Only a running agent has a turn
// to stop: anywhere else x does nothing.
func (m Model) cancelSelected() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok || row.session {
		return m, nil
	}
	a, ok := m.agentByID(row.id)
	if !ok || a.Status != contract.AgentRunning {
		return m, nil
	}
	if m.refusedWhileDown() {
		return m, nil
	}
	return m, m.cancelCmd(a)
}

// cancelCmd stops the agent's turn in a command, bounded by actionTimeout.
func (m Model) cancelCmd(a contract.AgentStatus) tea.Cmd {
	f, id, name := m.fleet, a.ID, agentName(a)
	seq := m.nextActionSeq()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return actionDoneMsg{what: "cancel", name: name, err: f.Cancel(ctx, id), seq: seq}
	}
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

// moveCursor moves the focused panel's cursor by delta: a list panel's row, or
// the Diff tab's file while the right panel has focus. Moving in Needs you
// selects that item's agent, so the right panel follows it.
func (m *Model) moveCursor(delta int) {
	switch m.focus {
	case focusAgents:
		m.moveAgents(delta)
	case focusNeedsYou:
		m.moveNeedsYou(delta)
	case focusRight:
		// The right panel's only list is the Diff tab's files. Its read
		// follows the cursor, from the key handler's syncDiff.
		if m.tab != diffTab || len(m.diff.files) == 0 {
			return
		}
		m.diff.cursor = clamp(m.diff.cursor+delta, 0, len(m.diff.files)-1)
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
	m.closeStaleAnswer()
}

// closeStaleAnswer drops an inline approval once the selection has left its
// agent: the prompt is drawn only for the selected agent, so nothing may
// answer it after the cursor moves on.
func (m *Model) closeStaleAnswer() {
	if m.answer != "" && m.answer != m.selected {
		m.answer = ""
	}
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
	m.closeStaleAnswer()
}
