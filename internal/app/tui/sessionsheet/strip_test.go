package sessionsheet

import "marshal/internal/app/tui/theme"

// StripANSI removes SGR escapes so tests can assert on visible runes.
func StripANSI(s string) string { return theme.ANSIRe.ReplaceAllString(s, "") }
