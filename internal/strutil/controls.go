package strutil

import (
	"fmt"
	"strings"
)

// IsDisplayControl reports whether r is a character a renderer acts on
// instead of showing: a C0 control other than newline and tab, DEL, a C1
// control, or a bidirectional formatting character. A terminal executes
// ESC, CR and the C1 introducers (cursor moves, erases, concealed text), and
// a bidi-aware renderer reorders the text around an override or isolate, so
// a string carrying any of them can display as something other than what it
// is. Newline and tab are layout, not control, and are allowed.
func IsDisplayControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
		return true
	}
	switch r {
	case 0x061c, 0x200e, 0x200f, // Arabic letter mark, LRM, RLM
		0x202a, 0x202b, 0x202c, 0x202d, 0x202e, // embeddings and overrides
		0x2066, 0x2067, 0x2068, 0x2069: // isolates
		return true
	}
	return false
}

// FirstDisplayControl returns the first display control in s, if any.
func FirstDisplayControl(s string) (rune, bool) {
	for _, r := range s {
		if IsDisplayControl(r) {
			return r, true
		}
	}
	return 0, false
}

// ShowControls replaces every display control in s with a visible escape —
// `\x1b`, `\r`, `\u202e` — and leaves everything else, newlines and tabs
// included, unchanged. It is for text the user is asked to authorise, where
// a control must be seen rather than silently removed: removing ESC from
// "ESC[2K" leaves an innocuous-looking "[2K", while "\x1b[2K" says what the
// text really holds.
func ShowControls(s string) string {
	if !strings.ContainsFunc(s, IsDisplayControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case !IsDisplayControl(r):
			b.WriteRune(r)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}
