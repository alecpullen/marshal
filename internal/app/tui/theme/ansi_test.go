package theme

import (
	"regexp"
	"testing"
)

func TestANSIReStripsEscapeSequences(t *testing.T) {
	// The shared regex must match ANSI escape sequences.
	input := "\x1b[31mred text\x1b[0m"
	cleaned := ANSIRe.ReplaceAllString(input, "")
	if cleaned != "red text" {
		t.Errorf("expected 'red text', got %q", cleaned)
	}
}

func TestANSIReIsRegexp(t *testing.T) {
	// Sanity: the variable is a compiled regex we can use.
	var _ *regexp.Regexp = ANSIRe
}
