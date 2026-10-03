package tui

import (
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func newLiveStripTestModel(t *testing.T) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	return m
}

func TestTruncateURLIsRuneAware(t *testing.T) {
	out := truncateURL("https://例え.com/path/very/long", 12)
	if !utf8.ValidString(out) {
		t.Fatal("output is not valid UTF-8")
	}
	if ansi.StringWidth(out) > 12 {
		t.Fatalf("output exceeds max width: %d", ansi.StringWidth(out))
	}
}

func TestTruncateURL(t *testing.T) {
	cases := []struct {
		url  string
		max  int
		want string
	}{
		{"https://example.com", 30, "example.com"},
		{"http://example.com", 30, "example.com"},
		{"https://example.com", 0, "example.com"},
		{"https://developer.example.com/very/long/path/to/somewhere", 25, "developer.example.com/ve…"},
		{"https://example.com/short", 30, "example.com/short"},
		{"", 30, ""},
	}
	for _, c := range cases {
		got := truncateURL(c.url, c.max)
		if c.want == "" && got == "" {
			continue
		}
		if got != c.want {
			t.Errorf("truncateURL(%q, %d) = %q, want %q", c.url, c.max, got, c.want)
		}
	}
}
