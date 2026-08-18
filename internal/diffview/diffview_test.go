package diffview

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const sampleUnified = `--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo
+
 import "fmt"
@@ -10,5 +11,6 @@
 func old() {
-	return 1
+	return 2
+	return 3
 }
`

func TestParseHunks(t *testing.T) {
	hunks, err := parseUnifiedDiff(sampleUnified)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(hunks))
	}
	if hunks[0].OldStart != 1 || hunks[0].NewStart != 1 {
		t.Fatalf("hunk0 starts = (%d,%d)", hunks[0].OldStart, hunks[0].NewStart)
	}
	if len(hunks[0].Lines) != 4 {
		// hunk header + 1 context + 1 added + 1 context
		t.Fatalf("hunk0 lines = %d", len(hunks[0].Lines))
	}
	var added, removed int
	for _, ln := range hunks[1].Lines {
		switch ln.Kind {
		case LineAdded:
			added++
		case LineRemoved:
			removed++
		}
	}
	if added != 2 || removed != 1 {
		t.Fatalf("hunk1 added=%d removed=%d", added, removed)
	}
	if hunks[0].FilePath != "foo.go" {
		t.Fatalf("hunk0 FilePath = %q, want foo.go", hunks[0].FilePath)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := parseUnifiedDiff("not a diff at all"); err == nil {
		t.Fatal("expected parse error for non-diff input")
	}
}

func TestParseUnifiedDiffNoNewlineMarker(t *testing.T) {
	diff := `--- a/foo.go
+++ b/foo.go
@@ -1,1 +1,1 @@
-old line
\ No newline at end of file
+new line
`
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parse failed on no-newline marker: %v", err)
	}
	if len(hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(hunks))
	}
	// The old line should be classified as removed, the new line as added.
	var removed, added int
	for _, ln := range hunks[0].Lines {
		switch ln.Kind {
		case LineRemoved:
			removed++
		case LineAdded:
			added++
		}
	}
	if removed != 1 {
		t.Fatalf("removed lines = %d, want 1", removed)
	}
	if added != 1 {
		t.Fatalf("added lines = %d, want 1", added)
	}
}

func TestRenderUnifiedFallbackPlainText(t *testing.T) {
	out := Render(sampleUnified, Options{Width: 40, Mode: ModeUnified, Highlight: false})
	if !strings.Contains(out, "return 2") {
		t.Fatalf("unified render missing content:\n%s", out)
	}
}

func TestRenderSideBySideWide(t *testing.T) {
	out := Render(sampleUnified, Options{Width: 160, Mode: ModeSideBySide, Highlight: false})
	if !strings.Contains(out, "1") || !strings.Contains(out, "2") {
		t.Fatalf("side-by-side render missing removed/added content:\n%s", out)
	}
	if !strings.Contains(out, "│") {
		t.Fatalf("side-by-side render missing separator:\n%s", out)
	}
}

func TestRenderModeAutoSelectsSideBySideAt120(t *testing.T) {
	out := Render(sampleUnified, Options{Width: 160, Mode: ModeAuto, Highlight: false})
	if !strings.Contains(out, "│") {
		t.Fatalf("ModeAuto at width 160 should render side-by-side:\n%s", out)
	}
}

func TestRenderModeAutoSelectsUnifiedAt80(t *testing.T) {
	out := Render(sampleUnified, Options{Width: 80, Mode: ModeAuto, Highlight: false})
	if strings.Contains(out, "│") {
		t.Fatalf("ModeAuto at width 80 should not have a separator:\n%s", out)
	}
}

func TestRenderUnifiedTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("x", 400)
	diff := fmt.Sprintf("--- a/f.go\n+++ b/f.go\n@@ -1,1 +1,1 @@\n-%s\n+%s\n", long, long)
	out := Render(diff, Options{Width: 75, Mode: ModeUnified, Highlight: false})
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if w := lipgloss.Width(line); w > 75 {
			t.Fatalf("unified line width = %d, want <= 75: %q", w, ansi.Strip(line))
		}
	}
}

func TestRenderFallsBackOnGarbage(t *testing.T) {
	out := Render("not a diff at all", Options{Width: 80, Mode: ModeUnified, Highlight: true})
	if !strings.Contains(out, "not a diff at all") {
		t.Fatalf("plain text fallback dropped content:\n%s", out)
	}
}

func TestRenderTruncatesLargeDiffs(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- a/big.go\n+++ b/big.go\n")
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "@@ -%d,1 +%d,1 @@\n-old line\n+new line\n", i+1, i+1)
	}
	out := Render(b.String(), Options{Width: 80, Mode: ModeUnified, Highlight: false})
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation note in output")
	}
}

func TestRenderSideBySideHighlightDoesNotCorruptEmphasis(t *testing.T) {
	// When highlighting is enabled, emphasis offsets (computed from
	// un-highlighted content) are incompatible with the highlighted
	// string. The fix skips emphasis when highlighting is on.
	diff := `--- a/foo.go
+++ b/foo.go
@@ -1,1 +1,1 @@
-return 1
+return 2
`
	// The emphasis styles render as bold color codes (1;91 for removed,
	// 1;92 for added). When highlighting is on, emphasis must be skipped so
	// these codes never appear; when highlighting is off, they must be
	// present. This directly detects the corruption the fix prevents: if
	// emphasis were applied to the highlighted string, the mismatched byte
	// offsets would emit garbled partial ANSI sequences.
	const (
		remEmphCode = "\x1b[1;91m"
		addEmphCode = "\x1b[1;92m"
	)

	out := Render(diff, Options{Width: 160, Mode: ModeSideBySide, Highlight: true})
	if strings.Contains(out, remEmphCode) || strings.Contains(out, addEmphCode) {
		t.Fatalf("emphasis applied while highlighting is on (corruption):\n%q", out)
	}
	if !strings.Contains(out, "return") {
		t.Fatalf("content missing from highlighted render:\n%q", out)
	}

	outNoHL := Render(diff, Options{Width: 160, Mode: ModeSideBySide, Highlight: false})
	if !strings.Contains(outNoHL, remEmphCode) && !strings.Contains(outNoHL, addEmphCode) {
		t.Fatalf("emphasis not applied when highlighting is off:\n%q", outNoHL)
	}
	if !strings.Contains(outNoHL, "return") {
		t.Fatalf("content missing from non-highlighted render:\n%q", outNoHL)
	}
}

func TestComputeEmphasisFindsDifferingSubstrings(t *testing.T) {
	le, re := computeEmphasis("return 1", "return 2")
	if le == nil || re == nil {
		t.Fatal("expected non-nil emph ranges")
	}
	if len(le.runs) == 0 || len(re.runs) == 0 {
		t.Fatalf("expected at least one emph run: left=%v right=%v", le, re)
	}
}

func TestComputeEmphasisNoDiff(t *testing.T) {
	le, re := computeEmphasis("same", "same")
	if le != nil || re != nil {
		t.Fatalf("expected nil emph for equal lines: %v %v", le, re)
	}
}

// TestTruncateVisibleKeepsANSISequencesIntact: truncation must happen on
// visible cells, not raw runes — slicing styled text by runes cuts through
// escape sequences and bleeds color into the next column.
func TestTruncateVisibleKeepsANSISequencesIntact(t *testing.T) {
	plain := strings.Repeat("x", 100)
	styled := "\x1b[38;5;196m" + plain + "\x1b[0m"

	out := truncateVisible(styled, 40)
	if w := lipgloss.Width(out); w > 40 {
		t.Fatalf("visible width = %d, want <= 40", w)
	}
	if got := ansi.Strip(out); got != strings.Repeat("x", 40) {
		t.Fatalf("visible text = %q, want %q", got, strings.Repeat("x", 40))
	}
}

func TestPairLinesGroupsRemovedAdded(t *testing.T) {
	h := Hunk{
		OldStart: 1,
		NewStart: 1,
		Lines: []Line{
			{Kind: LineContext, Content: "ctx"},
			{Kind: LineRemoved, Content: "old"},
			{Kind: LineAdded, Content: "new"},
			{Kind: LineContext, Content: "ctx2"},
		},
	}
	pairs := pairLines(h.Lines)
	if len(pairs) != 3 {
		t.Fatalf("pairs = %d, want 3 (ctx, removed+added paired, ctx)", len(pairs))
	}
	if pairs[1].left == nil || pairs[1].right == nil {
		t.Fatal("expected removed/added pair in middle")
	}
	if pairs[1].left.Content != "old" || pairs[1].right.Content != "new" {
		t.Fatalf("pair contents = (%q, %q)", pairs[1].left.Content, pairs[1].right.Content)
	}
}
