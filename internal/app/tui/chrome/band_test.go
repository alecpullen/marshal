package chrome

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The whole depth design rests on this: with no background, PaintBand must
// return its input unchanged — no padding, no escape sequences, nothing.
func TestPaintBandNoColorIsIdentity(t *testing.T) {
	for _, in := range []string{
		"",
		"one line",
		"two\nlines",
		"ragged\nlines of differing width\nx",
		"\x1b[38;5;209mcoloured\x1b[m",
		"trailing newline\n",
	} {
		if got := PaintBand(in, 40, lipgloss.NoColor{}); got != in {
			t.Errorf("PaintBand(%q, NoColor) = %q, want the input unchanged", in, got)
		}
	}
}

// A nil background is treated the same as NoColor — a zero Theme slot must
// not panic or paint.
func TestPaintBandNilIsIdentity(t *testing.T) {
	in := "content"
	if got := PaintBand(in, 40, nil); got != in {
		t.Errorf("PaintBand(nil bg) = %q, want %q", got, in)
	}
}

// With a real background every line is padded to exactly w cells, so the band
// has a straight right edge. A ragged band is the "light stripe" artifact
// recorded in view.go's chromeRailWidth comment.
func TestPaintBandPadsEveryLineToWidth(t *testing.T) {
	out := PaintBand("short\nrather longer line", 30, lipgloss.Color("236"))
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != 30 {
			t.Errorf("line %d width = %d, want 30 (ragged band): %q", i, w, line)
		}
	}
}

// Content wider than the band is truncated rather than allowed to overflow
// and wrap, which would break the frame-height invariant.
func TestPaintBandTruncatesOverlongLines(t *testing.T) {
	out := PaintBand(strings.Repeat("x", 50), 20, lipgloss.Color("236"))
	if w := ansi.StringWidth(out); w != 20 {
		t.Errorf("width = %d, want 20", w)
	}
}

// A trailing newline is structural — it separates stacked regions — so it
// must survive painting rather than becoming a padded blank band.
func TestPaintBandPreservesTrailingNewline(t *testing.T) {
	out := PaintBand("a\n", 10, lipgloss.Color("236"))
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("trailing newline lost: %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("trailing newline became a band row: %q", out)
	}
}

func TestPaintBandZeroWidthIsIdentity(t *testing.T) {
	if got := PaintBand("x", 0, lipgloss.Color("236")); got != "x" {
		t.Errorf("PaintBand(w=0) = %q, want %q", got, "x")
	}
}
