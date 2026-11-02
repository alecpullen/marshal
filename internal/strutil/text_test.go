package strutil

import (
	"strings"
	"testing"
)

func TestElideWorkdir(t *testing.T) {
	root := "/home/a/proj/.marshal/worktrees/x"
	if got := ElideWorkdir("cd "+root+" && pytest -q", root); got != "⌂ pytest -q" {
		t.Fatalf("got %q", got)
	}
	if got := ElideWorkdir("cd /elsewhere && ls", root); got != "cd /elsewhere && ls" {
		t.Fatalf("other dirs stay: %q", got)
	}
	// The separators a shell command uses, and quoting of the root.
	for _, cmd := range []string{"cd " + root + "; ls", `cd "` + root + `" && ls`, "cd '" + root + "' && ls"} {
		if got := ElideWorkdir(cmd, root); !strings.HasPrefix(got, "⌂ ") {
			t.Errorf("ElideWorkdir(%q) = %q", cmd, got)
		}
	}
	if got := ElideWorkdir("cd "+root, root); got != "cd "+root {
		t.Fatalf("a bare cd is not a prefix we elide: %q", got)
	}
	if got := ElideWorkdir("pytest -q", ""); got != "pytest -q" {
		t.Fatalf("no root, no elision: %q", got)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := []struct {
		in, head, rest string
	}{
		{"Reading the config. Then the tests.", "Reading the config.", "Then the tests."},
		{"Short. Then more text.", "Short. Then more text.", ""},
		{"Line one is long\nline two", "Line one is long", "line two"},
		{"  Padded head. Rest  ", "Padded head.", "Rest"},
		{"Is it done? Yes it is.", "Is it done?", "Yes it is."},
		{"Wait!\nNext", "Wait!\nNext", ""},
	}
	for _, c := range cases {
		head, rest := FirstSentence(c.in)
		if head != c.head || rest != c.rest {
			t.Errorf("FirstSentence(%q) = %q, %q; want %q, %q", c.in, head, rest, c.head, c.rest)
		}
	}
}

func TestStripEmphasis(t *testing.T) {
	in := "**Reading** the `config` and *tests* ~~now~~ __here__"
	if got := StripEmphasis(in); got != "Reading the config and tests now here" {
		t.Fatalf("StripEmphasis(%q) = %q", in, got)
	}
}
