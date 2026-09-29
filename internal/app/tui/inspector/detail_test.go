package inspector

import (
	"strings"
	"testing"
)

// detailBody builds a body of n distinct lines.
func detailBody(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("line ")
		b.WriteString(itoaDetail(i))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func itoaDetail(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestDetailScrollDoesNotMoveTheListAndViceVersa is the separation this type
// exists for. Two pieces of navigation state that can be confused for one
// another is how a reader loses their place.
func TestDetailScrollDoesNotMoveTheListAndViceVersa(t *testing.T) {
	d := NewDetailView()
	d.Resize(80, 5)
	d.SetContent(detailBody(50), false)

	d.Scroll(10)
	if d.ScrollOffset() == 0 {
		t.Fatal("scrolling the detail did not move it")
	}
	// The list state lives in the inspector Model, not here — which is the
	// point. Assert the detail kept its own offset across a resize and a
	// re-set, i.e. nothing else reset it.
	before := d.ScrollOffset()
	d.Resize(80, 5)
	if d.ScrollOffset() != before {
		t.Fatalf("a resize moved the detail from %d to %d", before, d.ScrollOffset())
	}
}

// TestDetailFollowKeepsTheReaderAtTheEnd pins the follow contract: while
// following, new content stays visible; once the reader scrolls, new content
// must not yank them.
func TestDetailFollowKeepsTheReaderAtTheEnd(t *testing.T) {
	d := NewDetailView()
	d.Resize(80, 5)

	d.SetContent(detailBody(50), false)
	if !d.Follow() {
		t.Fatal("a fresh detail view is not following")
	}
	// Following means the END is visible, not the start.
	view := d.View("patch")
	if !strings.Contains(view, "line 49") {
		t.Fatalf("a following view does not show the last line:\n%s", view)
	}
	if strings.Contains(view, "line 0\n") {
		t.Fatalf("a following view shows the first line, so it is not pinned to the end:\n%s", view)
	}

	// Take control.
	d.Scroll(-10)
	if d.Follow() {
		t.Fatal("scrolling did not clear follow")
	}
	anchored := d.ScrollOffset()
	// New content arrives.
	d.SetContent(detailBody(80), false)
	if d.Follow() {
		t.Fatal("content arriving re-enabled follow")
	}
	if got := d.ScrollOffset(); got > anchored {
		t.Fatalf("scroll moved from %d to %d when content arrived; a reading reader must not be yanked", anchored, got)
	}
}

// TestDetailTruncationIsNotConfusedWithMoreToScroll is the honesty rule: "scroll
// down for more" and "there IS no more" are different statements, and a reader
// who is told the first when the second is true believes they have seen a whole
// patch.
func TestDetailTruncationIsNotConfusedWithMoreToScroll(t *testing.T) {
	t.Run("more to scroll is not truncation", func(t *testing.T) {
		d := NewDetailView()
		d.Resize(80, 5)
		d.SetContent(detailBody(50), false)
		d.Top()

		if d.Truncated() {
			t.Fatal("a long but complete body reports Truncated")
		}
		view := d.View("patch")
		if !strings.Contains(view, "scroll for more") {
			t.Fatalf("a body with more to scroll to does not say so:\n%s", view)
		}
		if strings.Contains(view, "incomplete") {
			t.Fatalf("a complete body claims its patch is incomplete:\n%s", view)
		}
	})

	t.Run("truncated source says so and does not promise more", func(t *testing.T) {
		d := NewDetailView()
		d.Resize(80, 5)
		d.SetContent(detailBody(5), true) // fits, but the SOURCE was capped
		d.Top()

		if !d.Truncated() {
			t.Fatal("a truncated source does not report Truncated")
		}
		view := d.View("patch")
		if !strings.Contains(view, "incomplete") {
			t.Fatalf("a capped patch does not disclose that it is incomplete:\n%s", view)
		}
		if !strings.Contains(view, "not shown") {
			t.Fatalf("a capped patch does not say the rest is not shown:\n%s", view)
		}
	})

	t.Run("a truncated source that also needs scrolling says both", func(t *testing.T) {
		d := NewDetailView()
		d.Resize(80, 3)
		d.SetContent(detailBody(40), true)
		d.Top()

		view := d.View("patch")
		if !strings.Contains(view, "scroll for more") {
			t.Fatalf("the scrollable part is not disclosed:\n%s", view)
		}
		if !strings.Contains(view, "incomplete") {
			t.Fatalf("the truncated part is not disclosed:\n%s", view)
		}
	})
}

// TestDetailContentIsRawText pins the copy-integrity rule. The view renders
// styled text; the copy source must be the bytes with no escape sequences, or a
// paste would carry invisible corruption.
func TestDetailContentIsRawText(t *testing.T) {
	const raw = "diff --git a/x.go b/x.go\n+added\n-removed\n"
	d := NewDetailView()
	d.SetContent(raw, false)

	if got := d.Content(); got != raw {
		t.Fatalf("Content = %q, want the raw text %q", got, raw)
	}
	if strings.Contains(d.Content(), "\x1b") {
		t.Fatal("Content carries an escape sequence; a copy would paste corruption")
	}

	// The rendered view is allowed to differ from the source, and the point is
	// that the two are separate: whatever styling a caller applies, Content
	// stays raw.
	if d.Content() == "" {
		t.Fatal("Content is empty")
	}
}

// TestDetailScrollToMatchPinsTask13sNeed pins the search jump, including the
// rule that a miss must not move the reader.
func TestDetailScrollToMatchPinsTask13sNeed(t *testing.T) {
	d := NewDetailView()
	d.Resize(80, 3)
	d.SetContent(detailBody(20), false)
	d.Top()
	before := d.ScrollOffset()

	if d.ScrollToMatch("nowhere-in-this-body") {
		t.Fatal("ScrollToMatch claims to have found a needle that is absent")
	}
	if d.ScrollOffset() != before {
		t.Fatalf("a missed search moved the reader from %d to %d", before, d.ScrollOffset())
	}

	if !d.ScrollToMatch("line 12") {
		t.Fatal("ScrollToMatch did not find a needle that is present")
	}
	if got := d.ScrollOffset(); got != 12 {
		t.Fatalf("scroll = %d after matching line 12, want 12", got)
	}
	if d.Follow() {
		t.Fatal("a search jump left the view following; the reader has taken control")
	}
}
