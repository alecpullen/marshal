package chrome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func TestPanelEmbedsTitleAndSizes(t *testing.T) {
	out := Panel("Shell", "hello", 30, 6, true, testTheme)
	plain := ansi.Strip(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d:\n%s", len(lines), plain)
	}
	if !strings.Contains(lines[0], "╭") || !strings.Contains(lines[0], " Shell ") {
		t.Fatalf("top border should embed the title, got %q", lines[0])
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 30 {
			t.Fatalf("line %d should be width 30, got %d: %q", i, w, l)
		}
	}
}

func TestClipLinesWindowsAroundFocus(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "row"
	}
	out := ClipLines(lines, 29, 8, testTheme)
	got := strings.Split(out, "\n")
	if len(got) > 8 {
		t.Fatalf("must not exceed height 8, got %d lines", len(got))
	}
	if !strings.Contains(out, "↑") {
		t.Fatalf("expected ↑ more indicator when scrolled, got:\n%s", out)
	}
}
