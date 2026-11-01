package usability

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"marshal/test/usability/harness"
	"marshal/test/usability/screen"
)

const (
	// pasteResizeMarker is the bracketed-paste payload this journey types into
	// the real composer. It is deliberately short and single-line: the paste
	// handler (internal/app/tui/paste.go) condenses a paste into a
	// "[pasted #N · M lines]" chip only at >= 8 lines or >= 1500 chars, so this
	// text lands in the textarea as ordinary runes and the composer shows it
	// inline, character for character. "α" is a non-ASCII rune, which is what
	// proves the draft is the pasted text and not an ASCII-only echo of it.
	pasteResizeMarker = "paste-marker α"

	// pasteResizeMarkerRunes is the marker's length in runes: "α" is one rune,
	// not two bytes, and there is no chip-aware deletion, so cancelling the
	// draft takes exactly this many Backspace presses.
	pasteResizeMarkerRunes = 14

	// composerPrompt is the idle composer's prompt glyph. It is drawn once, on
	// the input row, which is what makes that row identifiable in the viewport.
	composerPrompt = "❯"

	// composerPlaceholder is what the textarea shows instead of a draft once the
	// draft is empty. It is the user-visible representation of "the composer is
	// clear again", so cancellation is asserted against it rather than against
	// the row simply going blank.
	composerPlaceholder = "Ask Marshal..."
)

// TestScriptedPasteResizeDraft drives the real composer at 80x24: bracketed
// paste must land as an inline draft, the draft must survive a resize to
// 100x30 and back, and Backspace must cancel it rune by rune without ever
// submitting anything.
//
// The viewport is the oracle throughout — no Enter is pressed, and the raw PTY
// history is deliberately not consulted, so nothing here depends on bytes
// Marshal happened to emit. The test-owned provider is configured with the
// passive always-failing fixture: any request would mean an unconfirmed draft
// reached a model, so every request is a failure.
func TestScriptedPasteResizeDraft(t *testing.T) {
	bin := requireBinary(t)
	env := isolatedHomeEnv(t)

	sess, err := harness.New(harness.Config{
		BinaryPath: bin,
		Width:      80,
		Height:     24,
		WorkDir:    t.TempDir(),
		Env:        env.Env,
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	defer sess.Close()

	// Per-test diagnostics: dump whatever the viewport ended up showing, so a
	// failed wait reports the frame itself and not only the assertion text.
	t.Cleanup(func() {
		scr, parseErr := screen.Parse(sess.Snapshot())
		if parseErr != nil {
			t.Logf("final viewport: %v", parseErr)
			return
		}
		t.Logf("final viewport %dx%d:\n%s", scr.Width, scr.Height, strings.Join(scr.Lines, "\n"))
	})

	waitForViewport(t, sess, 60*time.Second, "idle composer in an 80x24 viewport", func(scr screen.Screen) bool {
		return scr.Width == 80 && scr.Height == 24 && strings.Contains(scr.Content, composerPrompt)
	})

	// Step 1: the paste lands in the composer. Paste frames the text as
	// bracketed paste and never appends Enter, so this is a draft and nothing
	// more.
	if err := sess.Paste(pasteResizeMarker); err != nil {
		t.Fatalf("Paste(%q): %v", pasteResizeMarker, err)
	}
	waitForViewport(t, sess, 30*time.Second, "pasted marker on the composer row", func(scr screen.Screen) bool {
		return strings.Contains(composerRow(scr), pasteResizeMarker)
	})
	assertRowsFitViewport(t, sess, "after pasting the marker")

	// Step 2: grow and shrink the window under the draft. Both waits demand
	// evidence that the frame was repainted for the new size, not just that the
	// harness viewport model reports it.
	before, err := screen.Parse(sess.Snapshot())
	if err != nil {
		t.Fatalf("snapshot before resizing: %v", err)
	}
	if err := sess.Resize(100, 30); err != nil {
		t.Fatalf("Resize(100, 30): %v", err)
	}
	waitForDraftAfterResize(t, sess, 100, 30, func(scr screen.Screen) bool {
		// Growing: the retained cells stop at the old width, so a row wider than
		// 80 is a row the app drew for the 100-column frame. A frame that fits
		// its content inside 80 columns would never produce one, so any visible
		// difference from the pre-resize frame is accepted as proof of a
		// repaint too: this asserts that a fresh frame was drawn, without
		// pinning how a wider frame is laid out.
		return widestRow(scr) > 80 || repaintedSince(scr, before)
	})
	assertRowsFitViewport(t, sess, "after resizing to 100x30")

	if err := sess.Resize(80, 24); err != nil {
		t.Fatalf("Resize(80, 24): %v", err)
	}
	waitForDraftAfterResize(t, sess, 80, 24, nil)
	assertRowsFitViewport(t, sess, "after resizing back to 80x24")

	// Step 3: cancel the draft with one Backspace per rune, then prove the
	// composer is empty again. The marker stops being a substring of the row
	// after the first press (its last rune goes first), so the idle placeholder
	// appearing is the assertion that every rune was deleted.
	for i := 0; i < pasteResizeMarkerRunes; i++ {
		if err := sess.SendKey("backspace"); err != nil {
			t.Fatalf("SendKey(backspace) %d/%d: %v", i+1, pasteResizeMarkerRunes, err)
		}
	}
	waitForViewport(t, sess, 30*time.Second, "empty composer after cancelling the draft", func(scr screen.Screen) bool {
		row := composerRow(scr)
		if row == "" {
			return false
		}
		return !strings.Contains(row, pasteResizeMarker) && strings.Contains(row, composerPlaceholder)
	})
	assertRowsFitViewport(t, sess, "after cancelling the draft")

	// Nothing was submitted at any point, so nothing may have reached the model.
	if n := env.Requests(); n != 0 {
		t.Fatalf("fixture provider received %d requests while only the composer was used, want 0: no draft was submitted", n)
	}

	// Step 4: paste again after the cancellation. The marker must come back, and
	// still as a single inline draft with no request behind it.
	if err := sess.Paste(pasteResizeMarker); err != nil {
		t.Fatalf("Paste(%q) after cancelling: %v", pasteResizeMarker, err)
	}
	waitForViewport(t, sess, 30*time.Second, "marker back on the composer row after the second paste", func(scr screen.Screen) bool {
		return strings.Contains(composerRow(scr), pasteResizeMarker)
	})
	assertRowsFitViewport(t, sess, "after pasting the marker again")
	if got := strings.Count(snapshotScreen(t, sess).Content, pasteResizeMarker); got != 1 {
		t.Fatalf("the marker appears %d times on screen after the second paste, want exactly 1 inline draft", got)
	}

	if n := env.Requests(); n != 0 {
		t.Fatalf("fixture provider received %d requests after the second paste, want 0: no draft was submitted", n)
	}
}

// waitForDraftAfterResize waits for the draft on a frame the app drew at width x
// height. Resize updates the harness viewport model in the same call, so the
// reported dimensions switch immediately; the app repaints asynchronously and
// the retained cells are only clipped, never reflowed. The wait therefore also
// requires the draft to be back on the composer row, which after a shrink only
// a fresh frame can provide: the 30-row frame kept its composer on rows the
// 24-row viewport no longer has.
//
// painted is the extra evidence that the repaint happened for callers where the
// composer row alone is not enough (growing keeps the composer inside the old
// viewport); nil means the composer row is all the evidence available.
func waitForDraftAfterResize(t *testing.T, sess *harness.Session, width, height int, painted func(screen.Screen) bool) {
	t.Helper()
	what := fmt.Sprintf("repainted %dx%d frame with the draft", width, height)
	waitForViewport(t, sess, 30*time.Second, what, func(scr screen.Screen) bool {
		if scr.Width != width || scr.Height != height {
			return false
		}
		if !strings.Contains(composerRow(scr), pasteResizeMarker) {
			return false
		}
		return painted == nil || painted(scr)
	})
}

// composerRow returns the row the composer is drawn on: the only row carrying
// the prompt glyph. It returns "" when no composer row is visible.
func composerRow(scr screen.Screen) string {
	for _, line := range scr.Lines {
		if strings.Contains(line, composerPrompt) {
			return line
		}
	}
	return ""
}

// widestRow returns the display-cell width of the widest visible row.
func widestRow(scr screen.Screen) int {
	widest := 0
	for _, line := range scr.Lines {
		widest = max(widest, lipgloss.Width(line))
	}
	return widest
}

// repaintedSince reports whether scr differs from before in the rows the two
// frames share. Resize only clips the retained cells, so a frame the app has not
// repainted since the resize is identical to the pre-resize frame in those rows;
// any difference is a fresh paint. Comparing rows rather than widths keeps the
// check independent of how the app chooses to lay out a wider frame.
func repaintedSince(scr, before screen.Screen) bool {
	n := min(len(scr.Lines), len(before.Lines))
	for i := 0; i < n; i++ {
		if scr.Lines[i] != before.Lines[i] {
			return true
		}
	}
	return false
}

// assertRowsFitViewport holds every visible row to the viewport's width in
// display cells. A row wider than the viewport is what makes a real terminal
// wrap and corrupt the frame below it, so this is checked after every step; the
// failure names the row and both widths.
func assertRowsFitViewport(t *testing.T, sess *harness.Session, when string) {
	t.Helper()
	scr, err := screen.Parse(sess.Snapshot())
	if err != nil {
		t.Fatalf("%s: terminal model error: %v", when, err)
	}
	for i, line := range scr.Lines {
		if w := lipgloss.Width(line); w > scr.Width {
			t.Fatalf("%s: row %d is %d display cells wide, viewport is %d: %q", when, i, w, scr.Width, line)
		}
	}
}
