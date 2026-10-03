package diffview

import (
	"fmt"
	"strings"
	"testing"
)

// bigDiff produces a unified diff whose rendered line count exceeds the cap, so
// truncation can be observed rather than assumed.
func bigDiff(lines int) string {
	var b strings.Builder
	b.WriteString("--- a/big.go\n+++ b/big.go\n")
	b.WriteString("@@ -1,1 +1,1 @@\n")
	for i := 0; i < lines; i++ {
		b.WriteString("+added line\n")
	}
	return b.String()
}

// multiHunkDiff builds a unified diff with one file header and one hunk per
// entry in hunks, each carrying that many added lines.
//
// Hunk sizes are given explicitly rather than derived, because what a hunk
// contributes to Lines differs by mode: unified renders the "@@" header line as
// a content line, while side-by-side pairs only the content and skips the
// header. A test that wants two hunks to land exactly on the budget has to size
// them for the mode it is in.
func multiHunkDiff(hunks ...int) string {
	var b strings.Builder
	b.WriteString("--- a/multi.go\n+++ b/multi.go\n")
	start := 1
	for _, n := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", start, n, start, n)
		for i := 0; i < n; i++ {
			b.WriteString("+added line\n")
		}
		start += n
	}
	return b.String()
}

// TestRenderResultReportsTruncation pins the fact the Changes view needs: the
// renderer caps its output, and a caller that cannot tell "this is the whole
// diff" from "this is the first 500 lines" will label a partial patch as
// complete. Task 7 must say which one it is showing.
func TestRenderResultReportsTruncation(t *testing.T) {
	t.Run("under the cap is not truncated", func(t *testing.T) {
		res := RenderResult("--- a/s.go\n+++ b/s.go\n@@ -1,1 +1,2 @@\n context\n+added\n", Options{Width: 80})
		if res.Truncated {
			t.Fatalf("a small diff reported Truncated = true (rendered %d lines)", res.Lines)
		}
		if !strings.Contains(res.Text, "added") {
			t.Fatalf("the rendered text lost its content: %q", res.Text)
		}
		// The CONCRETE count, not merely "non-zero": Lines is the number a
		// caller compares against the cap, so the small case has to pin it as
		// exactly as the capped cases do. One hunk of a header, a context line
		// and an added line is three rendered lines — the `---`/`+++` file
		// headers are not content and are never counted.
		if res.Lines != 3 {
			t.Fatalf("Lines = %d, want 3 (hunk header + context + added)", res.Lines)
		}
	})

	t.Run("over the cap is truncated", func(t *testing.T) {
		res := RenderResult(bigDiff(maxRenderLines*2), Options{Width: 80})
		if !res.Truncated {
			t.Fatalf("a diff of %d lines rendered %d lines and reported Truncated = false, want true", maxRenderLines*2, res.Lines)
		}
		if res.Lines > maxRenderLines+1 {
			// +1 tolerates the count of the final partial hunk.
			t.Fatalf("Lines = %d, want at most the cap %d", res.Lines, maxRenderLines)
		}
	})

	t.Run("an empty diff is not truncated", func(t *testing.T) {
		res := RenderResult("", Options{Width: 80})
		if res.Truncated {
			t.Fatal("an empty diff reported Truncated = true")
		}
	})
}

// TestRenderResultHonoursOneBudgetAcrossHunks pins the GLOBAL cap — the one
// property a per-hunk budget cannot express. Each hunk's renderer used to start
// its own line count against maxRenderLines, and the outer loop only tested the
// budget AFTER a hunk returned, so two hunks that each fitted the cap on their
// own both rendered in full: Lines reached roughly twice the cap, and because no
// individual renderer had dropped anything Truncated was false. The caller was
// handed an over-budget render labelled complete, which is the false statement
// the Result type exists to prevent.
func TestRenderResultHonoursOneBudgetAcrossHunks(t *testing.T) {
	// Width 160 keeps ModeSideBySide wide enough for the real two-column layout
	// rather than its unified fallback, so both modes are exercised for real.
	modes := []struct {
		name string
		mode Mode
	}{
		{"unified", ModeUnified},
		{"side-by-side", ModeSideBySide},
	}

	t.Run("two hunks that together exceed the cap stop at the cap", func(t *testing.T) {
		// 300 lines each: neither hunk alone reaches the cap, so only a shared
		// budget can notice that together they do.
		diff := multiHunkDiff(300, 300)
		for _, m := range modes {
			t.Run(m.name, func(t *testing.T) {
				res := RenderResult(diff, Options{Width: 160, Mode: m.mode})
				if res.Lines > maxRenderLines {
					t.Fatalf("Lines = %d, want <= the global cap %d: each hunk budgeted its own %d",
						res.Lines, maxRenderLines, maxRenderLines)
				}
				if !res.Truncated {
					t.Fatalf("two hunks of 300 lines rendered %d lines under a %d-line budget and reported Truncated = false",
						res.Lines, maxRenderLines)
				}
				if !strings.Contains(res.Text, "truncated") {
					t.Fatalf("Truncated = true with no notice in the render:\n%s", res.Text)
				}
				// The drop is real, not merely declared: the render holds no
				// more content lines than the budget allows.
				if n := strings.Count(res.Text, "added line"); n > maxRenderLines {
					t.Fatalf("the render carries %d content lines, want <= %d", n, maxRenderLines)
				}
			})
		}
	})

	t.Run("two hunks that end exactly at the cap are complete", func(t *testing.T) {
		// A diff that spends the budget exactly is not truncated: the reader
		// sees all of it, and crying truncation here would teach them to
		// distrust a full render. Sizes differ per mode because unified counts
		// each hunk's "@@" header and side-by-side does not, so the same diff
		// would be over budget in one mode and exactly on it in the other.
		for _, tc := range []struct {
			name  string
			mode  Mode
			hunks []int
		}{
			{"unified", ModeUnified, []int{249, 249}},         // 2 headers + 498 added
			{"side-by-side", ModeSideBySide, []int{250, 250}}, // 500 pairs
		} {
			t.Run(tc.name, func(t *testing.T) {
				res := RenderResult(multiHunkDiff(tc.hunks...), Options{Width: 160, Mode: tc.mode})
				if res.Lines != maxRenderLines {
					t.Fatalf("Lines = %d, want the full budget %d", res.Lines, maxRenderLines)
				}
				if res.Truncated {
					t.Fatal("a render that ends exactly at the budget was reported truncated")
				}
				if strings.Contains(res.Text, "truncated") {
					t.Fatalf("a complete render carries the truncation notice:\n%s", res.Text)
				}
			})
		}
	})
}

// TestRenderIsARenderResultWrapper pins that the existing entry point keeps its
// exact behaviour: callers that only want the string are unaffected by the new
// return type.
func TestRenderIsARenderResultWrapper(t *testing.T) {
	for _, diff := range []string{
		sampleUnified,
		"",
		bigDiff(maxRenderLines * 2),
		"not a diff at all",
	} {
		for _, width := range []int{40, 120} {
			opts := Options{Width: width}
			want := RenderResult(diff, opts).Text
			if got := Render(diff, opts); got != want {
				t.Fatalf("Render diverged from RenderResult.Text for width %d:\n--- Render ---\n%q\n--- RenderResult.Text ---\n%q",
					width, got, want)
			}
		}
	}
}

// TestRenderResultPreservesTheExistingTruncationNotice pins that the user-facing
// note is unchanged. The flag is for callers; the note is for readers, and both
// must survive.
func TestRenderResultPreservesTheExistingTruncationNotice(t *testing.T) {
	res := RenderResult(bigDiff(maxRenderLines*2), Options{Width: 80})
	if !strings.Contains(res.Text, "truncated") {
		t.Fatalf("the truncated render lost its notice:\n%s", res.Text)
	}
}

// TestRenderResultLinesCountsRenderedContentLines pins what Lines means, so a
// caller can compare it against the cap without having to know how the renderer
// counts.
func TestRenderResultLinesCountsRenderedContentLines(t *testing.T) {
	res := RenderResult("--- a/s.go\n+++ b/s.go\n@@ -1,1 +1,2 @@\n context\n+added\n", Options{Width: 80})
	if rendered := strings.Count(res.Text, "\n"); res.Lines > rendered+1 {
		t.Fatalf("Lines = %d exceeds the %d newlines in the rendered text", res.Lines, rendered)
	}
}

// plainTextLines builds `n` plain-text lines (deliberately not a diff, so
// parseUnifiedDiff fails and the fallback is the renderer under test). When
// terminate is true the last line ends with a newline, which is what decides
// whether the fallback sees `n` lines or `n+1`.
func plainTextLines(n int, terminate bool) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %d\n", i+1)
	}
	out := b.String()
	if !terminate {
		out = strings.TrimSuffix(out, "\n")
	}
	return out
}

// TestRenderResultFallbackTruncatedMatchesWhatItDropped is the regression the
// plain-text fallback's flag is judged by: the renderer CAPS its output, and a
// caller that receives a capped render labelled complete will present a prefix
// as the whole diff — the false statement this API exists to prevent.
//
// The bug had two faces, both from deriving the flag out of strings.Count over
// the NEWLINES while the fallback enumerates strings.Split ELEMENTS. For input
// whose last line has no trailing newline the two differ by exactly one, so a
// 501-line unterminated input dropped its last line and still reported
// Truncated = false; a naive Count+1 "fix" then reports truncation for exactly
// maxRenderLines terminated lines, all of which render. Both directions are
// pinned below, because a test that only covered the first would accept the
// second.
func TestRenderResultFallbackTruncatedMatchesWhatItDropped(t *testing.T) {
	// The inline notice is the reader-facing statement that content was
	// dropped. The flag must never contradict it, so every case asserts the two
	// agree as well as asserting the expected value.
	const notice = "truncated"

	t.Run("unterminated last line past the cap is truncated", func(t *testing.T) {
		// maxRenderLines+1 split elements, the last one unterminated: the
		// fallback fills its budget and drops that last line. Before the fix
		// this reported Truncated = false (maxRenderLines newlines > cap is
		// false) while printing the notice.
		res := RenderResult(plainTextLines(maxRenderLines+1, false), Options{Width: 80})
		if !res.Truncated {
			t.Fatalf("the fallback dropped a line (%d newlines, %d lines rendered) and reported Truncated = false",
				strings.Count(plainTextLines(maxRenderLines+1, false), "\n"), res.Lines)
		}
		if !strings.Contains(res.Text, notice) {
			t.Fatalf("Truncated = true with no notice in the render:\n%s", res.Text)
		}
		if res.Lines != maxRenderLines {
			t.Fatalf("Lines = %d, want the render's actual %d", res.Lines, maxRenderLines)
		}
		// The dropped line is gone from the text, which is what makes the flag
		// true rather than merely conservative.
		if strings.Contains(res.Text, fmt.Sprintf("line %d", maxRenderLines+1)) {
			t.Fatal("the flag says content was dropped, but the last line is in the render")
		}
	})

	t.Run("exactly at the cap with a trailing newline is not truncated", func(t *testing.T) {
		// maxRenderLines terminated lines render in full: all content is on
		// screen, so the flag must be false even though the fallback (which
		// counts a trailing empty element) prints its notice. This is the
		// direction a Count+1 "fix" would break.
		res := RenderResult(plainTextLines(maxRenderLines, true), Options{Width: 80})
		if res.Truncated {
			t.Fatalf("all %d lines rendered (Lines = %d) and Truncated = true", maxRenderLines, res.Lines)
		}
		if !strings.Contains(res.Text, fmt.Sprintf("line %d\n", maxRenderLines)) {
			t.Fatalf("the last line did not render:\n%s", res.Text)
		}
		if res.Lines != maxRenderLines {
			t.Fatalf("Lines = %d, want %d", res.Lines, maxRenderLines)
		}
	})

	t.Run("exactly at the cap without a trailing newline is not truncated", func(t *testing.T) {
		// maxRenderLines split elements, all rendered: the budget is exactly
		// spent. Nothing is dropped, so nothing may be reported.
		res := RenderResult(plainTextLines(maxRenderLines, false), Options{Width: 80})
		if res.Truncated {
			t.Fatalf("all %d lines rendered (Lines = %d) and Truncated = true", maxRenderLines, res.Lines)
		}
		if res.Lines != maxRenderLines {
			t.Fatalf("Lines = %d, want %d", res.Lines, maxRenderLines)
		}
		// Lines must count what the TEXT shows: a caller comparing it against
		// the cap has to be able to trust the number.
		if rendered := strings.Count(res.Text, "\n"); rendered != res.Lines {
			t.Fatalf("Lines = %d but the render has %d newlines", res.Lines, rendered)
		}
	})
}
