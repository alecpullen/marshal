package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/doctorpanel"
	"marshal/internal/app/tui/memory"
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
				home, err := m.userHome()
				if err != nil {
					m.state.AddMessage(session.RoleSystem,
						fmt.Sprintf("✗ Failed to locate home directory: %v", err), session.ContentTypePlain)
				} else if err := config.SaveUserConfigProviderAPIKey(
					config.UserConfigPath(home), m.doctorFixProvider,
					config.ProviderConfig{APIKey: value}); err != nil {
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
					checks := doctorpanel.ComputeIntelligence(m.state.Config, m.memoryDB, m.memoryProject)
					m.dock.Open(doctorpanel.New(m.state, diags, checks))
				}
			}
			m.resetInput()
			m.input.Placeholder = m.savedInputPlaceholder
			m.doctorFixProvider = ""
			m.savedInputPlaceholder = ""
			m.refreshViewport()
			return *m, nil, true
		case "esc":
			m.resetInput()
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

	// Suggestion accept/dismiss keys are routed before the textarea sees
	// them, following the existing priority-routing pattern. Right accepts
	// only at end-of-input; Tab accepts only when the completion popup is
	// closed (popup priority wins); Esc dismisses only when idle (cancel
	// takes precedence while busy). The suggestion is composer chrome, so it
	// only claims keys while the composer owns typing.
	if m.suggestion != "" && !m.suggestionDismissed && m.composerReceivesTyping() {
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
			if !m.busy {
				m.suggestionDismissed = true
				m.refreshViewport()
				return *m, nil, true
			}
		}
	}

	// Focus dispatch runs before the composer keymap so exactly one surface
	// owns a key. Nothing here depends on which target is focused, so it runs
	// regardless — a control key that works while the conversation is focused
	// should not stop working because focus moved.
	switch msg.String() {
	case "ctrl+s":
		// The capture toggle is deliberately NOT gated on readlineShortcut-
		// Available(): the draft is the whole point. A user copying part of
		// the conversation while writing the next prompt was the exact case
		// the guard broke.
		mm, cmd := m.toggleMouseCapture()
		return mm, cmd, true
	case "f6", "shift+f6":
		return *m, m.cycleFocus(msg.String() == "f6"), true
	case "f2":
		// The palette takes over the dock and lists the available actions
		// itself, so it is reachable even from a state where a specific
		// action is unavailable.
		m.openActionPalette()
		return *m, nil, true
	case "y":
		// `y` copies. It is claimed only while the conversation owns the
		// keys: with the composer focused a bare `y` is a letter, and
		// swallowing it would put a hole in the keyboard.
		//
		// It must be dispatched HERE rather than in handleFocusedSurfaceKey,
		// which runs later in this function: that handler reports handled
		// for every key while the conversation is focused (deliberately — a
		// key that fell through would reach the textarea as a second,
		// invisible recipient), so it would swallow `y` before any copy
		// case could see it.
		if m.effectiveFocus() != FocusConversation {
			return *m, nil, false
		}
		// A SELECTION wins over the block the reader is on. Somebody who
		// dragged over a phrase means that phrase, and handing them the whole
		// block would make the careful gesture pointless — so the selection is
		// copied directly rather than through the block-target resolver,
		// which would look for a copy TARGET on the block and find the whole
		// answer.
		if m.hasSelection() {
			return *m, m.copySelectionText(), true
		}
		mm, cmd := m.runAction(ActionCopyAnswer)
		return mm, cmd, true
	case "esc":
		// The inspector backs out of its own depth first: a body-expanded
		// panel returns to its shared placement, and an open detail pops one
		// level. Both are "undo the last thing I opened", which is what Esc
		// means, and both must be tried BEFORE the focus move — otherwise the
		// first press would only move focus and the user would need two.
		//
		// The inspector is consulted when it owns the keys, or when it is
		// body-expanded (where it visibly owns the body regardless of where
		// m.focus points). A side-placed inspector while the composer has
		// focus leaves Esc alone: the composer's own meanings are nearer to
		// the user in that state.
		if m.inspector != nil && m.inspector.isRendering() {
			owns := m.effectiveFocus() == FocusInspector || m.inspector.replacesBodyOnly()
			if owns && m.inspector.esc() {
				m.refreshInspector()
				return *m, nil, true
			}
		}
		// A SELECTION is the innermost thing to back out of on the transcript:
		// the reader drew it last, and pressing Esc means "not that". It is
		// handled before the focus move so one press clears the highlight
		// rather than moving focus away from it — which would leave the
		// selection on screen, unowned.
		//
		// It is deliberately NOT handled before the inspector's Esc: the
		// inspector's own depth is nearer to the user when the inspector owns
		// the keys.
		if m.effectiveFocus() == FocusConversation && m.selectionActive() {
			m.clearSelection()
			m.lastTranscriptHash = 0
			m.refreshViewport()
			return *m, nil, true
		}
		// Esc leaves a non-composer focus target before any composer-side
		// meaning (popup dismissal, drill pop, turn cancel) can claim it: one
		// press performs one operation, and the outermost thing to back out
		// of is the focus move itself.
		if m.effectiveFocus() != FocusComposer {
			return *m, m.setFocus(FocusComposer), true
		}
	}

	// While the conversation owns the keys, its scroll keys are handled here
	// and every other key is swallowed rather than leaking into the composer
	// behind it. Reporting "unhandled" would hand the key to the textarea and
	// give the composer a second, invisible key recipient.
	if !m.composerReceivesTyping() {
		if m.inspector != nil && m.effectiveFocus() == FocusInspector {
			// The inspector is a real owner of the keys, not a marker, and it
			// is the inspector's own handler that decides what a key means on
			// the tab on display. Always reporting handled is the same contract
			// handleFocusedSurfaceKey keeps — a key that fell through would
			// reach the textarea behind it.
			return m.handleInspectorKey(msg)
		}
		return m.handleFocusedSurfaceKey(msg)
	}

	switch msg.String() {
	case "?":
		// ? on an empty textarea prints the help cheatsheet to the
		// transcript, same as typing /help. With input already present
		// (or mid approval/question/skill-gate/command-edit), ? falls
		// through to the trailing m.input.Update(msg) below and is typed
		// literally.
		if m.input.Value() == "" && !m.editingCommand && m.state.PendingQuestion() == nil && m.state.PendingChildQuestion() == nil && !m.hasPendingApproval() && m.state.PendingSkillGate() == nil {
			mm, cmd := m.dispatchCommand("/help")
			return mm, cmd, true
		}
		return *m, nil, false
	case "esc":
		// F18: dismiss the active completion popup first. Only if
		// nothing is up do we fall through to cancelling the in-flight
		// turn. (A non-composer focus target was already handled above; the
		// composer is the only surface left here.)
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
		// An idle esc dismisses the toast (a UI acknowledgement the user has
		// clearly seen) and then the notice banner: there is no turn to cancel
		// and those are the most recent things asking for attention. Busy
		// turns fall through to cancelTurn as before.
		if !m.busy {
			if m.toastText() != "" {
				m.clearToast()
				m.lastTranscriptHash = 0
				m.refreshViewport()
				return *m, nil, true
			}
			if _, ok := m.state.Notice(); ok {
				m.state.DismissNotice()
				m.refreshViewport()
				return *m, nil, true
			}
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
		// Ctrl+F keeps its explicit inspection meaning: the implicit
		// agent-lane takeover on Up/Down is gone, so this is the keyboard
		// route into a child transcript.
		mm, cmd := m.runAction(ActionInspectAgent)
		return mm, cmd, true
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
		m.clearActiveToolExpansions()
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
		m.refreshViewport()
		return *m, nil, true
	case "ctrl+b":
		// The toggle is deliberately NOT gated on readlineShortcutAvailable(),
		// for the same reason the capture toggle above is not: the draft is the
		// whole point. Inspecting what changed while writing the next prompt is
		// the case the guard broke, and it made the key mean two different
		// things depending on whether the user had typed — the kind of
		// conditional binding that teaches people not to trust a key.
		//
		// Ctrl+B toggles the INSPECTOR, not the bare rail. The inspector's
		// Overview is the rail's content made scrollable and navigable, so this
		// key yields a strictly more capable surface than the read-only strip.
		// The rail's own visibility remains a setting ([tui.side_panel].enabled);
		// the inspector is a session toggle that also works below the rail's
		// width threshold, by falling back to the dock.
		if m.inspector != nil {
			m.inspector.toggle(m.inspectorSideAvailable())
			m.refreshInspector()
		}
		m.resize(m.rawWidth, m.rawHeight)
		return *m, nil, true
	case "ctrl+r":
		mm, cmd := m.runAction(ActionRollback)
		return mm, cmd, true
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
		// Up/Down belong to the composer: prompt history and textarea
		// navigation. They used to move a cursor in the agents lane whenever
		// the input was empty, an invisible mode that made a blank Up key
		// mean "select an agent" instead of "recall my last prompt" — the
		// exact muscle memory the key exists for. Explicit Ctrl+F is the
		// keyboard route into a running child's transcript, and the lane
		// itself is still click-drillable.
		//
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
		// Down mirrors Up (see the "up" case above).
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
		if m.hasPendingApproval() || m.state.PendingQuestion() != nil || m.state.PendingChildQuestion() != nil {
			return *m, nil, false
		}
		m.cycleMode(true)
		return *m, nil, true
	case "shift+tab":
		if m.activeCompletionPopup() != nil {
			return *m, nil, true
		}
		if m.hasPendingApproval() || m.state.PendingQuestion() != nil || m.state.PendingChildQuestion() != nil {
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
		// Ctrl+X resolves to exactly one action, from the same context
		// snapshot the footer renders its hint from — stop the inspected
		// running child when that is available, otherwise clear a nonempty
		// queue while the turn is busy. The old code decided this inline
		// while the footer decided it separately, which is how one key ended
		// up advertised with two verbs.
		//
		// With no resolution the key is simply unbound: the footer omits the
		// hint in exactly that state, so there is nothing to explain and a
		// toast would announce a non-event. The key is still consumed, so it
		// never reaches the textarea as a second recipient.
		id, ok := m.actionSnapshot().ctrlXID()
		if !ok {
			return *m, nil, true
		}
		mm, cmd := m.runAction(id)
		return mm, cmd, true
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
	if m.state.SDDProgress().Finished {
		m.state.ClearSDDProgress()
		m.state.ClearRunEvents()
	}
}
