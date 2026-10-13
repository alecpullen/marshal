package viewmodel

import "strings"

// Display text in the transcript comes from the model: a todo item's content,
// a subagent's label, the command a job runs, a job's own output. Every one of
// those can carry a terminal control sequence — OSC 52 writes the clipboard,
// OSC 0 rewrites the window title, OSC 8 forges a hyperlink, CSI 2J clears the
// screen — and the transcript renders them verbatim, once per frame, on the
// user's terminal. A model that emits such a sequence is writing into the
// terminal rather than describing work, so the escape is removed before the
// text reaches any renderer.
//
// Both scrubbers are display-only: node IDs, keys and filters are computed
// from the raw values, never from a scrubbed copy, so two rows can never
// collide because scrubbing merged them. Both are idempotent, and both leave
// every printable byte alone — a path, a command, an emoji and a CJK glyph all
// survive unchanged. Scrubbing must never mangle legitimate text.
//
// ScrubText is for single-line display text (a card's label, a command, a
// todo's one-line preview). Line breaks and tabs become spaces rather than
// being dropped, so "line one\nline two" reads as two words instead of one.
func ScrubText(s string) string {
	if !strings.ContainsFunc(s, isControlRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if isControlRune(r) {
			return -1
		}
		return r
	}, s)
}

// ScrubBlock is for multi-line display text whose line structure is the
// content: a job's expanded output, a child's summary. Newlines and tabs are
// kept, carriage returns are dropped so CRLF collapses to LF rather than
// producing stray blank lines.
func ScrubBlock(s string) string {
	if !strings.ContainsFunc(s, isControlRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if isControlRune(r) {
			return -1
		}
		return r
	}, s)
}

// isControlRune reports whether r is a control character that must never
// reach the terminal as part of display text: C0 (including ESC), DEL, and C1.
// The C1 range is reachable in UTF-8 as U+0080..U+009F, so it has to be
// filtered by rune value and not by byte.
func isControlRune(r rune) bool {
	if r == 0x7f {
		return true
	}
	return r < 0x20 || (r >= 0x80 && r <= 0x9f)
}
