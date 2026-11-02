package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
)

// handleKeypress routes the global hotkeys and the Enter-submit flow.
// Input is always focused; approval/question pending states are routed by
// Update before this runs. It returns handled=false when the key should
// fall through to the input textarea (e.g. "?" with text present, arrow
// keys with no completion popup, Tab while an approval/question is
// pending).
func (m *Model) handleKeypress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	// Browse mode owns its keys; the ones it declines (Ctrl chords) continue
	// down the normal path below.
	if m.browsing {
		if mm, cmd, handled := m.handleBrowseKey(msg); handled {
			return mm, cmd, true
		}
		if !m.browsing {
			// An unbound printable key left browse mode; it is typed next.
			return *m, nil, false
		}
	}

	// readlineShortcutAvailable reports whether a key that shadows standard
	// readline/textarea bindings should be handled globally right now. When
	// the input has text or the user is editing a command, those keys fall
	// through to the textarea so shell muscle memory (Ctrl+U clears the
	// prompt, End goes to line-end, etc.) keeps working.
	readlineShortcutAvailable := func() bool {
		return m.input.Value() == "" && !m.editingApproval()
	}
	// Any key other than a second Ctrl+R disarms a pending rollback, so the
	// armed state can never outlive the keystroke that set it.
	if m.rollbackArmed && msg.String() != "ctrl+r" {
		m.rollbackArmed = false
	}

	// Suggestion accept/dismiss keys are routed before the textarea sees
	// them, following the existing priority-routing pattern. Right accepts
	// only at end-of-input; Tab accepts only when the completion popup is
	// closed (popup priority wins); Esc dismisses whether busy or idle.
	if m.suggestion != "" && !m.suggestionDismissed {
		switch msg.String() {
		case "right":
			if m.cursorAtEndOfInput() {
				m.acceptSuggestion()
				return *m, nil, true
			}
		case "tab":
			if m.activeCompletionPopup() == nil && m.cursorAtEndOfInput() {
				m.acceptSuggestion()
				return *m, nil, true
			}
		case "esc":
			m.suggestionDismissed = true
			m.refreshViewport()
			return *m, nil, true
		}
	}

	switch msg.String() {
	case "?":
		// ? on an empty textarea prints the help cheatsheet to the
		// transcript, same as typing /help. With input already present
		// (or mid approval/question/skill-gate/command-edit), ? falls
		// through to the trailing m.input.Update(msg) below and is typed
		// literally.
		p := m.sess.Pending()
		if m.input.Value() == "" && !m.editingApproval() && p.Question == nil && len(p.Approvals) == 0 && p.SkillGate == nil && p.Sudo == nil {
			mm, cmd := m.dispatchCommand("/help")
			return mm, cmd, true
		}
		return *m, nil, false
	case "esc":
		// Esc only closes things; it never cancels a turn (Ctrl+C, twice,
		// does). Dismiss the active completion popup first.
		if m.activeCompletionPopup() != nil {
			m.activeCompletionPopup().dismiss()
			m.completionSuppressed = true
			return *m, nil, true
		}
		// A text selection is the last thing the user made: Esc clears it
		// and does nothing else.
		if m.sel != nil {
			m.clearSelection()
			return *m, nil, true
		}
		// While drilled into a subagent, Esc pops back to the parent
		// transcript. Ctrl+X while drilled into a running subagent stops
		// that subagent; Ctrl+C (twice) stops the whole turn.
		if m.popDrill() {
			m.refreshViewport()
			return *m, nil, true
		}
		// Dismiss the notice banner, busy or idle: it is the most recent
		// thing asking for attention.
		if _, ok := m.state.Notice(); ok {
			m.state.DismissNotice()
			m.refreshViewport()
			return *m, nil, true
		}
		m.resetHistoryNav()
		// Nothing else wanted the key: Esc starts browsing the transcript.
		// It never cancels a turn, busy or idle.
		m.enterBrowse()
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
		m.toggleExpandAll()
		word := "off"
		if m.expandAll {
			word = "on"
		}
		cmd := m.setFlash("Expand all: " + word)
		m.invalidateTranscript()
		m.refreshViewport()
		return *m, cmd, true
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
		// The handover drops any selection and pending press outright:
		// the side that just lost the mouse never delivers the release
		// that would end them, and the refresh below revalidates a
		// selection only when the notice changes the content signature.
		m.clearSelection()
		verb := "released to the terminal — click-drag to select, keys to scroll (PgUp/PgDn/Ctrl+U/Ctrl+D/End)"
		if !m.mouseReleased {
			verb = "captured — wheel scrolls, drag selects and copies"
		}
		m.state.AddMessage(session.RoleSystem, "Mouse "+verb+". Ctrl+S to toggle.", session.ContentTypePlain)
		m.refreshViewport()
		return *m, nil, true
	case "ctrl+b":
		if !readlineShortcutAvailable() {
			return *m, nil, false
		}
		m.openSessionSheet()
		return *m, nil, true
	case "ctrl+r":
		if m.sess.Status().HasBackup {
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
			// The adapter restores the backup and logs the audit row, failed or
			// not. The error is ignored here because the row carries it, exactly
			// as the transcript did before.
			_ = m.sess.Rollback(m.ctx)
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
		p := m.sess.Pending()
		if len(p.Approvals) > 0 || p.Question != nil || p.Sudo != nil {
			return *m, nil, false
		}
		m.cycleMode(true)
		return *m, nil, true
	case "shift+tab":
		if m.activeCompletionPopup() != nil {
			return *m, nil, true
		}
		p := m.sess.Pending()
		if len(p.Approvals) > 0 || p.Question != nil || p.Sudo != nil {
			return *m, nil, false
		}
		m.cycleMode(false)
		return *m, nil, true
	case "alt+m":
		m.cycleModel(true)
		return *m, nil, true
	case "alt+e":
		m.cycleEffort()
		return *m, nil, true
	case "alt+shift+m":
		m.cycleModel(false)
		return *m, nil, true
	case "ctrl+x":
		switch m.ctrlXAction() {
		case ctrlXStopAgent:
			// The user is drilled into a subagent the snapshot says is
			// running, so Ctrl+X stops that specific child instead of
			// touching the steering queue. Esc still pops the drill;
			// Ctrl+C (twice) stops the whole turn.
			//
			// The view stack holds a snapshot: the child can finish or lose
			// its cancel handle before this request arrives. A refused stop is
			// consumed and leaves the queue untouched — it must never fall
			// through to clearing a different target, nor claim a stop that
			// did not happen.
			top := m.viewStack[len(m.viewStack)-1]
			if m.state.CancelSubagent(top.ID) {
				m.refreshViewport()
				return *m, nil, true
			}
			return *m, m.setFlash("Agent could not be stopped; queue unchanged"), true
		case ctrlXClearQueue:
			// F16 R3: clear the steering queue while the agent is
			// working. Out-of-band so /clear semantics don't collide.
			// An accepted, undelivered Send now request keeps its covered entry:
			// Ctrl+X clears ordinary queued text, it cannot revoke a request the
			// runtime already accepted.
			m.state.ClearSteering()
			m.refreshViewport()
			return *m, nil, true
		}
		return *m, nil, false
	case "enter":
		// F18: if a popup is visible, accept the selection. Commands
		// and setting values submit immediately (single Enter = accept
		// + run); file paths and setting keys accept only so the user
		// can keep composing (Enter = accept, then type more). Esc
		// dismisses without accepting.
		if p := m.activeCompletionPopup(); p != nil {
			shouldSubmit := m.completionShouldSubmit(p)
			if m.acceptCompletion() {
				if !shouldSubmit {
					return *m, nil, true
				}
				// Fall through: the popup accepted a command or
				// setting value, and the input now holds a
				// submittable command.
			}
		}
		value := strings.TrimSpace(m.input.Value())
		if len(m.pastes) > 0 {
			value = m.expandPastes(value)
		}
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
		m.resetInput()
		m.completionSuppressed = false
		m.lastInputForPopups = ""
		m.cmdArgMode = false
		m.cmdArgPrefix = ""
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
			m.refreshViewport()
			return *m, nil, true
		}
		if m.runner == nil {
			m.state.AddMessage(session.RoleUser, value, session.ContentTypePlain)
			m.refreshViewport()
			return *m, nil, true
		}
		mm, cmd, _ := m.startAgentRun(m.runner, value)
		return mm, cmd, true
	}
	return *m, nil, false
}

// cursorAtEndOfInput reports whether the textarea cursor sits at the end of
// the input value (the only position where Right/Tab accept a suggestion).
// It compares the cursor's character offset against the value length on the
// cursor's line.
func (m *Model) cursorAtEndOfInput() bool {
	value := m.input.Value()
	if value == "" {
		return true
	}
	li := m.input.LineInfo()
	// For a single-line input the cursor is at EOL when its char offset
	// equals the value length. Multi-line inputs only accept at the end of
	// the last line.
	if m.input.Line() != m.input.LineCount()-1 {
		return false
	}
	lines := strings.Split(value, "\n")
	return li.CharOffset >= len([]rune(lines[m.input.Line()]))
}

// acceptSuggestion fills the input with the active suggestion, moves the
// cursor to the end, and clears the suggestion so it behaves like normal
// typed text (editable, Enter submits).
func (m *Model) acceptSuggestion() {
	m.input.SetValue(m.suggestion)
	m.input.CursorEnd()
	m.suggestion = ""
	m.suggestionDismissed = false
	m.refreshViewport()
}

// clearFinishedRun clears a finished run's collapsed summary and its event
// log on the user's next turn, so a second run's events don't append to the
// first run's.
func (m *Model) clearFinishedRun() {
	if m.sess.Status().SDD.Finished {
		m.state.ClearSDDProgress()
		m.state.ClearRunEvents()
	}
}
