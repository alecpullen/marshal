// Package strutil holds small shared string helpers used across packages.
// It exists so that truncation, pointer, and compact-number helpers have
// exactly one implementation instead of per-package copies with divergent
// semantics.
package strutil

import "fmt"

// Truncate returns s shortened to at most max runes. When ellipsis is true
// and truncation occurred, "…" is appended (so the result may be max+1
// runes). Rune-aware: multi-byte characters are never split. A max <= 0
// returns the empty string.
func Truncate(s string, max int, ellipsis bool) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	out := string(runes[:max])
	if ellipsis {
		out += "…"
	}
	return out
}

// CompactTokens renders a token count compactly: "842", "18k".
func CompactTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// TruncateMiddle returns s shortened to at most max runes by replacing the
// middle with "…" when truncation is needed. The result is at most max runes
// (the ellipsis replaces characters, it is not appended). Rune-aware:
// multi-byte characters are never split. A max < 3 returns the empty string
// (too small for a meaningful middle-truncation).
//
// When s contains a "/" separator, TruncateMiddle tries to keep the last
// segment (the basename) intact, truncating the prefix instead. If the
// basename itself is longer than max, it falls back to even split.
func TruncateMiddle(s string, max int) string {
	if max < 3 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	// Try to keep the basename (last "/" segment) intact.
	if lastSlash := lastRuneIndex(s, '/'); lastSlash >= 0 {
		basename := s[lastSlash+1:]
		bnRunes := []rune(basename)
		if len(bnRunes) < max {
			// Keep the basename, truncate the prefix.
			prefixMax := max - 1 - len(bnRunes) // 1 for the "…"
			if prefixMax > 0 {
				prefix := []rune(s[:lastSlash])
				if len(prefix) > prefixMax {
					prefix = prefix[:prefixMax]
				}
				return string(prefix) + "…" + basename
			}
		}
	}
	// Fallback: keep roughly half on each side.
	left := (max - 1) / 2
	right := (max - 1) - left
	return string(runes[:left]) + "…" + string(runes[len(runes)-right:])
}

// lastRuneIndex returns the byte index of the last occurrence of b in s,
// or -1 if not found. Works on the byte level (safe for ASCII separators).
func lastRuneIndex(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }
