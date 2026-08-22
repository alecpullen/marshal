package theme

import "regexp"

// ANSIRe matches ANSI escape sequences used for terminal styling. It is
// shared across TUI packages to avoid duplicate definitions.
var ANSIRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
