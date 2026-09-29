package diffview

import (
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
		if res.Lines == 0 {
			t.Fatal("Lines = 0 for a non-empty diff")
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

// TestRenderIsARenderResultWrapper pins that the existing entry point keeps its
// exact behaviour: callers that only want the string are unaffected by the new
// return type. This is the compatibility guarantee the plan asks for.
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
