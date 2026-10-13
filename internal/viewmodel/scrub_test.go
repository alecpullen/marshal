package viewmodel

import (
	"strings"
	"testing"
)

// A terminal acts on these bytes; the text they arrive inside is
// model-authored. OSC 52 writes the clipboard, OSC 0 rewrites the window
// title, OSC 8 forges a hyperlink, CSI 2J clears the screen.
func TestScrubTextRemovesTerminalControlSequences(t *testing.T) {
	cases := []struct{ name, in string }{
		{"clipboard write", "copy \x1b]52;c;aGVsbG8=\x07 now"},
		{"window title", "\x1b]0;pwned\x07"},
		{"screen clear", "before\x1b[2Jafter"},
		{"cursor move", "\x1b[1;1H"},
		{"DEL", "ab\x7fcd"},
		{"C1 CSI", "ab\u009b2Jcd"},
		{"bare ESC", "ab\x1bcd"},
	}
	for _, c := range cases {
		got := ScrubText(c.in)
		for _, bad := range []string{"\x1b", "\x07", "\x7f", "\u009b"} {
			if strings.Contains(got, bad) {
				t.Fatalf("%s: ScrubText(%q) = %q, still contains %q", c.name, c.in, got, bad)
			}
		}
	}
}

// Scrubbing is not licence to rewrite text. A path, a command, an emoji and a
// CJK glyph must survive byte-for-byte: mangling a command would corrupt the
// audit trail the reader is looking at.
func TestScrubChangesNothingButControls(t *testing.T) {
	for _, s := range []string{
		"/Users/a/proj/internal/app/tui/steps.go",
		"cd /w && pytest -q --junitxml=/tmp/a.xml 2>&1 | tail -40",
		"task 🚀 done",
		"中文任务",
		"rm -rf --no-preserve-root",
	} {
		if got := ScrubText(s); got != s {
			t.Fatalf("ScrubText(%q) = %q", s, got)
		}
		if got := ScrubBlock(s); got != s {
			t.Fatalf("ScrubBlock(%q) = %q", s, got)
		}
	}
}

// A one-line row has to stay one line: a newline in a todo's content would
// split a band row in two and make the height budget undercount.
func TestScrubTextFlattensLineBreaks(t *testing.T) {
	if got := ScrubText("a\tb\nc\rd"); got != "a b c d" {
		t.Fatalf("ScrubText = %q, want %q", got, "a b c d")
	}
}

// A block keeps its line structure: an expanded job card renders the output's
// own lines, and a CRLF should collapse to one break rather than make a blank
// line.
func TestScrubBlockKeepsLineStructure(t *testing.T) {
	in := "line one\nline two\ttabbed"
	if got := ScrubBlock(in); got != in {
		t.Fatalf("ScrubBlock = %q, want %q", got, in)
	}
	if got := ScrubBlock("a\r\nb"); got != "a\nb" {
		t.Fatalf("CRLF = %q, want %q", got, "a\nb")
	}
	if got := ScrubBlock("a\x1b[2Jb\nc"); got != "a[2Jb\nc" {
		t.Fatalf("ScrubBlock = %q", got)
	}
}

// Every renderer may scrub the same string, and the projection scrubs text the
// tree already scrubbed. A second pass must change nothing.
func TestScrubIsIdempotent(t *testing.T) {
	in := "\x1b]0;x\x07a\tb\nc\x1b[2J"
	once := ScrubText(in)
	if twice := ScrubText(once); twice != once {
		t.Fatalf("ScrubText(%q) = %q, want %q", once, twice, once)
	}
	block := ScrubBlock(in)
	if again := ScrubBlock(block); again != block {
		t.Fatalf("ScrubBlock(%q) = %q, want %q", block, again, block)
	}
}

// The band travels to clients that render it on their own terminals, so the
// projection has to be safe for a BandInfo a caller assembled itself.
func TestProjectBandScrubsCallerSuppliedText(t *testing.T) {
	n := ProjectBand(BandInfo{
		Todos: true, Done: 1, Total: 3,
		Active:   "work\x1b]52;c;x\x07",
		Next:     []string{"next\x1b[2J"},
		Entities: []BandEntity{{Ref: NodeID{KindTool, "tool:1"}, Label: "job-1", Detail: "run\x1b]0;t\x07"}},
	})
	if strings.Contains(n.Band.Active, "\x1b") {
		t.Fatalf("active = %q", n.Band.Active)
	}
	if strings.Contains(n.Band.Next[0], "\x1b") {
		t.Fatalf("next = %q", n.Band.Next[0])
	}
	if strings.Contains(n.Band.Entities[0].Detail, "\x1b") {
		t.Fatalf("entity = %+v", n.Band.Entities[0])
	}
}
