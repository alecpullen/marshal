package settings

import "regexp"

// ansiRe matches the SGR escape sequences lipgloss v2 always emits; tests
// strip them so assertions can match visible text.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }
