package tui

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/glyph"
)

// FocusTarget names the surface that owns keyboard input. The product
// contract is that exactly one target receives typing at a time, that the
// marker for it is legible without relying on color, and that cycling never
// materializes a surface the user cannot see.
//
// The root stores only the *non-modal* target in m.focus. Panel focus is
// derived (see effectiveFocus): a dock panel, an approval, a question, or a
// skill gate owns every key while it is up, and the non-modal target is
// restored by construction rather than by bookkeeping — nothing overwrites
// m.focus while a panel is open, so every open/close path restores it
// without having to remember to save and restore.
type FocusTarget int

const (
	// FocusComposer is the chat textarea. It is the default: a session opens
	// with the caret in the composer.
	FocusComposer FocusTarget = iota
	// FocusConversation is the transcript viewport. Keys scroll it; typed
	// text is swallowed rather than leaking into the composer behind it.
	FocusConversation
	// FocusInspector is the side rail / inspector column.
	FocusInspector
	// FocusPanel is a modal surface that owns every key while it is up. It
	// is never elected directly: it is derived while a panel is open, so a
	// caller cannot focus a panel that is not there.
	FocusPanel
)

// label is the word the focus marker renders. It is paired with a glyph
// everywhere it appears, so the marker is never color alone.
func (f FocusTarget) label() string {
	switch f {
	case FocusConversation:
		return "conversation"
	case FocusInspector:
		return "inspector"
	case FocusPanel:
		return "panel"
	default:
		return "composer"
	}
}

// marker is the visible focus cue: a glyph plus a label.
func (f FocusTarget) marker() string {
	return glyph.Running + " " + f.label()
}

// availableFocusTargets lists the non-modal targets F6 may cycle between, in
// cycle order. The inspector appears only when the rail is actually on
// screen: cycling must not open a hidden surface, so a narrow terminal (or a
// hidden rail) simply has fewer stops.
func (m Model) availableFocusTargets() []FocusTarget {
	targets := []FocusTarget{FocusComposer, FocusConversation}
	if m.railEnabled() {
		targets = append(targets, FocusInspector)
	}
	return targets
}

// panelOwnsKeys reports whether a modal surface currently owns every key.
// These are the same surfaces Update routes before the composer sees a
// keypress at all; naming them here keeps the focused-target answer and the
// routing answer in one place.
func (m Model) panelOwnsKeys() bool {
	if m.dock.IsOpen() {
		return true
	}
	if m.state == nil {
		return false
	}
	return m.hasPendingApproval() ||
		m.state.PendingQuestion() != nil ||
		m.state.PendingChildQuestion() != nil ||
		m.state.PendingSkillGate() != nil
}

// effectiveFocus resolves the single target that owns keys right now.
//
// An inspector that is no longer on screen yields to the composer: shrinking
// the terminal must not leave keys routed to a surface that is not rendered,
// and m.focus keeps the user's intent so widening again restores it.
func (m Model) effectiveFocus() FocusTarget {
	if m.panelOwnsKeys() {
		return FocusPanel
	}
	if m.focus == FocusInspector && !m.railEnabled() {
		return FocusComposer
	}
	return m.focus
}

// composerReceivesTyping reports whether the trailing textarea fall-through
// in Update may consume this key. It is the single answer to "who receives
// typing?", consulted by both keys and pastes.
func (m Model) composerReceivesTyping() bool {
	// Doctor fix sub-mode owns the textarea for a typed API key even if the
	// user left focus on another surface: that prompt is modal in practice,
	// and silently dropping the key would strand it.
	if m.doctorFixProvider != "" {
		return true
	}
	return m.effectiveFocus() == FocusComposer
}

// setFocus installs a non-modal focus target and keeps the textarea's own
// focus flag in step with it: a blinking caret behind a conversation that
// owns the keys would be a second, false focus marker.
func (m *Model) setFocus(target FocusTarget) tea.Cmd {
	if target == FocusPanel {
		target = FocusComposer
	}
	m.focus = target
	var cmd tea.Cmd
	if target == FocusComposer {
		cmd = m.input.Focus()
	} else {
		m.input.Blur()
	}
	m.refreshViewport()
	return cmd
}

// cycleFocus advances (forward) or reverses the focus target among the
// currently available ones, wrapping around. A target that vanished (the
// rail hiding on a narrow resize) falls back to the composer rather than
// stepping to a neighbour the user did not ask for.
func (m *Model) cycleFocus(forward bool) tea.Cmd {
	targets := m.availableFocusTargets()
	if len(targets) == 0 {
		return nil
	}
	cur := m.effectiveFocus()
	idx := -1
	for i, t := range targets {
		if t == cur {
			idx = i
			break
		}
	}
	if idx < 0 {
		return m.setFocus(FocusComposer)
	}
	step := 1
	if !forward {
		step = -1
	}
	next := targets[(idx+step+len(targets))%len(targets)]
	return m.setFocus(next)
}

// handleFocusedSurfaceKey routes a key to the focused surface when the
// composer does not own typing.
//
// It always reports handled. Letting the key fall through would hand it to
// the textarea, giving the composer a second, invisible key recipient —
// exactly what the focus contract forbids.
func (m *Model) handleFocusedSurfaceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.effectiveFocus() != FocusConversation {
		// The inspector column owns no scroll state yet (it arrives with the
		// inspector host). Until then its target is a marker plus Esc back to
		// the composer, not a second keymap.
		return *m, nil, true
	}
	switch msg.String() {
	case "up":
		m.viewport.ScrollUp(1)
		m.viewportFollow = false
	case "down":
		m.viewport.ScrollDown(1)
		if m.viewport.AtBottom() {
			m.viewportFollow = true
		}
	case "pgup":
		m.viewport.PageUp()
		m.viewportFollow = false
	case "pgdown":
		m.viewport.PageDown()
		if m.viewport.AtBottom() {
			m.viewportFollow = true
		}
	case "ctrl+u":
		m.viewport.HalfPageUp()
		m.viewportFollow = false
	case "ctrl+d":
		m.viewport.HalfPageDown()
		if m.viewport.AtBottom() {
			m.viewportFollow = true
		}
	case "end":
		m.viewport.GotoBottom()
		m.viewportFollow = true
	case "home":
		m.viewport.GotoTop()
		m.viewportFollow = false
	}
	m.refreshViewport()
	return *m, nil, true
}
