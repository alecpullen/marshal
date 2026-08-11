package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/doctorpanel"
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
	// Doctor fix sub-mode: the input prompt is asking for an API key. Enter
	// saves it to the user config and re-runs /doctor; esc abandons it.
	if m.doctorFixProvider != "" {
		switch msg.String() {
		case "enter":
			value := strings.TrimSpace(m.input.Value())
			if value != "" {
				home, err := os.UserHomeDir()
				if err != nil {
					m.state.AddMessage(session.RoleSystem,
						fmt.Sprintf("✗ Failed to locate home directory: %v", err), session.ContentTypePlain)
				} else if err := config.SaveUserConfigProviderAPIKey(
					config.UserConfigPath(home), m.doctorFixProvider, value); err != nil {
					m.state.AddMessage(session.RoleSystem,
						fmt.Sprintf("✗ Failed to save API key: %v", err), session.ContentTypePlain)
				} else {
					// Reload config/runtime and re-run /doctor so the fixed
					// diagnostic disappears.
					newCfg := m.state.Config
					if newCfg.Providers != nil {
						copied := make(map[string]config.ProviderConfig, len(newCfg.Providers))
						for k, v := range newCfg.Providers {
							copied[k] = v
						}
						newCfg.Providers = copied
					}
					if pc, ok := newCfg.Providers[m.doctorFixProvider]; ok {
						pc.APIKey = value
						pc.APIKeyEnv = ""
						newCfg.Providers[m.doctorFixProvider] = pc
					}
					if saveErr, reloadErr := m.persistAndReload(newCfg); saveErr != nil {
						m.state.AddMessage(session.RoleSystem,
							fmt.Sprintf("✗ Saved key, but failed to persist project config: %v", saveErr), session.ContentTypePlain)
					} else if reloadErr != nil {
						m.state.AddMessage(session.RoleSystem,
							fmt.Sprintf("✗ Saved key, but failed to reload runtime: %v", reloadErr), session.ContentTypePlain)
					} else {
						m.state.AddMessage(session.RoleSystem,
							fmt.Sprintf("✓ Saved API key for %s", m.doctorFixProvider), session.ContentTypePlain)
					}
					// Re-run /doctor to refresh the panel.
					diags := config.Diagnose(m.state.Config, m.state.Layers())
					m.dock.Open(doctorpanel.New(m.state, diags))
				}
			}
			m.input.Reset()
			m.input.Placeholder = m.savedInputPlaceholder
			m.doctorFixProvider = ""
			m.savedInputPlaceholder = ""
			m.refreshViewport()
			return *m, nil, true
		case "esc":
			m.input.Reset()
			m.input.Placeholder = m.savedInputPlaceholder
			m.doctorFixProvider = ""
			m.savedInputPlaceholder = ""
			m.refreshViewport()
			return *m, nil, true
		}
		// Let typing/pasting fall through to the textarea.
	}

	// readlineShortcutAvailable reports whether a key that shadows standard
	// readline/textarea bindings should be handled globally right now. When
	// the input has text or the user is editing a command, those keys fall
	// through to the textarea so shell muscle memory (Ctrl+U clears the
	// prompt, End goes to line-end, etc.) keeps working.
	readlineShortcutAvailable := func() bool {
		return m.input.Value() == "" && !m.editingCommand
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
			m.completionSuppressed = true
			return *m, nil, true
		}
		// While drilled into a subagent, Esc pops back to the parent
		// transcript rather than cancelling the in-flight turn. Ctrl+X
		// while drilled into a running subagent stops that subagent;
		// Ctrl+C cancels the whole turn.
		if m.popDrill() {
			m.refreshViewport()
			return *m, nil, true
		}
		m.resetHistoryNav()
		m.cancelTurn()
		return *m, nil, true
	case "ctrl+o":
		m.openSettingsBrowser("")
		return *m, nil, true
	case "ctrl+f":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		// Drill into the most recently registered running subagent so
		// inspecting live work does not require a mouse. Esc pops back.
		if m.drillIntoLatestRunningSubagent() {
			m.refreshViewport()
			return *m, nil, true
		}
		return *m, nil, false
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
		m.cycleTodoPanelMode()
		return *m, nil, true
	case "ctrl+s":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		// Release the mouse to the terminal (or take it back) for the
		// session. Capture and native click-drag selection cannot coexist —
		// the terminal delivers events to one or the other — so copying a
		// block of output previously meant editing [tui].mouse_capture and
		// restarting, or knowing the terminal's override modifier.
		m.mouseReleased = !m.mouseReleased
		verb := "released to the terminal — click-drag to select, keys to scroll (PgUp/PgDn/Ctrl+U/Ctrl+D/End)"
		if !m.mouseReleased {
			verb = "captured — wheel scrolls the transcript"
		}
		m.state.AddMessage(session.RoleSystem, "Mouse "+verb+". Ctrl+S to toggle.", session.ContentTypePlain)
		m.refreshViewport()
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
		// Completion popups keep precedence over drill exit and prompt
		// history.
		if p := m.activeCompletionPopup(); p != nil {
			p.moveUp()
			return *m, nil, true
		}
		// While drilled into a subagent, up arrow pops back to the
		// parent transcript — matching ESC and the breadcrumb hint.
		if m.popDrill() {
			m.refreshViewport()
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
		// If the user is drilled into a running subagent, Ctrl+X stops
		// that specific subagent instead of touching the steering queue.
		// Esc still pops the drill; Ctrl+C cancels the whole turn.
		if m.busy && len(m.viewStack) > 0 {
			if top := m.viewStack[len(m.viewStack)-1]; top.Status == session.SubagentRunning {
				if m.state.CancelSubagent(top.ID) {
					m.refreshViewport()
					return *m, nil, true
				}
			}
		}
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
		m.completionSuppressed = false
		m.lastInputForPopups = ""
		// The all-done todo summary belongs to the finished turn; the next
		// user turn clears it (a fresh list from the agent brings it back —
		// see refreshViewport).
		if todosAllDone(m.state.Todos()) {
			m.todosDismissed = true
		}
		// A finished run's collapsed summary belongs to that run; the
		// next user turn clears it, same as the all-done todo summary.
		m.clearFinishedRun()
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

// clearFinishedRun clears a finished run's collapsed summary and its event
// log on the user's next turn, so a second run's events don't append to the
// first run's.
func (m *Model) clearFinishedRun() {
	if m.state.SDDProgress().Finished {
		m.state.ClearSDDProgress()
		m.state.ClearRunEvents()
	}
}
