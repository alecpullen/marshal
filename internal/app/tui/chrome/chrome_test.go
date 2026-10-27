package chrome

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func TestPanelRendersOpenFrame(t *testing.T) {
	out := Panel("Settings", "line one\nline two", 40, 10, true, testTheme)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "▍") {
		t.Fatalf("panel still uses the old gutter glyph:\n%s", plain)
	}
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("want top border + 2 content rows = 3, got %d:\n%s", len(lines), plain)
	}
	if !strings.HasPrefix(lines[0], "╭─ ") || !strings.HasSuffix(lines[0], "╮") {
		t.Fatalf("first row is not an open top border: %q", lines[0])
	}
	if !strings.Contains(lines[0], "Settings") {
		t.Fatalf("top border missing title: %q", lines[0])
	}
	for i, l := range lines[1:] {
		if !strings.HasPrefix(l, "│ ") || !strings.HasSuffix(l, " │") {
			t.Fatalf("row %d is not a content row: %q", i+1, l)
		}
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != 42 {
			t.Fatalf("row %d is %d cells wide, want w+2 = 42: %q", i, got, l)
		}
	}
	if strings.Contains(plain, "╰") {
		t.Fatalf("an open frame must not draw a bottom border:\n%s", plain)
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
	hintEnd := strings.Index(header, "Esc close") + len("Esc close")
	if gap := ansi.StringWidth(header[hintEnd:strings.LastIndex(header, "╮")]); gap != 1 {
		t.Fatalf("hints end %d cells before the top border's ╮, want 2: %q", gap+1, header)
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

func TestClipLinesNoBlankFirstRowWhenNotScrolled(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("row-%02d", i)
	}
	out := ansi.Strip(ClipLines(lines, 0, 8, testTheme))
	got := strings.Split(out, "\n")
	if len(got) > 8 {
		t.Fatalf("must not exceed height 8, got %d lines", len(got))
	}
	if strings.TrimSpace(got[0]) == "" {
		t.Fatalf("first row is blank when focus is at top; want row content:\n%s", out)
	}
	if got[0] != "row-00" {
		t.Fatalf("first row = %q, want row-00", got[0])
	}
}
