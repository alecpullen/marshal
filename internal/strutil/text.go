package strutil

import (
	"strings"
	"unicode"
)

// ElideWorkdir shortens a command that starts by changing into the session's
// working tree: "cd <root> && pytest" reads as "⌂ pytest".
//
// A shell command that has to run in the worktree has to say so, and the
// path is both long and already implied by the row's context. Only a leading
// cd into the root, followed by a separator, is elided: a cd somewhere else
// is information the reader needs.
func ElideWorkdir(cmd, root string) string {
	if root == "" {
		return cmd
	}
	for _, sep := range []string{" && ", "; "} {
		for _, q := range []string{root, `"` + root + `"`, "'" + root + "'"} {
			if p := "cd " + q + sep; strings.HasPrefix(cmd, p) {
				return "⌂ " + strings.TrimPrefix(cmd, p)
			}
		}
	}
	return cmd
}

// FirstSentence splits s at the first ". ", "! ", "? " or newline that comes
// after at least 8 runes. A sentence-final mark at the very end of s stays
// with the head.
func FirstSentence(s string) (head, rest string) {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\n':
			if i >= 8 {
				return strings.TrimSpace(string(runes[:i])), strings.TrimSpace(string(runes[i+1:]))
			}
		case (r == '.' || r == '!' || r == '?') && i >= 7:
			if i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
				return string(runes[:i+1]), strings.TrimSpace(string(runes[i+1:]))
			}
		}
	}
	return s, ""
}

// StripEmphasis removes markdown emphasis and code markers from a headline,
// which is rendered as plain text.
func StripEmphasis(s string) string {
	return strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "~~", "").Replace(s)
}
