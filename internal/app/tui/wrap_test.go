package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestWrapBreakpointsKeepReferencesIntact pins F13: every ansi.Wrap call site
// passed "" for breakpoints, so a file reference split at an arbitrary
// character. A coding agent's transcript is mostly file references, and a
// split one stops being copyable or clickable in every terminal that detects
// them.
func TestWrapBreakpointsKeepReferencesIntact(t *testing.T) {
	const ref = "internal/app/tui/listpanel/fieldlist.go:624"
	s := "see " + ref + " and https://example.com/a/very/long/path/segment?x=1 ok"

	got := ansi.Wrap(s, 40, WrapBreakpoints)
	for _, line := range strings.Split(got, "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Errorf("line exceeds the limit: %q", line)
		}
	}
	// The reference may wrap at its own separators, but must never be split
	// at a position that is not a breakpoint character.
	if strings.Contains(got, "fieldlist.go:\n") {
		t.Errorf("reference split after the colon:\n%s", got)
	}
	if strings.Contains(got, "fie\nldlist") || strings.Contains(got, "segme\nnt") {
		t.Errorf("token split mid-word:\n%s", got)
	}
}

// TestWrapBreakpointsStillRespectLimit guards the obvious regression: a
// breakpoint set must not let a line exceed its width.
func TestWrapBreakpointsStillRespectLimit(t *testing.T) {
	long := strings.Repeat("a", 200) + " " + strings.Repeat("b/", 60)
	for _, limit := range []int{10, 20, 40, 80} {
		for _, line := range strings.Split(ansi.Wrap(long, limit, WrapBreakpoints), "\n") {
			if ansi.StringWidth(line) > limit {
				t.Errorf("limit=%d: line is %d cells: %q", limit, ansi.StringWidth(line), line)
			}
		}
	}
}
