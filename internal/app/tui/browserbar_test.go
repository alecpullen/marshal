package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func newBrowserBarTestModel(t *testing.T) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	return m
}

func TestRenderBrowserBarHiddenWhenNoSession(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{SessionOpen: false})
	if bar := m.renderBrowserBar(); bar != "" {
		t.Fatalf("expected empty bar, got:\n%s", bar)
	}
}

func TestRenderBrowserBarShowsURLTitleMode(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/docs",
		Title:       "Example Docs",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	for _, want := range []string{"example.com/docs", "Example Docs", "standalone"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("bar missing %q:\n%s", want, bar)
		}
	}
}

func TestRenderBrowserBarStripsProtocol(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	if strings.Contains(stripped, "https://") {
		t.Fatalf("bar should strip protocol:\n%s", bar)
	}
	if !strings.Contains(stripped, "example.com") {
		t.Fatalf("bar should contain hostname:\n%s", bar)
	}
}

func TestRenderBrowserBarShowsSpinnerWhenActive(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.spinnerFrame = "⠋"
	m.now = func() time.Time { return time.Unix(105, 0) }
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		Active:      true,
		ToolName:    "browser.click",
		URL:         "https://example.com",
		Mode:        "standalone",
		UpdatedAt:   time.Unix(100, 0),
	})
	m.state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "browser.click", StartedAt: time.Unix(100, 0)})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	if !strings.Contains(stripped, "browser.click") {
		t.Fatalf("bar should show active tool name:\n%s", bar)
	}
}

func TestRenderBrowserBarHidesSpinnerWhenIdle(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		Active:      false,
		URL:         "https://example.com",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	stripped := stripANSI(bar)
	if strings.Contains(stripped, "browser.") {
		t.Fatalf("idle bar should not contain tool name:\n%s", bar)
	}
}

func TestRenderBrowserBarNarrowWidth(t *testing.T) {
	m := newBrowserBarTestModel(t)
	m.resize(25, 30)
	m.state.SetBrowserInfo(session.BrowserInfo{
		SessionOpen: true,
		URL:         "https://example.com/some/very/long/path",
		Title:       "Very Long Title That Exceeds Width",
		Mode:        "standalone",
	})
	bar := m.renderBrowserBar()
	if bar == "" {
		t.Fatal("bar should not be empty even on narrow width")
	}
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
