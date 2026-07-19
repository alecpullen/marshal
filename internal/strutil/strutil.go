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

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }
