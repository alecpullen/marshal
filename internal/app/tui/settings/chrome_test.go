package settings

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderPanelEmbedsTitleAndSizes(t *testing.T) {
	out := renderPanel("Shell", "hello", 30, 6, true)
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
	if !strings.Contains(plain, "hello") {
		t.Fatalf("content missing:\n%s", plain)
	}
}

func TestRenderPanelFocusChangesBorderColor(t *testing.T) {
	focused := renderPanel("S", "x", 20, 4, true)
	blurred := renderPanel("S", "x", 20, 4, false)
	if focused == blurred {
		t.Fatal("focused and unfocused panels should differ (border color)")
	}
}
