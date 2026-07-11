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

func TestOverlayCentersPanelOverBackground(t *testing.T) {
	bgLine := strings.Repeat("x", 40)
	bg := strings.Join([]string{bgLine, bgLine, bgLine, bgLine, bgLine}, "\n")
	panel := "PAN\nEL!"
	out := Overlay(bg, panel, 40, 5)
	lines := strings.Split(ansi.Strip(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
	// panel rows land centered: y = (5-2)/2 = 1, x = (40-3)/2 = 18
	if !strings.Contains(lines[1], "PAN") || !strings.Contains(lines[2], "EL!") {
		t.Fatalf("panel not spliced in:\n%s", ansi.Strip(out))
	}
	if !strings.HasPrefix(lines[1], strings.Repeat("x", 18)) {
		t.Fatalf("background left of panel should survive, got %q", lines[1])
	}
	if !strings.HasSuffix(lines[1], strings.Repeat("x", 19)) {
		t.Fatalf("background right of panel should survive, got %q", lines[1])
	}
	if lines[0] != bgLine || lines[4] != bgLine {
		t.Fatalf("rows outside the panel must be untouched")
	}
}

func TestOverlaySurvivesStyledBackground(t *testing.T) {
	styled := "\x1b[31m" + strings.Repeat("r", 40) + "\x1b[0m"
	bg := strings.Join([]string{styled, styled, styled}, "\n")
	out := Overlay(bg, "OK", 40, 3)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "OK") {
		t.Fatalf("panel missing over styled bg:\n%s", plain)
	}
	for i, l := range strings.Split(plain, "\n") {
		if w := len([]rune(l)); w != 40 {
			t.Fatalf("line %d width %d, want 40: %q", i, w, l)
		}
	}
}
