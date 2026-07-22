package chrome

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func TestPanelRendersGutterFrame(t *testing.T) {
	out := Panel("Settings", "line one\nline two", 40, 10, true, testTheme)
	plain := ansi.Strip(out)
	for _, glyph := range []string{"╭", "╰", "│", "─"} {
		if strings.Contains(plain, glyph) {
			t.Fatalf("panel still uses box glyph %q:\n%s", glyph, plain)
		}
	}
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 content rows = 3, got %d:\n%s", len(lines), plain)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, " ▍ ") {
			t.Fatalf("row %d missing gutter prefix: %q", i, l)
		}
	}
	if !strings.Contains(lines[0], "Settings") {
		t.Fatalf("header missing title: %q", lines[0])
	}
}

func TestPanelWithHintsRightAligns(t *testing.T) {
	out := ansi.Strip(PanelWithHints("Memory", "Esc close", "body", 40, 10, true, testTheme))
	header := strings.Split(out, "\n")[0]
	if !strings.Contains(header, "Memory") || !strings.Contains(header, "Esc close") {
		t.Fatalf("header missing title or hints: %q", header)
	}
	if strings.Index(header, "Esc close") < strings.Index(header, "Memory") {
		t.Fatalf("hints should be right of the title: %q", header)
	}
}

func TestPanelTruncatesToHeightBudget(t *testing.T) {
	out := ansi.Strip(Panel("T", "a\nb\nc\nd\ne", 40, 3, true, testTheme))
	if got := len(strings.Split(out, "\n")); got != 3 {
		t.Fatalf("panel must clamp to h rows, got %d:\n%s", got, out)
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
