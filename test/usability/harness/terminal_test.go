package harness

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// writeAll writes s to m and checks the io.Writer contract: full length and
// a nil error, even once the model has latched a sticky error.
func writeAll(t *testing.T, m *terminalModel, s string) {
	t.Helper()
	n, err := m.Write([]byte(s))
	if err != nil {
		t.Fatalf("Write(%q) error = %v, want nil", s, err)
	}
	if n != len(s) {
		t.Fatalf("Write(%q) = %d bytes, want %d", s, n, len(s))
	}
}

// wantLines asserts a healthy snapshot whose Lines (and Content, derived
// from them) match want.
func wantLines(t *testing.T, snap Snapshot, want ...string) {
	t.Helper()
	if snap.Err != nil {
		t.Fatalf("unexpected sticky error: %v", snap.Err)
	}
	if len(snap.Lines) != len(want) {
		t.Fatalf("Lines = %q, want %d lines", snap.Lines, len(want))
	}
	for i := range want {
		if snap.Lines[i] != want[i] {
			t.Errorf("Lines[%d] = %q, want %q", i, snap.Lines[i], want[i])
		}
	}
	if got := string(snap.Content); got != strings.Join(want, "\n") {
		t.Errorf("Content = %q, want %q", got, strings.Join(want, "\n"))
	}
}

// assertSnapshot fails unless got matches want in every observable field.
func assertSnapshot(t *testing.T, where string, got, want Snapshot) {
	t.Helper()
	if got.Err != nil || want.Err != nil {
		t.Fatalf("%s: Err = %v, want nil (reference Err %v)", where, got.Err, want.Err)
	}
	if !slices.Equal(got.Lines, want.Lines) {
		t.Fatalf("%s: Lines = %q, want %q", where, got.Lines, want.Lines)
	}
	if !bytes.Equal(got.Content, want.Content) {
		t.Fatalf("%s: Content = %q, want %q", where, got.Content, want.Content)
	}
	if got.Width != want.Width || got.Height != want.Height {
		t.Fatalf("%s: size = %dx%d, want %dx%d", where, got.Width, got.Height, want.Width, want.Height)
	}
}

func TestTerminalCursorAndErase(t *testing.T) {
	// The plan's anchor case: CR, an overwrite and EL 0 leave "X" and no
	// stale "bc".
	m := newTerminalModel(10, 3)
	writeAll(t, m, "abc\rX\x1b[K")
	wantLines(t, m.Snapshot(), "X", "", "")

	// Absolute addressing: CUP (H), HVP (f), and omitted or zero parameters
	// counting as one.
	m = newTerminalModel(10, 4)
	writeAll(t, m, "\x1b[3;2Hhi")
	wantLines(t, m.Snapshot(), "", "", " hi", "")
	writeAll(t, m, "\x1b[2;5fZ")
	wantLines(t, m.Snapshot(), "", "    Z", " hi", "")
	writeAll(t, m, "\x1b[HT")
	wantLines(t, m.Snapshot(), "T", "    Z", " hi", "")
	writeAll(t, m, "\x1b[0;0HY")
	wantLines(t, m.Snapshot(), "Y", "    Z", " hi", "")

	m = newTerminalModel(10, 2)
	writeAll(t, m, "\x1b[0CX") // a zero movement count moves one column
	wantLines(t, m.Snapshot(), " X", "")

	// Relative addressing: CUU/CUD/CUF/CUB, CHA, VPA and clamping to the
	// viewport.
	m = newTerminalModel(10, 6)
	writeAll(t, m, "\x1b[4;5Hu")
	writeAll(t, m, "\x1b[2AU")
	writeAll(t, m, "\x1b[3BD")
	writeAll(t, m, "\x1b[2CR")
	writeAll(t, m, "\x1b[4DL")
	writeAll(t, m, "\x1b[GA")
	writeAll(t, m, "\x1b[2dv")
	wantLines(t, m.Snapshot(), "", " v   U", "", "    u", "A    LD  R", "")
	writeAll(t, m, "\x1b[99;99H!") // clamps to the bottom-right corner
	writeAll(t, m, "\x1b[99A\x1b[99D@")
	wantLines(t, m.Snapshot(), "@", " v   U", "", "    u", "A    LD  R", "         !")

	// ED 0: cursor to end of screen, inclusive.
	m = newTerminalModel(10, 3)
	writeAll(t, m, "aaa\r\nbbb\r\nccc\x1b[1;2H\x1b[0J")
	wantLines(t, m.Snapshot(), "a", "", "")

	// ED 1: start of screen to cursor, inclusive.
	m = newTerminalModel(10, 3)
	writeAll(t, m, "aaa\r\nbbbde\r\nccc\x1b[2;4H\x1b[1J")
	wantLines(t, m.Snapshot(), "", "    e", "ccc")

	// ED 2: everything, without moving the cursor.
	m = newTerminalModel(10, 3)
	writeAll(t, m, "aaa\r\nbbb\r\nccc\x1b[2JZ")
	wantLines(t, m.Snapshot(), "", "", "   Z")

	// EL 1: start of line to cursor, inclusive.
	m = newTerminalModel(10, 2)
	writeAll(t, m, "abcdef\x1b[1;4H\x1b[1K")
	wantLines(t, m.Snapshot(), "    ef", "")

	// EL 2: the whole line, without moving the cursor.
	m = newTerminalModel(10, 2)
	writeAll(t, m, "abcdef\x1b[2KZ")
	wantLines(t, m.Snapshot(), "      Z", "")

	// LF at the bottom scrolls the viewport up one row.
	m = newTerminalModel(6, 3)
	writeAll(t, m, "1\r\n2\r\n3\r\n4")
	wantLines(t, m.Snapshot(), "2", "3", "4")

	// Save/restore via CSI s/u.
	m = newTerminalModel(10, 4)
	writeAll(t, m, "abc\x1b[s\r\nxyz\x1b[u!")
	wantLines(t, m.Snapshot(), "abc!", "xyz", "", "")

	// Save/restore via ESC 7/8.
	m = newTerminalModel(10, 4)
	writeAll(t, m, "abc\x1b7\r\nxyz\x1b8!")
	wantLines(t, m.Snapshot(), "abc!", "xyz", "", "")

	// Backspace and tab (with BEL ignored and the right edge holding the
	// cursor).
	m = newTerminalModel(10, 2)
	writeAll(t, m, "abcdef\bX")
	wantLines(t, m.Snapshot(), "abcdeX", "")
	writeAll(t, m, "\r\a\tQ")
	wantLines(t, m.Snapshot(), "abcdeX  Q", "")
	writeAll(t, m, "\x1b[1;10H\tW")
	wantLines(t, m.Snapshot(), "abcdeX  QW", "")
}

func TestTerminalIncrementalWrites(t *testing.T) {
	const (
		w = 24
		h = 6
	)
	// One stream with CSI (cursor addressing, erase, SGR, a private mode),
	// OSC (window title and the colour queries the real TUI issues at startup)
	// and multibyte UTF-8: two-, three- and four-byte runes plus a combining
	// mark.
	stream := "h\u00e9llo \x1b]2;demo\x07\u4e16 \x1b[1;31mbold\x1b[0m\r\n" +
		"second \U0001F600 line\x1b[4;3Htail e\u0301nd\x1b[4;8H\x1b[K\x1b[?25l" +
		"\x1b]11;?\x07"
	golden := []string{
		"h\u00e9llo \u4e16 bold",
		"second \U0001F600 line",
		"",
		"  tail",
		"",
		"",
	}

	ref := newTerminalModel(w, h)
	writeAll(t, ref, stream)
	wantLines(t, ref.Snapshot(), golden...)

	// Every two-chunk split of the stream must decode identically to the
	// single write.
	for k := 1; k < len(stream); k++ {
		m := newTerminalModel(w, h)
		writeAll(t, m, stream[:k])
		writeAll(t, m, stream[k:])
		assertSnapshot(t, fmt.Sprintf("split at byte %d", k), m.Snapshot(), ref.Snapshot())
	}

	// So must a feed of one byte per Write call.
	m := newTerminalModel(w, h)
	for i := 0; i < len(stream); i++ {
		writeAll(t, m, stream[i:i+1])
	}
	assertSnapshot(t, "single bytes", m.Snapshot(), ref.Snapshot())
}

func TestTerminalAutowrap(t *testing.T) {
	// DECAWM (?7) off is what the real TUI sets: writing in the last column
	// overwrites that cell instead of moving to the next line.
	m := newTerminalModel(8, 3)
	writeAll(t, m, "\x1b[?7l123456789")
	wantLines(t, m.Snapshot(), "12345679", "", "")

	// Re-enabling autowrap restores the deferred wrap: the next grapheme moves
	// to the following row.
	writeAll(t, m, "\x1b[?7h0")
	wantLines(t, m.Snapshot(), "12345670", "", "")
	writeAll(t, m, "A")
	wantLines(t, m.Snapshot(), "12345670", "A", "")

	// With autowrap off, a wide grapheme that cannot fit is not split.
	m = newTerminalModel(8, 3)
	writeAll(t, m, "\x1b[?7l1234567\u4e16")
	wantLines(t, m.Snapshot(), "1234567", "", "")
}

func TestTerminalColorQueryOSC(t *testing.T) {
	// The real TUI asks the terminal for its colours at startup
	// ("\x1b]11;?\a" among others). Those queries never change visible cells, so
	// the model must ignore them instead of failing on a real screen.
	m := newTerminalModel(20, 3)
	writeAll(t, m, "ok")
	writeAll(t, m, "\x1b]10;?\x07\x1b]11;?\x07\x1b]12;?\x07")
	writeAll(t, m, "\x1b]11;rgb:0000/0000/0000\x07")
	wantLines(t, m.Snapshot(), "ok", "", "")
}

func TestTerminalCellWidths(t *testing.T) {
	// ASCII occupies one cell per rune.
	m := newTerminalModel(8, 4)
	writeAll(t, m, "ab")
	writeAll(t, m, "c")
	writeAll(t, m, "d")
	wantLines(t, m.Snapshot(), "abcd", "", "", "")

	// A wide CJK glyph contributes one grapheme plus an occupied
	// continuation cell, not an extra character.
	m = newTerminalModel(8, 4)
	writeAll(t, m, "a\u4e16b")
	wantLines(t, m.Snapshot(), "a\u4e16b", "", "", "")
	writeAll(t, m, "c") // the cursor sits after the continuation cell
	wantLines(t, m.Snapshot(), "a\u4e16bc", "", "", "")

	// A combining mark extends the preceding grapheme rather than taking a
	// cell: "X" lands in the next column.
	m = newTerminalModel(8, 4)
	writeAll(t, m, "e\u0301X")
	wantLines(t, m.Snapshot(), "e\u0301X", "", "", "")
	m = newTerminalModel(8, 4) // also when the mark arrives in a later write
	writeAll(t, m, "e")
	writeAll(t, m, "\u0301X")
	wantLines(t, m.Snapshot(), "e\u0301X", "", "", "")

	// Overwriting the continuation half clears the entire prior grapheme.
	m = newTerminalModel(8, 4)
	writeAll(t, m, "a\u4e16b\x1b[1;3HX")
	wantLines(t, m.Snapshot(), "a Xb", "", "", "")

	// Overwriting the head half clears the entire prior grapheme too.
	m = newTerminalModel(8, 4)
	writeAll(t, m, "a\u4e16b\x1b[1;2HY")
	wantLines(t, m.Snapshot(), "aY b", "", "", "")

	// A two-cell grapheme at the right edge wraps before drawing.
	m = newTerminalModel(8, 4)
	writeAll(t, m, "1234567\u4e16")
	wantLines(t, m.Snapshot(), "1234567", "\u4e16", "", "")

	// A one-cell write at the right edge defers the wrap to the next rune.
	m = newTerminalModel(8, 4)
	writeAll(t, m, "123456789")
	wantLines(t, m.Snapshot(), "12345678", "9", "", "")
}

func TestTerminalUnknownScreenControl(t *testing.T) {
	m := newTerminalModel(10, 3)
	const p = "ok\x1b[2;5r" // DECSTBM: an unsupported screen-changing command
	n, err := m.Write([]byte(p))
	if err != nil {
		t.Fatalf("Write error = %v, want nil", err)
	}
	if n != len(p) {
		t.Fatalf("Write = %d bytes, want %d", n, len(p))
	}

	snap := m.Snapshot()
	if snap.Err == nil {
		t.Fatalf("Snapshot.Err = nil, want a sticky unsupported-sequence error")
	}
	if !strings.Contains(snap.Err.Error(), `\x1b[2;5r`) {
		t.Fatalf("error should report the offending sequence bytes: %v", snap.Err)
	}
	// The snapshot still carries the cells decoded before the failure.
	if snap.Lines[0] != "ok" {
		t.Fatalf("Lines[0] = %q, want %q", snap.Lines[0], "ok")
	}

	// A cell-changing DEC private mode (DECCOLM clears the screen) must fail
	// as well, while the screen-neutral modes Bubble Tea sets are ignored.
	priv := newTerminalModel(10, 3)
	writeAll(t, priv, "ok\x1b[?3h")
	if pErr := priv.Snapshot().Err; pErr == nil || !strings.Contains(pErr.Error(), "DEC private mode 3") {
		t.Fatalf("private mode 3: Err = %v, want a sticky unsupported-mode error", pErr)
	}
	neutral := newTerminalModel(10, 3)
	writeAll(t, neutral, "ok\x1b[?25l\x1b[?2004h\x1b[?1000h")
	wantLines(t, neutral.Snapshot(), "ok", "", "")

	// The error is sticky and the model stays frozen for later writes.
	n2, err2 := m.Write([]byte("more"))
	if err2 != nil {
		t.Fatalf("Write after sticky error = %v, want nil", err2)
	}
	if n2 != len("more") {
		t.Fatalf("Write after sticky error = %d bytes, want %d", n2, len("more"))
	}
	snap2 := m.Snapshot()
	if snap2.Err == nil {
		t.Fatalf("sticky error cleared by a later write")
	}
	if snap2.Lines[0] != "ok" {
		t.Fatalf("Lines[0] = %q, want the model frozen at %q", snap2.Lines[0], "ok")
	}
}

func TestTerminalAlternateScreen(t *testing.T) {
	// Primary -> alternate -> restored primary, with each screen keeping its
	// own buffer and cursor.
	m := newTerminalModel(10, 3)
	writeAll(t, m, "one\r\ntwo")
	wantLines(t, m.Snapshot(), "one", "two", "")

	writeAll(t, m, "\x1b[?1049h") // a fresh 1049 entry starts empty
	wantLines(t, m.Snapshot(), "", "", "")
	writeAll(t, m, "alt")
	wantLines(t, m.Snapshot(), "alt", "", "")

	writeAll(t, m, "\x1b[?1049l")
	wantLines(t, m.Snapshot(), "one", "two", "")
	writeAll(t, m, "!") // the primary cursor came back with its buffer
	wantLines(t, m.Snapshot(), "one", "two!", "")

	// A repeated enter or exit must not clear the visible buffer.
	writeAll(t, m, "\x1b[?1049h")
	writeAll(t, m, "x")
	writeAll(t, m, "\x1b[?1049h")
	wantLines(t, m.Snapshot(), "x", "", "")
	writeAll(t, m, "\x1b[?1049l\x1b[?1049l")
	wantLines(t, m.Snapshot(), "one", "two!", "")

	// 47 and 1047 switch buffers without discarding the primary, and a 47
	// round trip keeps the alternate contents because 47 does not clear.
	m = newTerminalModel(10, 2)
	writeAll(t, m, "main")
	writeAll(t, m, "\x1b[?47h")
	writeAll(t, m, "alt")
	wantLines(t, m.Snapshot(), "alt", "")
	writeAll(t, m, "\x1b[?47l")
	wantLines(t, m.Snapshot(), "main", "")
	writeAll(t, m, "\x1b[?47h")
	wantLines(t, m.Snapshot(), "alt", "")
	writeAll(t, m, "\x1b[?1047l")
	wantLines(t, m.Snapshot(), "main", "")
	// 1049 clears an alternate buffer a previous 47 entry left behind.
	writeAll(t, m, "\x1b[?1049h")
	wantLines(t, m.Snapshot(), "", "")
}

func TestTerminalResize(t *testing.T) {
	// Grow: retained cells keep their position, added cells are blank.
	m := newTerminalModel(4, 2)
	writeAll(t, m, "abcd\r\nwxyz")
	if err := m.Resize(6, 3); err != nil {
		t.Fatalf("Resize(6, 3) error = %v, want nil", err)
	}
	snap := m.Snapshot()
	if snap.Width != 6 || snap.Height != 3 {
		t.Fatalf("after Resize(6, 3) size = %dx%d, want 6x3", snap.Width, snap.Height)
	}
	wantLines(t, snap, "abcd", "wxyz", "")

	// Shrink: the upper-left intersection survives and rows never reflow.
	if err := m.Resize(3, 2); err != nil {
		t.Fatalf("Resize(3, 2) error = %v, want nil", err)
	}
	wantLines(t, m.Snapshot(), "abc", "wxy")

	// Nonpositive sizes are rejected and leave the model untouched.
	for _, size := range [][2]int{{0, 2}, {3, 0}, {-1, -1}} {
		if err := m.Resize(size[0], size[1]); err == nil {
			t.Fatalf("Resize(%d, %d) error = nil, want a rejection", size[0], size[1])
		}
	}
	wantLines(t, m.Snapshot(), "abc", "wxy")

	// A wide grapheme whose continuation cell is clipped is cleared rather
	// than left as half a glyph; one that still fits survives whole.
	m = newTerminalModel(4, 1)
	writeAll(t, m, "a\u4e16")
	wantLines(t, m.Snapshot(), "a\u4e16")
	if err := m.Resize(3, 1); err != nil {
		t.Fatalf("Resize(3, 1) error = %v, want nil", err)
	}
	wantLines(t, m.Snapshot(), "a\u4e16")
	if err := m.Resize(2, 1); err != nil {
		t.Fatalf("Resize(2, 1) error = %v, want nil", err)
	}
	wantLines(t, m.Snapshot(), "a")
	writeAll(t, m, "Z") // drawing continues from the clamped cursor
	wantLines(t, m.Snapshot(), "aZ")

	// Cursor positions clamp to the shrunken viewport.
	m = newTerminalModel(6, 4)
	writeAll(t, m, "\x1b[3;5HX") // row 2, column 4
	wantLines(t, m.Snapshot(), "", "", "    X", "")
	if err := m.Resize(3, 2); err != nil {
		t.Fatalf("Resize(3, 2) error = %v, want nil", err)
	}
	writeAll(t, m, "Y")
	wantLines(t, m.Snapshot(), "", "  Y")

	// Resizing while the alternate screen is active clips both buffers, so
	// the restored primary already has the new dimensions.
	m = newTerminalModel(8, 3)
	writeAll(t, m, "primary")
	writeAll(t, m, "\x1b[?1049h")
	writeAll(t, m, "alt")
	if err := m.Resize(4, 2); err != nil {
		t.Fatalf("Resize(4, 2) while alternate is active: error = %v, want nil", err)
	}
	wantLines(t, m.Snapshot(), "alt", "")
	writeAll(t, m, "\x1b[?1049l")
	wantLines(t, m.Snapshot(), "prim", "")
	// The primary cursor came back clamped to the shrunken viewport, so it
	// sits on the last column and overwrites it.
	writeAll(t, m, "!")
	wantLines(t, m.Snapshot(), "pri!", "")
}

func TestTerminalSnapshotIsIndependent(t *testing.T) {
	m := newTerminalModel(6, 2)
	writeAll(t, m, "ab\r\ncd")

	before := m.Snapshot()
	wantLines(t, before, "ab", "cd")

	// A later write must never reach an already-returned snapshot.
	writeAll(t, m, "\x1b[HXY")
	wantLines(t, before, "ab", "cd")
	wantLines(t, m.Snapshot(), "XY", "cd")

	// Caller mutation of one snapshot cannot reach a later one or the model.
	after := m.Snapshot()
	for i := range before.Lines {
		before.Lines[i] = strings.Repeat("z", 3)
	}
	for i := range before.Content {
		before.Content[i] = 'Q'
	}
	fresh := m.Snapshot()
	if !slices.Equal(fresh.Lines, after.Lines) {
		t.Fatalf("Lines = %q, want %q after mutating an earlier snapshot", fresh.Lines, after.Lines)
	}
	if !bytes.Equal(fresh.Content, after.Content) {
		t.Fatalf("Content = %q, want %q after mutating an earlier snapshot", fresh.Content, after.Content)
	}
	wantLines(t, fresh, "XY", "cd")

	// Lines and Content of one snapshot do not share backing storage either.
	for i := range after.Content {
		after.Content[i] = 'Q'
	}
	if after.Lines[0] != "XY" {
		t.Fatalf("Lines[0] = %q, want %q after mutating Content", after.Lines[0], "XY")
	}
	wantLines(t, m.Snapshot(), "XY", "cd")
}

func TestTerminalOversizedControlIsBounded(t *testing.T) {
	m := newTerminalModel(20, 4)
	// An unterminated OSC whose payload is far longer than the parser's
	// 64 KiB data buffer: the model must fail instead of growing memory.
	oversized := "\x1b]0;" + strings.Repeat("x", 96*1024)
	n, err := m.Write([]byte(oversized))
	if err != nil {
		t.Fatalf("Write error = %v, want nil", err)
	}
	if n != len(oversized) {
		t.Fatalf("Write = %d bytes, want %d", n, len(oversized))
	}

	snap := m.Snapshot()
	if snap.Err == nil {
		t.Fatalf("Snapshot.Err = nil, want a sticky payload-bound error")
	}
	if !strings.Contains(snap.Err.Error(), "65536") {
		t.Fatalf("error should name the 64 KiB bound: %v", snap.Err)
	}
	// The payload is string data: no cells were written.
	if snap.Lines[0] != "" {
		t.Fatalf("Lines[0] = %q, want %q", snap.Lines[0], "")
	}

	// The error stays sticky for later writes.
	if _, err := m.Write([]byte("z")); err != nil {
		t.Fatalf("Write after sticky error = %v, want nil", err)
	}
	if m.Snapshot().Err == nil {
		t.Fatalf("sticky error cleared by a later write")
	}
}
