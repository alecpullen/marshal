package chrome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

func stripANSI(s string) string { return theme.ANSIRe.ReplaceAllString(s, "") }

func TestHeaderRightAligns(t *testing.T) {
	got := stripANSI(Header("CHANGED", "3", 20))
	if ansi.StringWidth(got) != 20 {
		t.Errorf("width = %d, want 20: %q", ansi.StringWidth(got), got)
	}
	if !strings.HasPrefix(got, "CHANGED") {
		t.Errorf("got %q, want a CHANGED prefix", got)
	}
	if !strings.HasSuffix(got, " 3") {
		t.Errorf("got %q, want a ' 3' suffix", got)
	}
}

func TestHeaderDropsRightWhenNoRoom(t *testing.T) {
	out := stripANSI(Header("CONTEXT", "78%", 9))
	if ansi.StringWidth(out) != 9 {
		t.Fatalf("width = %d, want 9: %q", ansi.StringWidth(out), out)
	}
	if strings.Contains(out, "78%") {
		t.Errorf("want right dropped at narrow width, got %q", out)
	}
}

func TestHeaderRendersRule(t *testing.T) {
	out := stripANSI(Header("CONTEXT", "", 20))
	if ansi.StringWidth(out) != 20 {
		t.Fatalf("width = %d, want 20: %q", ansi.StringWidth(out), out)
	}
	if !strings.HasPrefix(out, "CONTEXT ") {
		t.Errorf("want title then space, got %q", out)
	}
	if !strings.Contains(out, "─") {
		t.Errorf("want a rule, got %q", out)
	}
	if strings.TrimRight(out, "─") != "CONTEXT " {
		t.Errorf("want rule to run to the edge, got %q", out)
	}
}

func TestHeaderRuleSeparatesTitleFromRight(t *testing.T) {
	out := stripANSI(Header("CONTEXT", "78%", 20))
	if ansi.StringWidth(out) != 20 {
		t.Fatalf("width = %d, want 20: %q", ansi.StringWidth(out), out)
	}
	if !strings.HasPrefix(out, "CONTEXT ") {
		t.Errorf("want title first, got %q", out)
	}
	if !strings.HasSuffix(out, " 78%") {
		t.Errorf("want right value last, got %q", out)
	}
	if !strings.Contains(out, "─") {
		t.Errorf("want a rule between title and value, got %q", out)
	}
}

func TestHeaderTruncatesLongTitle(t *testing.T) {
	out := stripANSI(Header("VERYLONGSECTIONTITLE", "", 8))
	if ansi.StringWidth(out) != 8 {
		t.Fatalf("width = %d, want 8: %q", ansi.StringWidth(out), out)
	}
}

func TestHeaderZeroWidth(t *testing.T) {
	if got := Header("CONTEXT", "", 0); got != "" {
		t.Errorf("want empty string at width 0, got %q", got)
	}
}
