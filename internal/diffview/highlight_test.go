package diffview

import (
	"strings"
	"testing"
)

func TestHighlightGoLine(t *testing.T) {
	out := highlightCode(`func foo() { return "bar" }`, "go")
	if !strings.Contains(out, "func") {
		t.Fatalf("highlight dropped content:\n%s", out)
	}
}

func TestHighlightFallsBackOnUnknownLanguage(t *testing.T) {
	out := highlightCode("plain text", "not-a-language-xyz")
	if out != "plain text" {
		t.Fatalf("fallback should return plain content, got %q", out)
	}
}

func TestDetectLanguageFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"foo.go", "Go"},
		{"foo.py", "Python"},
		{"foo.rs", "Rust"},
		{"foo", "go"},
		{"", "go"},
	}
	for _, c := range cases {
		h := Hunk{FilePath: c.path}
		got := detectLanguage(h)
		if got != c.want {
			t.Errorf("detectLanguage(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
