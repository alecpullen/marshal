package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// pasteCondenseMinLines and pasteCondenseMinChars are the thresholds above
// which a bracketed paste is collapsed into a chip instead of being dumped
// into the textarea. Small pastes keep the legacy inline behavior.
const (
	pasteCondenseMinLines = 8
	pasteCondenseMinChars = 1500
)

// pasteAttachment holds the full content of one condensed paste. Chips in
// the textarea reference attachments by N (1-based, per-input-session).
type pasteAttachment struct {
	N       int
	Lines   int
	Chars   int
	Content string
}

// shouldCondensePaste reports whether content is large enough to collapse.
func shouldCondensePaste(content string) bool {
	return strings.Count(content, "\n")+1 >= pasteCondenseMinLines ||
		len(content) > pasteCondenseMinChars
}

// chipText returns the placeholder inserted into the textarea.
func (p pasteAttachment) chipText() string {
	return fmt.Sprintf("[pasted #%d · %d lines]", p.N, p.Lines)
}

// addPaste condenses a large paste into an attachment and inserts a chip at
// the current cursor. The full content never enters the textarea, avoiding
// the CharLimit silent-truncation path. The attachment is recorded only after
// the whole chip has landed, so a refused insert never leaves a partial chip
// or an attachment no chip can expand; a refusal is reported with a flash
// naming the limit that rejected it and leaves the draft exactly as it was.
func (m *Model) addPaste(content string) tea.Cmd {
	lines := strings.Count(content, "\n") + 1
	att := pasteAttachment{
		N:       len(m.pastes) + 1,
		Lines:   lines,
		Chars:   len(content),
		Content: content,
	}
	chip := att.chipText()
	// InsertString silently truncates to the remaining CharLimit, and drops the
	// insert altogether once the textarea's line budget is spent. Preflight the
	// characters the textarea would compare (Length is what it measures against
	// CharLimit; the chip is ASCII plus one middle dot, so its rune count is its
	// width) and verify the whole chip afterwards: either the complete chip
	// lands at the cursor or nothing changes.
	if m.input.CharLimit > 0 && m.input.Length()+len([]rune(chip)) > m.input.CharLimit {
		return m.setFlash(fmt.Sprintf("Paste did not fit: the input limit is %d characters", m.input.CharLimit))
	}
	before := m.input.Value()
	m.input.InsertString(chip)
	if after := m.input.Value(); !strings.Contains(after, chip) ||
		len([]rune(after)) != len([]rune(before))+len([]rune(chip)) {
		// The line/height guard refused the insert (possibly after trimming it):
		// restore the draft and leave the attachments alone.
		if m.input.Value() != before {
			m.input.SetValue(before)
		}
		return m.setFlash("Paste did not fit: the input has reached its line limit")
	}
	m.pastes = append(m.pastes, att)
	m.updateViewportHeight()
	return nil
}

// expandPastes replaces each chip in value with its full fenced content.
// Attachments whose chip no longer appears in value are dropped. The result
// is the prompt that history, steering, and the agent will see.
func (m *Model) expandPastes(value string) string {
	kept := make([]pasteAttachment, 0, len(m.pastes))
	for _, att := range m.pastes {
		token := att.chipText()
		if !strings.Contains(value, token) {
			continue
		}
		kept = append(kept, att)
		block := "\n\n```\n" + strings.TrimRight(att.Content, "\n") + "\n```\n"
		value = strings.Replace(value, token, block, 1)
	}
	m.pastes = kept
	return value
}

// resetInput clears the textarea and any pending paste attachments. It must
// be called at every site that previously called m.input.Reset() so stale
// attachments do not expand into later prompts.
func (m *Model) resetInput() {
	m.input.Reset()
	m.pastes = nil
}
