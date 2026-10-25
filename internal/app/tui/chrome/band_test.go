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

// bandCells reports, for every printable cell of an SGR-styled line, whether
// a background was in effect when it was drawn: any 48 or 40–47/100–107 sets
// one, and a bare or 0 reset or a 49 clears it. The fixtures only use the
// shapes this models.
func bandCells(t *testing.T, line string) []bool {
	t.Helper()
	var cells []bool
	painted := false
	var state byte
	for len(line) > 0 {
		seq, width, n, next := ansi.DecodeSequence(line, state, nil)
		switch {
		case strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m"):
			body := seq[2 : len(seq)-1]
			switch {
			case body == "" || body == "0" || strings.HasPrefix(body, "0;") || body == "49":
				painted = strings.Contains(body, ";48;")
			case strings.HasPrefix(body, "48;"), strings.Contains(body, ";48;"):
				painted = true
			}
		case width > 0:
			for range width {
				cells = append(cells, painted)
			}
		}
		state, line = next, line[n:]
	}
	return cells
}

// Styled segments end in a reset. The band must outlast every one of them and
// paint the padding after the last, or a tinted row shows the terminal
// background everywhere but its first segment.
func TestPaintBandSurvivesStyledSegments(t *testing.T) {
	in := "\x1b[38;5;81m▌┆ job-3  \x1b[mDownload  \x1b[38;5;247m2m 0s\x1b[m \x1b[0m\x1b[38;5;81m▸\x1b[m"
	out := PaintBand(in, 30, lipgloss.Color("236"))
	cells := bandCells(t, out)
	if len(cells) != 30 {
		t.Fatalf("band has %d cells, want 30: %q", len(cells), out)
	}
	for i, painted := range cells {
		if !painted {
			t.Fatalf("cell %d is on the terminal background: %q", i, out)
		}
	}
}

// A background set inside the band is the content's own highlight, so the band
// must not paint over it.
func TestPaintBandKeepsAnInnerBackground(t *testing.T) {
	in := "a\x1b[48;5;209mX\x1b[mb"
	out := PaintBand(in, 5, lipgloss.Color("236"))
	if !strings.Contains(out, "\x1b[48;5;209mX") {
		t.Errorf("the inner background was painted over: %q", out)
	}
}

func TestClearsBackground(t *testing.T) {
	for _, tc := range []struct {
		seq  string
		want bool
	}{
		{"\x1b[m", true},
		{"\x1b[0m", true},
		{"\x1b[49m", true},
		{"\x1b[0;38;5;81m", true},
		{"\x1b[38;5;49m", false},     // foreground colour 49
		{"\x1b[38;2;0;49;0m", false}, // truecolor foreground with a 49 channel
		{"\x1b[0;48;5;236m", false},
		{"\x1b[0;44m", false},
		{"\x1b[0;48:5:236m", false},
		{"\x1b[38;5;81m", false},
		{"\x1b[1m", false},
		{"\x1b[2K", false},
		{"x", false},
	} {
		if got := clearsBackground(tc.seq); got != tc.want {
			t.Errorf("clearsBackground(%q) = %v, want %v", tc.seq, got, tc.want)
		}
	}
}
