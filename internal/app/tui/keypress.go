package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/tools/registry"
)

// handleKeypress routes the global hotkeys and the Enter-submit flow.
// Input is always focused; approval/question pending states are routed by
// Update before this runs. It returns handled=false when the key should
// fall through to the input textarea (e.g. "?" with text present, arrow
// keys with no completion popup, Tab while an approval/question is
// pending).
func (m *Model) handleKeypress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	// readlineShortcutAvailable reports whether a key that shadows standard
	// readline/textarea bindings should be handled globally right now. When
	// the input has text or the user is editing a command, those keys fall
	// through to the textarea so shell muscle memory (Ctrl+U clears the
	// prompt, End goes to line-end, etc.) keeps working.
	readlineShortcutAvailable := func() bool {
		return m.input.Value() == "" && !m.editingCommand
	}
	// Pipeline human gate: esc abandons the run; every other key falls
	// through to the input so the user can type an answer. The Enter
	// handler routes the submitted text to AnswerGate.
	if m.pendingSDDGate && msg.String() == "esc" {
		m.pendingSDDGate = false
		m.state.ClearSDDGate()
		m.state.AddMessage(session.RoleSystem, "Plan run stopped. Re-run /sdd with the same plan to resume from the ledger.", session.ContentTypePlain)
		m.refreshViewport()
		return *m, nil, true
	}

	// Any key other than a second Ctrl+R disarms a pending rollback, so the
	// armed state can never outlive the keystroke that set it.
	if m.rollbackArmed && msg.String() != "ctrl+r" {
		m.rollbackArmed = false
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
		m.resetHistoryNav()
		m.cancelTurn()
		return *m, nil, true
	case "ctrl+o":
		m.openSettingsBrowser("")
		return *m, nil, true
	case "ctrl+p":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		cmd := m.openModels()
		m.refreshViewport()
		return *m, cmd, true
	case "ctrl+k":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		if m.memoryDB == nil {
			return *m, nil, true
		}
		m.dock.Open(memory.NewPanel(m.memoryDB, m.memoryProject))
		m.refreshViewport()
		return *m, nil, true
	case "ctrl+g":
		m.detailExpanded = !m.detailExpanded
		m.itemExpanded = map[itemKey]bool{}
		m.activeToolExpanded = false
		m.lastTranscriptHash = 0
		m.refreshViewport()
		return *m, nil, true
	case "ctrl+t":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		// Cycle the pinned todo panel: expanded → collapsed → hidden.
		// State persists for the session.
		m.todoPanelMode = (m.todoPanelMode + 1) % todoPanelModeCount
		m.updateViewportHeight()
		return *m, nil, true
	case "ctrl+b":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		// Toggle the widescreen side rail for the session. Not persisted;
		// [tui.side_panel].enabled is the durable setting.
		m.railHidden = !m.railHidden
		m.resize(m.rawWidth, m.rawHeight)
		return *m, nil, true
	case "ctrl+r":
		if m.state.HasBackup() {
			// Arm on the first press, revert on the second. Ctrl+R is
			// reverse-i-search in every readline shell, so it gets pressed
			// reflexively; without this it silently rewrote the working tree
			// on a single keystroke. Every other destructive surface here
			// (tool approval, skill/plugin removal) confirms first.
			if !m.rollbackArmed {
				m.rollbackArmed = true
				m.state.AddMessage(session.RoleSystem,
					"Press Ctrl+R again to revert the last patch, or any other key to cancel.",
					session.ContentTypePlain)
				m.refreshViewport()
				return *m, nil, true
			}
			m.rollbackArmed = false
			// The error is load-bearing: a partial rollback leaves a mixed
			// working tree, and reporting success would hide that from both
			// the user and the audit trail.
			ev := registry.AuditEvent{
				Timestamp:     time.Now(),
				ToolName:      "rollback",
				ResultSummary: "Rollback applied successfully",
			}
			if err := m.state.RollbackBackup(); err != nil {
				ev.Error = err.Error()
				ev.ResultSummary = "Rollback failed"
			}
			m.state.LogToolCall(ev)
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
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		m.viewport.HalfPageUp()
		m.viewportFollow = false
		return *m, nil, true
	case "ctrl+d":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		m.viewport.HalfPageDown()
		if m.viewport.AtBottom() {
			m.viewportFollow = true
		}
		return *m, nil, true
	case "end":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		m.viewport.GotoBottom()
		m.viewportFollow = true
		return *m, nil, true
	case "up":
		// Completion popups keep precedence over prompt history.
		if p := m.activeCompletionPopup(); p != nil {
			p.moveUp()
			return *m, nil, true
		}
		if m.recallOlder() {
			return *m, nil, true
		}
		return *m, nil, false
	case "down":
		if p := m.activeCompletionPopup(); p != nil {
			p.moveDown()
			return *m, nil, true
		}
		if m.recallNewer() {
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
		// Pipeline human gate: submit the typed answer.
		if m.pendingSDDGate {
			answer := strings.TrimSpace(m.input.Value())
			if answer == "" {
				return *m, nil, true
			}
			m.input.SetValue("")
			m.pendingSDDGate = false
			m.state.AddMessage(session.RoleUser, answer, session.ContentTypePlain)
			if m.pipelineRunner != nil {
				m.pipelineRunner.AnswerGate(answer)
			}
			goal := m.state.SDDProgress().PlanPath
			_, cmd := m.startAgentRun(m.pipelineRunner, goal)
			return *m, cmd, true
		}
		// F18: if a popup is visible, accept it (replaces the trigger
		// token) and keep editing — Enter on a popup is a selection,
		// not a submit. Esc is the way to dismiss without accepting.
		if m.acceptCompletion() {
			return *m, nil, true
		}
		value := strings.TrimSpace(m.input.Value())
		if value != "" {
			m.recordPrompt(value)
		}
		m.resetHistoryNav()
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
