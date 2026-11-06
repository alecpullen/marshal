// Package strutil holds small shared string helpers used across packages.
// It exists so that truncation, pointer, and compact-number helpers have
// exactly one implementation instead of per-package copies with divergent
// semantics.
package strutil

import (
	"fmt"
	"strings"
	"time"
)

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

// HumanAge renders a duration compactly: "30m", "3h", "2d", "5w". A
// non-positive duration (unknown age) renders as "?".
func HumanAge(d time.Duration) string {
	if d <= 0 {
		return "?"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw", int(d.Hours()/(24*7)))
	}
}

// CompactDuration renders a countdown compactly with two units at most:
// "45s", "12m", "4h7m", "6d23h". It is for quota reset countdowns, where
// the exact minute matters but seconds past the first hour do not.
//
// A non-positive duration renders as "now" — a reset that has already
// passed is better described as imminent than as a negative number.
func CompactDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) - days*24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
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
		// If the basename fits within max, return just the basename.
		// No prefix or ellipsis — the basename is the most important part.
		if len(bnRunes) <= max {
			return basename
		}
		// Basename itself doesn't fit — middle-truncate just the basename.
		left := (max - 1) / 2
		right := (max - 1) - left
		return string(bnRunes[:left]) + "…" + string(bnRunes[len(bnRunes)-right:])
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

// MaskKey renders a secret for display: •••• plus its last four characters,
// •••• alone for one shorter than four, and "(not set)" for none. A value
// that is already a mask is returned unchanged.
//
// The real value is never rendered; save paths always use the raw value.
// The last-four suffix is a deliberate design choice (per spec): it
// helps the user confirm they are looking at the expected key without
// exposing the full value.
func MaskKey(key string) string {
	if IsMasked(key) {
		return key
	}
	if key == "" {
		return "(not set)"
	}
	runes := []rune(key)
	if len(runes) < 4 {
		return maskPrefix
	}
	return maskPrefix + string(runes[len(runes)-4:])
}

// maskPrefix is the bullet run every mask begins with.
const maskPrefix = "••••"

// IsMasked reports whether s is a mask MaskKey produced. No real secret
// begins with its bullets.
func IsMasked(s string) bool { return strings.HasPrefix(s, maskPrefix) }

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }

// UniqueName returns base, or base-2, base-3 and on: the first that existing
// does not hold.
func UniqueName(base string, existing map[string]bool) string {
	if !existing[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !existing[candidate] {
			return candidate
		}
	}
}
