package tui

import "time"

// promptHistoryLimit caps how many entries are loaded at startup and kept
// in memory.
const promptHistoryLimit = 200

// recordPrompt adds a submitted prompt to history (newest first), collapsing
// consecutive duplicates and skipping empty submissions, and persists it to
// the project database. The db write is best-effort: a failed write must not
// break submit. Steering-queue follow-ups are not recorded here — they were
// recorded when the user originally typed them.
func (m *Model) recordPrompt(value string) {
	if value == "" || !m.state.Config.History.Enabled {
		return
	}
	if len(m.history) == 0 || m.history[0] != value {
		m.history = append([]string{value}, m.history...)
		if len(m.history) > promptHistoryLimit {
			m.history = m.history[:promptHistoryLimit]
		}
	}
	if database := m.state.DB(); database != nil && m.memoryProject != 0 {
		_ = database.SavePrompt(m.memoryProject, value, time.Now())
	}
}

// resetHistoryNav exits history-browsing mode (on submit or Esc).
func (m *Model) resetHistoryNav() {
	m.histIdx = -1
	m.draft = ""
}

// recallOlder handles up-arrow history navigation. It only consumes the key
// when the cursor is on the first input line; anywhere else the textarea
// keeps its ordinary cursor movement. Returns true when the key was consumed.
func (m *Model) recallOlder() bool {
	if !m.state.Config.History.Enabled || len(m.history) == 0 || m.input.Line() != 0 {
		return false
	}
	if m.histIdx == -1 {
		m.draft = m.input.Value()
		m.histIdx = 0
	} else if m.histIdx < len(m.history)-1 {
		m.histIdx++
	}
	// Edits to a recalled entry are transient: navigating re-reads the slice.
	m.input.SetValue(m.history[m.histIdx])
	m.input.CursorEnd()
	m.resetCompletionStateAfterRecall()
	return true
}

// resetCompletionStateAfterRecall clears popup state after a history
// recall: any visible popup is dismissed (without leaving Esc-suppression
// behind, so the next real edit re-evaluates triggers normally), the
// idempotency cache is dropped — a stale cache would otherwise swallow the
// first keystroke after recall — and any armed argument-completion prefix
// is disarmed.
func (m *Model) resetCompletionStateAfterRecall() {
	m.dismissCompletionPopups()
	m.completionSuppressed = false
	m.lastInputForPopups = ""
	m.cmdArgMode = false
	m.cmdArgPrefix = ""
}

// recallNewer handles down-arrow history navigation while browsing. Stepping
// past the newest entry restores the stashed draft. Returns true when the
// key was consumed.
func (m *Model) recallNewer() bool {
	if m.histIdx == -1 || len(m.history) == 0 || m.input.Line() != m.input.LineCount()-1 {
		return false
	}
	if m.histIdx > 0 {
		m.histIdx--
		m.input.SetValue(m.history[m.histIdx])
	} else {
		m.histIdx = -1
		m.input.SetValue(m.draft)
		m.draft = ""
	}
	m.input.CursorEnd()
	m.resetCompletionStateAfterRecall()
	return true
}
