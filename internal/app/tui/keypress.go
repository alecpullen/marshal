package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/sdd"
	"marshal/internal/tools/registry"
)

// handleKeypress routes the global hotkeys and the Enter-submit flow.
// Input is always focused; approval/question pending states are routed by
// Update before this runs. It returns handled=false when the key should
// fall through to the input textarea (e.g. "?" with text present, arrow
// keys with no completion popup, Tab while an approval/question is
// pending).
func (m *Model) handleKeypress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	// SDD human gate: only y/n/esc are accepted while a gate is pending.
	if m.pendingSDDGate {
		switch msg.String() {
		case "y":
			m.pendingSDDGate = false
			// ResolveGate reads the gate, advances the controller state
			// machine, and clears it. Do NOT clear the gate here first.
			if m.sddRunner != nil {
				m.sddRunner.ResolveGate()
			}
			// Re-dispatch Run with the same plan path. The controller
			// resumes from its saved State field. Read the plan path
			// from the controller adapter's stored PlanPath.
			goal := ""
			if a, ok := m.sddRunner.(*sdd.ControllerAdapter); ok {
				goal = a.Controller().PlanPath
			}
			_, cmd := m.startAgentRun(m.sddRunner, goal)
			return *m, cmd, true
		case "n", "esc":
			m.pendingSDDGate = false
			m.state.ClearSDDGate()
			m.state.AddMessage(session.RoleSystem, "SDD gate aborted by user.", session.ContentTypePlain)
			m.refreshViewport()
			return *m, nil, true
		}
		// Any other key is swallowed while the gate is pending.
		return *m, nil, true
	}

	switch msg.String() {
	case "?":
		// ? on an empty textarea prints the help cheatsheet to the
		// transcript, same as typing /help. With input already present
		// (or mid approval/question/command-edit), ? falls through to
		// the trailing m.input.Update(msg) below and is typed literally.
		if m.input.Value() == "" && !m.editingCommand && m.state.PendingQuestion() == nil && m.state.PendingApproval() == nil {
			mm, cmd := m.dispatchCommand("/help")
			return mm, cmd, true
		}
		return *m, nil, false
	case "esc":
		// F18: dismiss the active completion popup first. Only if
		// nothing is up do we fall through to cancelling the in-flight
		// turn.
		if m.activeCompletionPopup() != nil {
			m.activeCompletionPopup().dismiss()
			return *m, nil, true
		}
		m.cancelTurn()
		return *m, nil, true
	case "ctrl+o":
		m.openSettingsBrowser("")
		return *m, nil, true
	case "ctrl+p":
		cmd := m.openModels()
		m.refreshViewport()
		return *m, cmd, true
	case "ctrl+k":
		if m.memoryDB == nil {
			return *m, nil, true
		}
		m.dock.Open(memory.NewPanel(m.memoryDB, m.memoryProject))
		m.refreshViewport()
		return *m, nil, true
	case "ctrl+g":
		m.thinkingExpanded = !m.thinkingExpanded
		m.lastTranscriptHash = 0
		m.refreshViewport()
		return *m, nil, true
	case "ctrl+t":
		// Cycle the pinned todo panel: expanded → collapsed → hidden.
		// State persists for the session.
		m.todoPanelMode = (m.todoPanelMode + 1) % todoPanelModeCount
		m.updateViewportHeight()
		return *m, nil, true
	case "ctrl+r":
		if m.state.HasBackup() {
			_ = m.state.RollbackBackup()
			m.state.LogToolCall(registry.AuditEvent{
				Timestamp:     time.Now(),
				ToolName:      "rollback",
				ResultSummary: "Rollback applied successfully",
			})
			m.refreshViewport()
		}
		return *m, nil, true
	case "pgup", "pgdown":
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		if msg.String() == "pgup" {
			m.viewportFollow = false
		}
		if msg.String() == "pgdown" && m.viewport.AtBottom() {
			m.viewportFollow = true
		}
		return *m, vpCmd, true
	case "ctrl+u":
		m.viewport.HalfPageUp()
		m.viewportFollow = false
		return *m, nil, true
	case "ctrl+d":
		m.viewport.HalfPageDown()
		if m.viewport.AtBottom() {
			m.viewportFollow = true
		}
		return *m, nil, true
	case "end":
		m.viewport.GotoBottom()
		m.viewportFollow = true
		return *m, nil, true
	case "up":
		if m.activeCompletionPopup() != nil {
			m.activeCompletionPopup().moveUp()
			return *m, nil, true
		}
		return *m, nil, false
	case "down":
		if m.activeCompletionPopup() != nil {
			m.activeCompletionPopup().moveDown()
			return *m, nil, true
		}
		return *m, nil, false
	case "tab":
		if m.acceptCompletion() {
			return *m, nil, true
		}
		if m.state.PendingApproval() != nil || m.state.PendingQuestion() != nil {
			return *m, nil, false
		}
		m.cycleMode(true)
		return *m, nil, true
	case "shift+tab":
		if m.activeCompletionPopup() != nil {
			return *m, nil, true
		}
		if m.state.PendingApproval() != nil || m.state.PendingQuestion() != nil {
			return *m, nil, false
		}
		m.cycleMode(false)
		return *m, nil, true
	case "alt+m":
		m.cycleModel(true)
		return *m, nil, true
	case "alt+shift+m":
		m.cycleModel(false)
		return *m, nil, true
	case "ctrl+x":
		// F16 R3: clear the steering queue while the agent is
		// working. Out-of-band so /clear semantics don't collide.
		if m.busy {
			m.state.ClearSteering()
			m.queuedCount = 0
			m.refreshViewport()
			return *m, nil, true
		}
		return *m, nil, false
	case "enter":
		// F18: if a popup is visible, accept it (replaces the trigger
		// token) and keep editing — Enter on a popup is a selection,
		// not a submit. Esc is the way to dismiss without accepting.
		if m.acceptCompletion() {
			return *m, nil, true
		}
		value := strings.TrimSpace(m.input.Value())
		// F16 R2 follow-up turn: when the agent has finished and the
		// user presses Enter with no new input, pop the oldest queued
		// steering message and submit it as the next turn. This must
		// happen BEFORE the empty-input short-circuit so a blank
		// prompt still drains the queue.
		if value == "" && !m.busy {
			if followUp, ok := m.popOldestSteering(); ok {
				value = followUp
			} else {
				return *m, nil, true
			}
		}
		if value == "" {
			return *m, nil, true
		}
		m.input.Reset()
		// The all-done todo summary belongs to the finished turn; the next
		// user turn clears it (a fresh list from the agent brings it back —
		// see refreshViewport).
		if todosAllDone(m.state.Todos()) {
			m.todosDismissed = true
		}
		m.dismissCompletionPopups()
		m.updateViewportHeight()
		m.viewportFollow = true

		if strings.HasPrefix(value, "/") {
			mm, cmd := m.dispatchCommand(value)
			return mm, cmd, true
		}

		if m.busy {
			// F16: turn is running — enqueue as a steering message
			// instead of dropping the input.
			m.state.PushSteering(value)
			return *m, nil, true
		}
		if m.runner == nil {
			m.state.AddMessage(session.RoleUser, value, session.ContentTypePlain)
			m.refreshViewport()
			return *m, nil, true
		}
		mm, cmd := m.startAgentRun(m.runner, value)
		return mm, cmd, true
	}
	return *m, nil, false
}
